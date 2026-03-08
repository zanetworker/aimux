import asyncio
import logging
import os
import signal

from google import genai
from coordinator import AgentCoordinator

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)
log = logging.getLogger(__name__)

ROLE = os.environ.get("ROLE", "researcher")
MODEL = os.environ.get("MODEL", "gemini-2.0-flash")
GOOGLE_API_KEY = os.environ.get("GOOGLE_API_KEY", "")


async def run_task(coord, task_id: str, task: dict) -> tuple[str, str]:
    """Execute one task. Returns (result_summary, result_full).

    task dict has bytes keys and bytes values (from Redis hgetall).
    Raises on any failure — caller is responsible for calling fail_task.
    """
    prompt = task.get(b"prompt", b"").decode()

    client = genai.Client(api_key=GOOGLE_API_KEY)
    response = await asyncio.to_thread(
        client.models.generate_content,
        model=MODEL,
        contents=prompt,
    )
    result_text = response.text or ""
    return result_text[:500], result_text


async def main():
    coord = AgentCoordinator(
        redis_url=os.environ["REDIS_URL"],
        team_id=os.environ["TEAM_ID"],
        agent_id=os.environ["AGENT_ID"],
        role=ROLE,
    )
    provider = os.environ.get("PROVIDER", "gemini")
    model = os.environ.get("MODEL", MODEL)
    await coord.register(provider=provider, model=model, namespace=os.environ.get("POD_NAMESPACE", ""))
    log.info("Agent started: id=%s role=%s model=%s provider=%s", coord.agent, ROLE, model, provider)

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
            prompt_preview = task.get(b"prompt", b"").decode()[:80]
            log.info("Claimed task %s: %s...", task_id, prompt_preview)
            try:
                summary, result_full = await run_task(coord, task_id, task)
                await coord.complete_task(task_id, summary, result_full=result_full)
                log.info("Completed task %s (%d chars)", task_id, len(result_full))
                await coord.send(
                    "lead",
                    f"Task {task_id} done: {summary[:200]}",
                    f"Task {task_id} done",
                )
            except Exception as e:
                log.error("Task %s failed: %s", task_id, e)
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
