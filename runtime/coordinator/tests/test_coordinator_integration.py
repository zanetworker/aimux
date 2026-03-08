"""Integration tests for AgentCoordinator.

Unlike the unit tests in test_coordinator.py (which mock r.eval), these tests
run the real Lua claim script through fakeredis so that the atomic Lua
semantics—role checking, dependency resolution, and status transitions—are
exercised end-to-end without a live Redis server.

All async tests use pytest-asyncio.  Each test creates its own FakeAsyncRedis
instance so there is no shared state between tests.

Scenarios covered here are NOT covered by the existing unit tests:
  1. Two agents competing for the same task (atomicity)
  2. Task retry lifecycle through to dead status (3 retries → dead)
  3. Full result storage — truncated summary vs full text
  4. Broadcast delivery to multiple agents via XREADGROUP
  5. Dependent task not claimable until dependency is completed
  6. Heartbeat timestamp advances on successive calls
"""

import asyncio
import sys
import time
from pathlib import Path

import fakeredis
import pytest
import pytest_asyncio

# Make the coordinator package importable without installing it.
COORDINATOR_DIR = Path(__file__).parent.parent
sys.path.insert(0, str(COORDINATOR_DIR.parent))

from coordinator.coordinator import AgentCoordinator  # noqa: E402

# ---------------------------------------------------------------------------
# pytest-asyncio configuration
# ---------------------------------------------------------------------------
pytest_plugins = ("pytest_asyncio",)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_coord(fake_redis, *, team="integ-team", agent="agent-1", role="coder"):
    """Return an AgentCoordinator wired to a shared FakeAsyncRedis instance.

    Uses __new__ + manual field assignment to bypass __init__, which would
    create a real redis.asyncio client against a live server.
    """
    coord = AgentCoordinator.__new__(AgentCoordinator)
    coord.r = fake_redis
    coord.team = team
    coord.agent = agent
    coord.role = role
    return coord


# ---------------------------------------------------------------------------
# Test 1: test_two_agents_only_one_claims
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_two_agents_only_one_claims():
    """Two coordinators compete for a single pending task.

    Because the Lua script atomically changes status to 'claimed' and removes
    the task from tasks:pending in one round-trip, only one agent should win.

    The calls are sequential here (single event loop, no concurrency), which
    is sufficient to prove the Lua script's guard: once agent-1 claims the
    task, the task hash's status is no longer 'pending', so agent-2's Lua
    evaluation returns 0 (already taken) and claim_task() returns None.
    """
    r = fakeredis.FakeAsyncRedis()
    try:
        coord1 = _make_coord(r, agent="agent-1", role="coder")
        coord2 = _make_coord(r, agent="agent-2", role="coder")

        # Seed one pending task that both agents can claim.
        await coord1.create_task("task1", "do something", required_role="coder")

        # Both attempt to claim — whichever runs first wins.
        result1 = await coord1.claim_task()
        result2 = await coord2.claim_task()

        # Exactly one claim must succeed.
        claimed = [x for x in [result1, result2] if x is not None]
        assert len(claimed) == 1, (
            f"Expected exactly one successful claim; got result1={result1!r}, result2={result2!r}"
        )
        assert claimed[0] == "task1"

        # Verify Redis state: task must be 'claimed', removed from pending set.
        status = await r.hget("team:integ-team:task:task1", "status")
        assert status == b"claimed"

        in_pending = await r.zscore("team:integ-team:tasks:pending", "task1")
        assert in_pending is None, "Claimed task must be removed from tasks:pending"

        # Verify assignee is one of the two agents.
        assignee = await r.hget("team:integ-team:task:task1", "assignee")
        assert assignee in (b"agent-1", b"agent-2")
    finally:
        await r.aclose()


# ---------------------------------------------------------------------------
# Test 2: test_task_retry_then_dead
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_task_retry_then_dead():
    """A task transitions through retry states before becoming dead.

    The coordinator's fail_task() logic uses the condition `if retry >= 3`
    on the CURRENT (pre-increment) retry_count to decide whether to mark the
    task dead.  Starting from retry_count=0:

      Call 1: retry=0 (0 < 3) → status=pending,  retry_count=1
      Call 2: retry=1 (1 < 3) → status=pending,  retry_count=2
      Call 3: retry=2 (2 < 3) → status=pending,  retry_count=3
      Call 4: retry=3 (3 >= 3) → status=dead,    retry_count=4

    Three failures are therefore NOT enough to kill the task; four are.
    This test asserts the actual code behaviour rather than the docstring
    description (which says "3 retries → dead") so that any future change
    to the threshold is caught.
    """
    r = fakeredis.FakeAsyncRedis()
    try:
        coord = _make_coord(r)
        task_id = "task-retry-dead"

        # Seed a claimed task with retry_count=0.
        await r.hset(
            f"team:integ-team:task:{task_id}",
            mapping={"status": "claimed", "retry_count": "0"},
        )

        # --- Failure 1 ---
        await coord.fail_task(task_id, "error #1")
        retry = await r.hget(f"team:integ-team:task:{task_id}", "retry_count")
        status = await r.hget(f"team:integ-team:task:{task_id}", "status")
        assert retry == b"1", f"After fail 1: expected retry_count=1, got {retry}"
        assert status == b"pending", "After fail 1: task must be pending"
        # Re-claim simulation: remove from pending and mark claimed again.
        await r.hset(f"team:integ-team:task:{task_id}", mapping={"status": "claimed"})
        await r.zrem(f"team:integ-team:tasks:pending", task_id)

        # --- Failure 2 ---
        await coord.fail_task(task_id, "error #2")
        retry = await r.hget(f"team:integ-team:task:{task_id}", "retry_count")
        status = await r.hget(f"team:integ-team:task:{task_id}", "status")
        assert retry == b"2", f"After fail 2: expected retry_count=2, got {retry}"
        assert status == b"pending", "After fail 2: task must be pending"
        await r.hset(f"team:integ-team:task:{task_id}", mapping={"status": "claimed"})
        await r.zrem(f"team:integ-team:tasks:pending", task_id)

        # --- Failure 3 ---
        await coord.fail_task(task_id, "error #3")
        retry = await r.hget(f"team:integ-team:task:{task_id}", "retry_count")
        status = await r.hget(f"team:integ-team:task:{task_id}", "status")
        assert retry == b"3", f"After fail 3: expected retry_count=3, got {retry}"
        assert status == b"pending", (
            "After fail 3: task must still be pending (dead threshold is retry>=3 on pre-increment count)"
        )
        # Task must be back in pending for a 4th attempt.
        score = await r.zscore(f"team:integ-team:tasks:pending", task_id)
        assert score is not None, "Task must be in tasks:pending after fail 3"
        await r.hset(f"team:integ-team:task:{task_id}", mapping={"status": "claimed"})
        await r.zrem(f"team:integ-team:tasks:pending", task_id)

        # --- Failure 4 — task goes dead ---
        await coord.fail_task(task_id, "error #4")
        retry = await r.hget(f"team:integ-team:task:{task_id}", "retry_count")
        status = await r.hget(f"team:integ-team:task:{task_id}", "status")
        assert retry == b"4", f"After fail 4: expected retry_count=4, got {retry}"
        assert status == b"dead", "After fail 4 (retry_count was 3): task must be dead"

        # Dead task must NOT appear in tasks:pending.
        in_pending = await r.zscore(f"team:integ-team:tasks:pending", task_id)
        assert in_pending is None, "Dead task must not be in tasks:pending"

        # Verify error field was recorded.
        error = await r.hget(f"team:integ-team:task:{task_id}", "error")
        assert error == b"error #4"
    finally:
        await r.aclose()


# ---------------------------------------------------------------------------
# Test 3: test_complete_task_stores_full_result
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_complete_task_stores_full_result():
    """complete_task() stores a truncated summary in the hash and the full
    result in a separate Redis key.

    result_summary in the task hash must be capped at 500 characters.
    get_task_result_full() must return the untruncated original string.
    """
    r = fakeredis.FakeAsyncRedis()
    try:
        coord = _make_coord(r)
        task_id = "task-full-result"

        # Create a result string longer than 500 characters.
        result_full = "A" * 300 + "B" * 300  # 600 chars total

        # Seed a minimal claimed task hash.
        await r.hset(
            f"team:integ-team:task:{task_id}",
            mapping={"status": "claimed"},
        )

        await coord.complete_task(task_id, result_full, result_full=result_full)

        # --- Verify task hash ---
        status = await r.hget(f"team:integ-team:task:{task_id}", "status")
        assert status == b"completed"

        stored_summary = await r.hget(f"team:integ-team:task:{task_id}", "result_summary")
        assert stored_summary is not None
        assert len(stored_summary) == 500, (
            f"result_summary must be truncated to 500 chars; got {len(stored_summary)}"
        )
        # The truncated summary should be the first 500 chars of result_full.
        assert stored_summary == result_full[:500].encode()

        # --- Verify separate full-result key ---
        full_from_redis = await coord.get_task_result_full(task_id)
        assert full_from_redis == result_full, (
            "get_task_result_full() must return the complete untruncated string"
        )
        assert len(full_from_redis) == 600

        # Confirm the full result key exists directly in Redis.
        raw = await r.get(f"team:integ-team:task:{task_id}:result_full")
        assert raw == result_full.encode()
    finally:
        await r.aclose()


# ---------------------------------------------------------------------------
# Test 4: test_broadcast_reaches_multiple_agents
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_broadcast_reaches_multiple_agents():
    """One agent broadcasts; both registered agents receive it via receive().

    Each agent has its own consumer group on the events stream so that every
    broadcast message is delivered independently to each consumer.

    fakeredis supports XREADGROUP with separate consumer groups.  If it
    raises an exception (e.g. due to a version limitation), the test is
    skipped with a clear message.
    """
    r = fakeredis.FakeAsyncRedis()
    try:
        coord1 = _make_coord(r, agent="agent-1", role="coder")
        coord2 = _make_coord(r, agent="agent-2", role="coder")

        # Register both agents so their per-agent consumer groups exist.
        await coord1.register(provider="claude", model="claude-opus-4-5")
        await coord2.register(provider="claude", model="claude-opus-4-5")

        # Agent 1 broadcasts a message.
        broadcast_text = "Hello from agent-1"
        await coord1.broadcast(broadcast_text, summary="greeting")

        # Both agents call receive() — timeout 0 to avoid blocking.
        try:
            msgs1 = await coord1.receive(timeout_ms=0)
            msgs2 = await coord2.receive(timeout_ms=0)
        except Exception as exc:
            pytest.skip(f"fakeredis xreadgroup limitation: {exc}")

        # Each agent must have received exactly one message on the events stream.
        events1 = [
            (stream, mid, data)
            for stream, mid, data in msgs1
            if b":events" in stream.encode() or ":events" in stream
        ]
        events2 = [
            (stream, mid, data)
            for stream, mid, data in msgs2
            if b":events" in stream.encode() or ":events" in stream
        ]

        assert len(events1) == 1, (
            f"agent-1 must receive 1 broadcast event; got {len(events1)}"
        )
        assert len(events2) == 1, (
            f"agent-2 must receive 1 broadcast event; got {len(events2)}"
        )

        # Both should contain the same text.
        for _stream, _mid, data in events1 + events2:
            text_val = data.get(b"text", data.get("text", b""))
            if isinstance(text_val, bytes):
                text_val = text_val.decode()
            assert text_val == broadcast_text, (
                f"Received text {text_val!r} does not match broadcast {broadcast_text!r}"
            )

        # Verify the events stream in Redis has exactly one entry.
        stream_len = await r.xlen(f"team:integ-team:events")
        assert stream_len == 1
    finally:
        await r.aclose()


# ---------------------------------------------------------------------------
# Test 5: test_dependent_task_not_claimable_until_dep_done
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_dependent_task_not_claimable_until_dep_done():
    """A task with an unmet dependency cannot be claimed.

    Flow:
      1. Create taskA (no dependencies).
      2. Create taskB (depends_on=["taskA"]).
      3. Agent claims next task — gets taskA (earliest in FIFO queue).
      4. Agent tries to claim again — taskB's dep is unmet, returns None.
      5. Agent completes taskA (status → 'completed').
      6. Agent claims again — now gets taskB because dep is satisfied.

    This exercises the full Lua dependency-check path (the `deps_json` branch
    in claim_task.lua) without any mocking.
    """
    r = fakeredis.FakeAsyncRedis()
    try:
        coord = _make_coord(r, agent="agent-1", role="coder")

        # Create taskA (no deps) then taskB (depends on taskA).
        # Small sleep between creates to ensure FIFO ordering by timestamp score.
        await coord.create_task("taskA", "do A", required_role="coder")
        await asyncio.sleep(0.01)
        await coord.create_task("taskB", "do B", required_role="coder", depends_on=["taskA"])

        # Claim 1: should grab taskA (first in queue, no deps).
        first_claim = await coord.claim_task()
        assert first_claim == "taskA", f"Expected taskA; got {first_claim!r}"

        # Verify taskA is now claimed.
        status_a = await r.hget("team:integ-team:task:taskA", "status")
        assert status_a == b"claimed"

        # Claim 2: taskB's dep (taskA) is not yet completed → Lua returns -2 → None.
        second_claim = await coord.claim_task()
        assert second_claim is None, (
            f"taskB must not be claimable while taskA is not completed; got {second_claim!r}"
        )

        # Verify taskB is still pending.
        status_b = await r.hget("team:integ-team:task:taskB", "status")
        assert status_b == b"pending"

        # Complete taskA.
        await coord.complete_task("taskA", "A is done")
        status_a_after = await r.hget("team:integ-team:task:taskA", "status")
        assert status_a_after == b"completed"

        # Claim 3: taskB's dep is now satisfied → should claim taskB.
        third_claim = await coord.claim_task()
        assert third_claim == "taskB", (
            f"Expected taskB after taskA completed; got {third_claim!r}"
        )

        # Verify taskB is now claimed.
        status_b_after = await r.hget("team:integ-team:task:taskB", "status")
        assert status_b_after == b"claimed"

        # TaskB must be removed from tasks:pending.
        in_pending = await r.zscore("team:integ-team:tasks:pending", "taskB")
        assert in_pending is None, "Claimed taskB must be removed from tasks:pending"
    finally:
        await r.aclose()


# ---------------------------------------------------------------------------
# Test 6: test_heartbeat_updates_timestamp
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_heartbeat_updates_timestamp():
    """heartbeat() writes an advancing Unix timestamp to the team heartbeat hash.

    Steps:
      1. Call heartbeat() and record the stored timestamp (t1).
      2. Sleep 1 second.
      3. Call heartbeat() again and record the new timestamp (t2).
      4. Assert t2 > t1 (timestamp advanced).
      5. Assert t2 is within 2 seconds of wall-clock now (freshness check).
    """
    r = fakeredis.FakeAsyncRedis()
    try:
        coord = _make_coord(r, agent="agent-hb", role="coder")

        # First heartbeat.
        before = time.time()
        await coord.heartbeat()
        raw1 = await r.hget(f"team:integ-team:heartbeat", "agent-hb")
        assert raw1 is not None, "heartbeat() must write a value for the agent key"
        t1 = float(raw1)
        assert t1 >= before, "Stored timestamp must not predate the call"

        # Wait long enough to guarantee the clock advances.
        await asyncio.sleep(1)

        # Second heartbeat.
        await coord.heartbeat()
        raw2 = await r.hget(f"team:integ-team:heartbeat", "agent-hb")
        assert raw2 is not None
        t2 = float(raw2)

        assert t2 > t1, (
            f"Second heartbeat timestamp ({t2}) must be greater than first ({t1})"
        )

        # Freshness: t2 should be within 2 seconds of now.
        now = time.time()
        assert now - t2 < 2, (
            f"Heartbeat timestamp {t2} is too stale relative to now {now}"
        )

        # Verify the heartbeat hash key structure in Redis directly.
        all_fields = await r.hgetall(f"team:integ-team:heartbeat")
        assert b"agent-hb" in all_fields, (
            "agent-hb key must exist in the team heartbeat hash"
        )
    finally:
        await r.aclose()
