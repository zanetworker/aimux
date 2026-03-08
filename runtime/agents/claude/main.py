import asyncio
import os
import signal
import subprocess

from claude_code_sdk import query, ClaudeCodeOptions, AssistantMessage
from coordinator import AgentCoordinator

WORKSPACE = "/workspace"
ROLE = os.environ.get("ROLE", "coder")
ALLOWED_TOOLS = os.environ.get("ALLOWED_TOOLS", "Read,Grep,Glob").split(",")
GIT_TOKEN = os.environ.get("GIT_TOKEN", "")
GIT_HOST = os.environ.get("GIT_HOST", "")


def git(*args):
    """Run a git command in /workspace. Raises CalledProcessError on failure."""
    subprocess.run(["git"] + list(args), cwd=WORKSPACE, check=True)


def configure_git_auth():
    """Set remote URL with token so git push works without interactive auth."""
    if GIT_TOKEN and GIT_HOST:
        git("remote", "set-url", "origin", f"https://{GIT_TOKEN}@{GIT_HOST}")


async def run_task(coord, task_id: str, task: dict) -> tuple[str, str]:
    """Execute one task. Returns (result_summary, result_ref).

    task dict has bytes keys and bytes values (from Redis hgetall).
    Raises on any failure — caller is responsible for calling fail_task.
    """
    prompt = task.get(b"prompt", b"").decode()
    source_branch = task.get(b"source_branch", b"").decode()

    # Pull source branch if this task depends on a prior task's file output.
    if source_branch:
        git("fetch", "origin", source_branch)
        git("checkout", source_branch)

    # Run the LLM agent — collect text blocks from AssistantMessage only.
    result_text = ""
    async for msg in query(
        prompt=prompt,
        options=ClaudeCodeOptions(allowed_tools=ALLOWED_TOOLS),
    ):
        if isinstance(msg, AssistantMessage):
            for block in msg.content:
                if hasattr(block, "text"):
                    result_text += block.text

    result_ref = ""

    # Coders commit and push their file changes to a task-specific branch.
    if ROLE == "coder":
        configure_git_auth()
        branch = f"task-{task_id}"
        git("checkout", "-b", branch)
        git("add", "-A")
        git("commit", "-m", f"task {task_id}: {result_text[:80]}")
        git("push", "origin", branch)
        result_ref = f"branch:{branch}"

    # Reviewers are read-only — result_ref is empty string, not None.
    return result_text[:500], result_ref


async def main():
    coord = AgentCoordinator(
        redis_url=os.environ["REDIS_URL"],
        team_id=os.environ["TEAM_ID"],
        agent_id=os.environ["AGENT_ID"],
        role=ROLE,
    )
    await coord.register(
        provider=os.environ.get("PROVIDER", "claude"),
        model=os.environ.get("MODEL", "default"),
        namespace=os.environ.get("POD_NAMESPACE", ""),
    )

    # Heartbeat runs as an independent asyncio task so long LLM calls
    # (which can take 60s+) don't prevent the heartbeat from firing every 10s.
    async def heartbeat_loop():
        while True:
            await coord.heartbeat()
            await asyncio.sleep(10)

    asyncio.create_task(heartbeat_loop())

    # Graceful shutdown on SIGTERM (K8s preStop) and SIGINT (Ctrl-C).
    loop = asyncio.get_event_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, lambda: asyncio.create_task(shutdown(coord)))

    # Main work loop.
    while True:
        # 1. Check for incoming messages (inbox + events broadcast).
        messages = await coord.receive(timeout_ms=2000)
        for stream, msg_id, data in messages:
            if data.get(b"type") == b"shutdown_request":
                # Graceful lead-requested shutdown: notify lead, then exit.
                await coord.send("lead", "Shutting down", "shutdown approved")
                await coord.deregister()
                return
            # Always ack after processing, even for unknown message types.
            await coord.ack(stream, msg_id)

        # 2. Look for available work.
        task_id = await coord.claim_task()
        if task_id:
            task = await coord.r.hgetall(f"team:{coord.team}:task:{task_id}")
            try:
                summary, result_ref = await run_task(coord, task_id, task)
                await coord.complete_task(task_id, summary, result_ref=result_ref)
                await coord.send(
                    "lead",
                    f"Task {task_id} done: {summary[:200]}",
                    f"Task {task_id} done",
                )
            except Exception as e:
                # Never swallow exceptions — always report failure to Redis.
                await coord.fail_task(task_id, str(e))
                await coord.send(
                    "lead",
                    f"Task {task_id} failed: {e}",
                    f"Task {task_id} failed",
                )

        # Avoid busy-waiting when no tasks are available.
        await asyncio.sleep(1)


async def shutdown(coord):
    """Graceful shutdown: deregister from Redis before exit so heartbeat is cleaned up."""
    await coord.deregister()
    raise SystemExit(0)


if __name__ == "__main__":
    asyncio.run(main())
