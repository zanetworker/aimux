"""AgentCoordinator — Redis-backed coordination library for K8s agents.

Each agent instance creates one coordinator. The coordinator handles:
- Registration and deregistration in Redis
- Direct messaging (per-agent inbox stream)
- Broadcast messaging (shared events stream with per-agent consumer groups)
- Atomic task claiming via Lua script
- Task lifecycle: create → claim → complete/fail
- Heartbeat updates
- Token cost reporting
"""

import json
import time
from pathlib import Path

import redis.asyncio as redis

# Load the Lua script once at module import time.
# Path is resolved relative to this file so it works regardless of cwd.
_LUA_DIR = Path(__file__).parent / "lua"
_CLAIM_SCRIPT = (_LUA_DIR / "claim_task.lua").read_text()


class AgentCoordinator:
    """Coordinator for a single agent instance connected to a shared Redis team."""

    def __init__(
        self,
        redis_url: str,
        team_id: str,
        agent_id: str,
        role: str = "",
    ) -> None:
        self.r = redis.from_url(redis_url)
        self.team = team_id
        self.agent = agent_id
        self.role = role

    # ------------------------------------------------------------------ #
    # Setup                                                                #
    # ------------------------------------------------------------------ #

    async def register(self, provider: str, model: str, namespace: str = "") -> None:
        """Register this agent in Redis on startup.

        Creates two consumer groups:
        - Per-agent group on the events stream for broadcast delivery (each
          agent gets every broadcast message).
        - Shared 'agents' group on the inbox stream so unacked messages
          redeliver after a restart.

        BusyGroup errors (group already exists) are silenced — they are
        expected on pod restart.  All other Redis errors propagate.
        """
        # Per-agent consumer group for the broadcast events stream.
        # id="$" means only new messages, not history.
        try:
            await self.r.xgroup_create(
                f"team:{self.team}:events",
                f"agent-{self.agent}",
                id="$",
                mkstream=True,
            )
        except redis.ResponseError as exc:
            if "BUSYGROUP" not in str(exc):
                raise

        # Shared consumer group for this agent's inbox.
        # id="0" means replay unacked messages from the beginning (safe on restart).
        try:
            await self.r.xgroup_create(
                f"team:{self.team}:inbox:{self.agent}",
                "agents",
                id="0",
                mkstream=True,
            )
        except redis.ResponseError as exc:
            if "BUSYGROUP" not in str(exc):
                raise

        await self.r.hset(
            f"team:{self.team}:agent:{self.agent}",
            mapping={
                "provider": provider,
                "role": self.role,
                "model": model,
                "namespace": namespace,
                "registered_at": str(time.time()),
            },
        )

    async def deregister(self) -> None:
        """Clean up this agent's Redis keys on graceful shutdown."""
        await self.r.hdel(f"team:{self.team}:heartbeat", self.agent)
        await self.r.delete(f"team:{self.team}:agent:{self.agent}")
        await self.r.delete(f"team:{self.team}:cost:{self.agent}")

    # ------------------------------------------------------------------ #
    # Messaging                                                            #
    # ------------------------------------------------------------------ #

    async def send(self, to: str, text: str, summary: str = "") -> None:
        """Send a direct message to another agent's inbox stream."""
        await self.r.xadd(
            f"team:{self.team}:inbox:{to}",
            {
                "from": self.agent,
                "text": text,
                "summary": summary,
                "timestamp": str(time.time()),
            },
            maxlen=1000,
        )

    async def broadcast(self, text: str, summary: str = "") -> None:
        """Publish a message to the shared events stream (all agents receive it)."""
        await self.r.xadd(
            f"team:{self.team}:events",
            {
                "from": self.agent,
                "type": "broadcast",
                "text": text,
                "summary": summary,
                "timestamp": str(time.time()),
            },
            maxlen=10000,
        )

    async def receive(self, timeout_ms: int = 5000) -> list[tuple[str, str, dict]]:
        """Read pending messages from both the inbox and events streams.

        Reads from:
        - team:{team}:inbox:{agent}  via the shared 'agents' consumer group
        - team:{team}:events          via the per-agent 'agent-{id}' consumer group

        Returns a list of (stream_name, msg_id, data_dict) tuples.
        The caller must call ack(stream, msg_id) after processing each message.
        Returns an empty list if no messages are available — never raises on
        timeout or empty results.
        """
        streams_and_groups = [
            (
                f"team:{self.team}:inbox:{self.agent}",
                "agents",
            ),
            (
                f"team:{self.team}:events",
                f"agent-{self.agent}",
            ),
        ]

        messages: list[tuple[str, str, dict]] = []
        for stream, group in streams_and_groups:
            try:
                results = await self.r.xreadgroup(
                    group,
                    self.agent,
                    {stream: ">"},
                    count=10,
                    block=timeout_ms,
                )
                if not results:
                    continue
                for stream_name, entries in results:
                    sname = (
                        stream_name.decode()
                        if isinstance(stream_name, bytes)
                        else stream_name
                    )
                    for msg_id, data in entries:
                        mid = (
                            msg_id.decode() if isinstance(msg_id, bytes) else msg_id
                        )
                        messages.append((sname, mid, data))
            except Exception:
                # Empty results, stream does not exist yet, or timeout — all benign.
                pass
        return messages

    async def ack(self, stream: str, msg_id: str) -> None:
        """Acknowledge a message after processing.

        The consumer group is inferred from the stream name:
        - events stream  → per-agent group (agent-{id})
        - inbox stream   → shared 'agents' group
        """
        group = f"agent-{self.agent}" if ":events" in stream else "agents"
        await self.r.xack(stream, group, msg_id)

    # ------------------------------------------------------------------ #
    # Tasks                                                                #
    # ------------------------------------------------------------------ #

    async def create_task(
        self,
        task_id: str,
        prompt: str,
        required_role: str = "",
        depends_on: list[str] | None = None,
    ) -> None:
        """Create a new task and add it to the pending work queue.

        Writes all task fields to a Redis hash and adds the task to the
        tasks:pending sorted set (score = creation timestamp for FIFO ordering).
        """
        await self.r.hset(
            f"team:{self.team}:task:{task_id}",
            mapping={
                "status": "pending",
                "prompt": prompt,
                "required_role": required_role,
                "assignee": "",
                "result_summary": "",
                "error": "",
                "depends_on": json.dumps(depends_on or []),
                "retry_count": "0",
                "created_at": str(time.time()),
            },
        )
        score = time.time()
        await self.r.zadd(f"team:{self.team}:tasks:pending", {task_id: score})

    async def claim_task(self) -> str | None:
        """Try to claim the next available task matching this agent's role.

        Iterates tasks:pending in ascending score order (FIFO).  For each
        task, runs the atomic Lua script which checks status, role, and
        dependencies in one round-trip.

        Returns the claimed task_id on success, None if no claimable task
        exists.
        """
        pending = await self.r.zrange(f"team:{self.team}:tasks:pending", 0, -1)
        for tid in pending:
            task_id = tid.decode() if isinstance(tid, bytes) else tid
            result = await self.r.eval(
                _CLAIM_SCRIPT,
                2,
                f"team:{self.team}:task:{task_id}",
                f"team:{self.team}",
                self.agent,
                self.role,
                task_id,
            )
            if result == 1:
                return task_id
        return None

    async def complete_task(
        self, task_id: str, result_summary: str, result_full: str = ""
    ) -> None:
        """Mark a task as completed.

        result_summary (truncated to 500 chars) is stored in the task hash for
        quick status checks. result_full (unlimited) is stored in a separate
        Redis key so dependent tasks can read the complete output.
        """
        pipe = self.r.pipeline()
        pipe.hset(
            f"team:{self.team}:task:{task_id}",
            mapping={
                "status": "completed",
                "result_summary": result_summary[:500],
                "completed_at": str(time.time()),
            },
        )
        if result_full:
            pipe.set(f"team:{self.team}:task:{task_id}:result_full", result_full)
        await pipe.execute()

    async def get_task_result_full(self, task_id: str) -> str:
        """Fetch the full result text for a completed task, or empty string."""
        val = await self.r.get(f"team:{self.team}:task:{task_id}:result_full")
        if val is None:
            return ""
        return val.decode() if isinstance(val, bytes) else val

    async def fail_task(self, task_id: str, error: str) -> None:
        """Record a task failure and decide whether to retry or mark dead.

        Increments retry_count.  If the new count reaches 3, status is set
        to 'dead' (needs human intervention).  Otherwise the task is
        returned to tasks:pending for another agent to pick up.
        """
        raw = await self.r.hget(f"team:{self.team}:task:{task_id}", "retry_count")
        retry = int(raw or 0)
        new_retry = retry + 1

        if retry >= 3:
            new_status = "dead"
        else:
            new_status = "pending"
            await self.r.zadd(
                f"team:{self.team}:tasks:pending", {task_id: time.time()}
            )

        await self.r.hset(
            f"team:{self.team}:task:{task_id}",
            mapping={
                "status": new_status,
                "error": error,
                "assignee": "",
                "retry_count": str(new_retry),
            },
        )

    # ------------------------------------------------------------------ #
    # Heartbeat                                                            #
    # ------------------------------------------------------------------ #

    async def heartbeat(self) -> None:
        """Update this agent's heartbeat timestamp.

        Should be called every 10 seconds from an independent asyncio task
        so long-running LLM API calls don't block it.
        """
        await self.r.hset(
            f"team:{self.team}:heartbeat", self.agent, str(time.time())
        )

    # ------------------------------------------------------------------ #
    # Cost                                                                 #
    # ------------------------------------------------------------------ #

    async def report_tokens(self, tokens_in: int, tokens_out: int) -> None:
        """Atomically increment the cumulative token counters for this agent."""
        key = f"team:{self.team}:cost:{self.agent}"
        pipe = self.r.pipeline()
        pipe.hincrby(key, "tokens_in", tokens_in)
        pipe.hincrby(key, "tokens_out", tokens_out)
        await pipe.execute()
