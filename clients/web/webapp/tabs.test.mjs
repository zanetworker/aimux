import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { folderKey, isOpenChoice, poller, readAllMode, saveAllMode, saveChoice, tabKey } from './tabs.mjs';

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

test('the tab bar has Now and All only: the Search tab is gone', () => {
  const html = readFileSync(new URL('./index.html', import.meta.url), 'utf8');
  const tabs = [...html.matchAll(/data-tab="([^"]+)"/g)].map((m) => m[1]);
  assert.deepEqual(tabs, ['now', 'all']);
  assert.equal(/id="search"/.test(html), false, 'no search screen');
});

function memoryStorage() {
  const data = {};
  return { getItem: (k) => (k in data ? data[k] : null), setItem: (k, v) => { data[k] = String(v); }, data };
}
const broken = { getItem: () => { throw new Error('denied'); }, setItem: () => { throw new Error('denied'); } };

test('All starts on Recent and remembers Folders; anything else stored reads as Recent', () => {
  const storage = memoryStorage();
  assert.equal(readAllMode(storage), 'recent');
  saveAllMode(storage, 'folders');
  assert.equal(storage.data.allMode, 'folders');
  assert.equal(readAllMode(storage), 'folders', 'a new read (a reload) finds Folders');
  saveAllMode(storage, 'recent');
  assert.equal(readAllMode(storage), 'recent');
  storage.data.allMode = 'mostActive';
  assert.equal(readAllMode(storage), 'recent');
});

test('All mode: storage that throws is Recent and a save does not crash', () => {
  assert.equal(readAllMode(broken), 'recent');
  assert.doesNotThrow(() => saveAllMode(broken, 'folders'));
});

test('folds: an explicit true or false wins over the default; keys are window|tab index|tab and repo|worktree', () => {
  const storage = memoryStorage();
  const key = tabKey('Window 1', 2, 'aimux');
  assert.equal(key, 'Window 1|2|aimux');
  assert.notEqual(tabKey('Window 1', 3, 'aimux'), key, 'two tabs with one name stay apart');
  assert.equal(isOpenChoice(storage, 'openTabs', key, true), true, 'no choice: the default');
  saveChoice(storage, 'openTabs', key, false);
  assert.equal(isOpenChoice(storage, 'openTabs', key, true), false, 'folded by hand over an open default');
  saveChoice(storage, 'openTabs', tabKey('Window 1', 3, 'blog'), true);
  assert.deepEqual(JSON.parse(storage.data.openTabs), { 'Window 1|2|aimux': false, 'Window 1|3|blog': true });
  assert.equal(folderKey('/src/aimux'), '/src/aimux');
  assert.equal(folderKey('/src/aimux', 'clients'), '/src/aimux|clients');
});

test('folds: unreadable or malformed storage falls back to the defaults; non-boolean values are ignored', () => {
  assert.equal(isOpenChoice(broken, 'openFolders', 'x', true), true);
  assert.doesNotThrow(() => saveChoice(broken, 'openFolders', 'x', false));
  const storage = memoryStorage();
  storage.data.openFolders = '{"a": "yes", "b": false}';
  assert.equal(isOpenChoice(storage, 'openFolders', 'a', true), true);
  assert.equal(isOpenChoice(storage, 'openFolders', 'b', true), false);
  storage.data.openFolders = 'not json';
  assert.equal(isOpenChoice(storage, 'openFolders', 'b', true), true);
  storage.data.openFolders = '[true]';
  assert.equal(isOpenChoice(storage, 'openFolders', '0', false), false);
});
