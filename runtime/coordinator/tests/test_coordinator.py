"""Tests for AgentCoordinator.

Uses fakeredis.FakeAsyncRedis to mock Redis for all operations except
Lua eval (which fakeredis does not implement).  claim_task() calls
r.eval() with the Lua script; those tests mock r.eval via
unittest.mock.AsyncMock to simulate the Lua return values.

All tests use pytest-asyncio in auto mode.
"""

import json
import sys
import time
import types
from pathlib import Path
from unittest.mock import AsyncMock, patch

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

def _make_coord(fake_redis, *, team="test-team", agent="agent-1", role="coder"):
    """Return an AgentCoordinator wired to a pre-created FakeAsyncRedis."""
    coord = AgentCoordinator(
        redis_url="redis://localhost",
        team_id=team,
        agent_id=agent,
        role=role,
    )
    # Replace the real async Redis client with the fake one.
    coord.r = fake_redis
    return coord


@pytest_asyncio.fixture
async def r():
    """Fresh FakeAsyncRedis instance for each test."""
    client = fakeredis.FakeAsyncRedis()
    yield client
    await client.aclose()


@pytest_asyncio.fixture
async def coord(r):
    """AgentCoordinator using the FakeAsyncRedis fixture."""
    return _make_coord(r)


# ---------------------------------------------------------------------------
# 1. test_register_creates_consumer_groups
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_register_creates_consumer_groups(r):
    """register() must call xgroup_create for both the events stream
    and the inbox stream.  We verify by patching xgroup_create and
    checking the call arguments.
    """
    coord = _make_coord(r)

    calls = []

    original = r.xgroup_create

    async def recording_xgroup_create(stream, group, id="$", mkstream=False):
        calls.append((stream, group))
        # Use the real fakeredis implementation so state is consistent.
        return await original(stream, group, id=id, mkstream=mkstream)

    r.xgroup_create = recording_xgroup_create

    await coord.register(provider="claude", model="claude-opus-4-5")

    stream_names = [c[0] for c in calls]
    assert f"team:test-team:events" in stream_names, (
        "xgroup_create must be called for the events stream"
    )
    assert f"team:test-team:inbox:agent-1" in stream_names, (
        "xgroup_create must be called for the inbox stream"
    )

    # Also verify the agent hash was written.
    val = await r.hget("team:test-team:agent:agent-1", "provider")
    assert val == b"claude"


# ---------------------------------------------------------------------------
# 2. test_claim_task_success
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_claim_task_success(coord, r):
    """Seed a pending task; eval returns 1 (claimed).
    claim_task() should return the task_id.
    """
    await r.zadd("team:test-team:tasks:pending", {"task-abc": time.time()})

    with patch.object(r, "eval", new=AsyncMock(return_value=1)):
        result = await coord.claim_task()

    assert result == "task-abc"


# ---------------------------------------------------------------------------
# 3. test_claim_task_wrong_role
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_claim_task_wrong_role(r):
    """Task requires role=coder; agent has role=reviewer.
    Lua returns -1 (wrong role). claim_task() should return None.
    """
    coord = _make_coord(r, role="reviewer")
    await r.zadd("team:test-team:tasks:pending", {"task-xyz": time.time()})

    with patch.object(r, "eval", new=AsyncMock(return_value=-1)):
        result = await coord.claim_task()

    assert result is None


# ---------------------------------------------------------------------------
# 4. test_claim_task_dependency_not_met
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_claim_task_dependency_not_met(coord, r):
    """Task has depends_on=[dep1]; dep1 is not completed.
    Lua returns -2 (dependency not met). claim_task() should return None.
    """
    await r.zadd("team:test-team:tasks:pending", {"task-dep": time.time()})
    # dep1 exists but is still pending (not completed)
    await r.hset("team:test-team:task:dep1", mapping={"status": "pending"})

    with patch.object(r, "eval", new=AsyncMock(return_value=-2)):
        result = await coord.claim_task()

    assert result is None


# ---------------------------------------------------------------------------
# 5. test_complete_task_stores_full_result
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_complete_task_stores_full_result(coord, r):
    """complete_task() stores truncated summary in the task hash and the
    full result in a separate Redis key. status must be set to completed.
    """
    task_id = "task-001"
    await r.hset(f"team:test-team:task:{task_id}", mapping={"status": "claimed"})

    summary = "Refactored users endpoint, all tests pass"
    full = summary + " " + ("x" * 500)  # >500 chars total

    await coord.complete_task(task_id, summary, result_full=full)

    stored_status = await r.hget(f"team:test-team:task:{task_id}", "status")
    stored_summary = await r.hget(f"team:test-team:task:{task_id}", "result_summary")
    stored_full = await r.get(f"team:test-team:task:{task_id}:result_full")

    assert stored_status == b"completed"
    assert stored_summary == summary.encode()
    assert stored_full == full.encode()


# ---------------------------------------------------------------------------
# 6. test_fail_task_retry
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_fail_task_retry(coord, r):
    """Failing a task twice (retry_count goes 0→1→2) must keep status=pending
    and re-add the task to tasks:pending each time.
    """
    task_id = "task-retry"
    await r.hset(
        f"team:test-team:task:{task_id}",
        mapping={"status": "claimed", "retry_count": "0"},
    )

    # First failure
    await coord.fail_task(task_id, "network error")
    retry_count = await r.hget(f"team:test-team:task:{task_id}", "retry_count")
    status = await r.hget(f"team:test-team:task:{task_id}", "status")
    assert retry_count == b"1"
    assert status == b"pending"

    # Simulate re-claiming: mark claimed again and remove from pending
    await r.hset(f"team:test-team:task:{task_id}", mapping={"status": "claimed"})
    await r.zrem("team:test-team:tasks:pending", task_id)

    # Second failure
    await coord.fail_task(task_id, "timeout")
    retry_count = await r.hget(f"team:test-team:task:{task_id}", "retry_count")
    status = await r.hget(f"team:test-team:task:{task_id}", "status")
    assert retry_count == b"2"
    assert status == b"pending"

    # Task must be present in tasks:pending
    score = await r.zscore("team:test-team:tasks:pending", task_id)
    assert score is not None, "failed task must be re-added to tasks:pending"


# ---------------------------------------------------------------------------
# 7. test_fail_task_dead
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_fail_task_dead(coord, r):
    """After 3 failures (retry_count reaching 3), status must become 'dead'
    and the task must NOT be re-added to tasks:pending.
    """
    task_id = "task-dead"
    # Seed at retry_count=3 (already failed 3 times).
    await r.hset(
        f"team:test-team:task:{task_id}",
        mapping={"status": "claimed", "retry_count": "3"},
    )

    await coord.fail_task(task_id, "fatal error")

    status = await r.hget(f"team:test-team:task:{task_id}", "status")
    retry_count = await r.hget(f"team:test-team:task:{task_id}", "retry_count")
    in_pending = await r.zscore("team:test-team:tasks:pending", task_id)

    assert status == b"dead", "status must be dead after 3+ failures"
    assert retry_count == b"4"
    assert in_pending is None, "dead task must not be in tasks:pending"


# ---------------------------------------------------------------------------
# 8. test_create_task_with_depends_on
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_create_task_with_depends_on(coord, r):
    """create_task() stores depends_on as JSON and adds the task to tasks:pending."""
    task_id = "task-dep"

    await coord.create_task(
        task_id=task_id,
        prompt="Review the refactored /users endpoint",
        required_role="reviewer",
        depends_on=["a3f2bc"],
    )

    stored_status = await r.hget(f"team:test-team:task:{task_id}", "status")
    stored_deps = await r.hget(f"team:test-team:task:{task_id}", "depends_on")
    score = await r.zscore("team:test-team:tasks:pending", task_id)

    assert stored_status == b"pending"
    assert json.loads(stored_deps) == ["a3f2bc"]
    assert score is not None, "task must appear in tasks:pending sorted set"
