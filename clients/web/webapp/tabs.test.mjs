import { test } from 'node:test';
import assert from 'node:assert/strict';
import { localMatches, poller } from './tabs.mjs';

// A poller on a fake clock: `timers` holds what was scheduled.
function harness({ load = async () => {}, render = () => {}, visible = true, active = true } = {}) {
  const state = { visible, active, renders: 0, timers: [], errors: [] };
  const p = poller({
    load,
    render: () => { state.renders += 1; render(); },
    isActive: () => state.active,
    isVisible: () => state.visible,
    every: 15000,
    setTimer: (fn, ms) => { state.timers.push({ fn, ms }); return state.timers.length; },
    clearTimer: () => {},
    onError: (e) => state.errors.push(e),
  });
  return { p, state };
}

test('a poll loads, renders and schedules the next one in 15 s', async () => {
  const { p, state } = harness();
  await p.tick();
  assert.equal(state.renders, 1);
  assert.deepEqual(state.timers.map((t) => t.ms), [15000]);
});

test('a failed load still renders and schedules the next poll', async () => {
  const { p, state } = harness({ load: async () => { throw new Error('offline'); } });
  await p.tick();
  assert.equal(state.errors.length, 1);
  assert.equal(state.renders, 1);
  assert.equal(state.timers.length, 1);
});

test('a render that throws still schedules the next poll, and the next tick runs', async () => {
  let loads = 0;
  const { p, state } = harness({ load: async () => { loads += 1; }, render: () => { throw new Error('bad row'); } });
  await p.tick();
  assert.equal(state.errors.length, 1);
  assert.equal(state.timers.length, 1);
  await state.timers[0].fn();
  assert.equal(loads, 2, 'not stuck as ticking');
});

test('a hidden page or a hidden tab does not poll or reschedule', async () => {
  let loads = 0;
  const hidden = harness({ load: async () => { loads += 1; }, visible: false });
  await hidden.p.tick();
  const inactive = harness({ load: async () => { loads += 1; }, active: false });
  await inactive.p.tick();
  assert.equal(loads, 0);
  assert.equal(hidden.state.timers.length + inactive.state.timers.length, 0);
});

test('a tick while one is running is ignored', async () => {
  let loads = 0;
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const { p } = harness({ load: () => { loads += 1; return gate; } });
  const first = p.tick();
  await p.tick();
  release();
  await first;
  assert.equal(loads, 1);
});

test('every may be a function: each poll asks it for the next pause', async () => {
  const pauses = [2000, 15000];
  const state = { timers: [] };
  const p = poller({
    load: async () => {}, render: () => {}, isActive: () => true, isVisible: () => true,
    every: () => pauses.shift(),
    setTimer: (fn, ms) => { state.timers.push(ms); return state.timers.length; },
    clearTimer: () => {},
  });
  await p.tick();
  await p.tick();
  assert.deepEqual(state.timers, [2000, 15000]);
});

test('schedule waits one pause before the next poll; never two timers, never while one runs', async () => {
  const state = { timers: [], cleared: 0 };
  let loads = 0;
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const p = poller({
    load: () => { loads += 1; return gate; }, render: () => {}, isActive: () => true, isVisible: () => true,
    every: () => 2000,
    setTimer: (fn, ms) => { state.timers.push({ fn, ms, live: true }); return state.timers.length; },
    clearTimer: (n) => { if (n) state.timers[n - 1].live = false; },
  });
  p.schedule();
  assert.equal(loads, 0, 'schedule does not poll at once');
  p.schedule();
  assert.equal(state.timers.filter((t) => t.live).length, 1, 'a second schedule replaces the first');
  const running = state.timers[1].fn();
  p.schedule();
  assert.equal(state.timers.filter((t) => t.live).length, 0, 'no timer while a poll runs');
  release();
  await running;
  assert.equal(loads, 1);
  assert.deepEqual(state.timers.filter((t) => t.live).map((t) => t.ms), [2000], 'the running poll sets the one next timer');
});

const s = (id, title, extra = {}) => ({ id, title, project: '/src/' + id, lastPrompt: '', ...extra });
const ids = (list) => list.map((x) => x.id);

test('search: titles and descriptions match first, then folders and last prompts, list order kept inside', () => {
  const sessions = [
    s('p1', 'Tidy up', { lastPrompt: 'fix the login page' }),
    s('d1', 'Session 1234abcd', { description: 'Finds why the login page loops back.' }),
    s('login', 'Something else'),
    s('t1', 'Fix the LOGIN redirect'),
  ];
  assert.deepEqual(ids(localMatches(sessions, 'login')), ['d1', 't1', 'p1', 'login']);
});

test('search: every word must match, across title and description', () => {
  const sessions = [s('a', 'Login redirect', { description: 'loops on Safari' }), s('b', 'Login redirect')];
  assert.deepEqual(ids(localMatches(sessions, '  login   safari ')), ['a']);
});

test('search: an empty query matches nothing; a missing description or prompt is no text', () => {
  assert.deepEqual(localMatches([s('a', 'x')], '   '), []);
  assert.deepEqual(ids(localMatches([{ id: 'a', title: 'x', project: '/p' }], 'x')), ['a']);
});
