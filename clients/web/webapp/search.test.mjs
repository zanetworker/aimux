import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { apiDate } from './apiDate.mjs';
import { deriveActivity } from './now.mjs';
import { compareCodePoints, conversationSearch, isOpen, isWorking, matches, newestFirst, projectName, queryWords, searchSessions } from './search.mjs';

const dir = new URL('../../../api/testdata/clients/search/', import.meta.url);
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort();

test('the search fixture folder is not empty', () => {
  assert.ok(files.length >= 5, `only ${files.length} fixtures found`);
});

for (const file of files) {
  const fx = JSON.parse(readFileSync(new URL(file, dir), 'utf8'));
  test(`search/${file}: ${fx.name}`, () => {
    const activity = {};
    for (const [id, events] of Object.entries(fx.events ?? {})) activity[id] = deriveActivity(events);
    const sessions = fx.sessions.map((s) => ({ ...s, lastActive: apiDate(s.lastActive) }));
    const got = searchSessions(sessions, fx.query, activity, apiDate(fx.now));
    assert.deepEqual({ open: got.open.map((s) => s.id), other: got.other.map((s) => s.id) }, fx.expect);
  });
}

const s = (id, extra = {}) => ({ id, title: id, project: '/src/' + id, state: 'idle', lastActive: 0, ...extra });

test('queryWords: lower-cased, split on any whitespace; blank is no words', () => {
  assert.deepEqual(queryWords('  Login\tPAGE x '), ['login', 'page', 'x']);
  assert.deepEqual(queryWords('   '), []);
  assert.deepEqual(queryWords(undefined), []);
});

test('matches: every word in any field; missing fields are empty text', () => {
  const session = s('a', { title: 'Fix login', mac: { window: { name: 'Reviews' }, tab: { name: 'web' } } });
  assert.equal(matches(session, ['login', 'reviews']), true);
  assert.equal(matches(session, ['login', 'nowhere']), false);
  assert.equal(matches({ id: 'b' }, ['x']), false);
  assert.equal(matches({ id: 'b' }, []), true);
});

test('open and working: mac is open whatever the state; lastActive never makes either', () => {
  assert.equal(isOpen(s('a', { mac: { pane: 1 } }), { kind: 'none' }), true);
  assert.equal(isOpen(s('a', { lastActive: Date.now() }), { kind: 'none' }), false);
  assert.equal(isOpen(s('a'), { kind: 'working' }), true);
  assert.equal(isWorking(s('a', { state: 'open_in_terminal', working: true }), { kind: 'none' }), true);
  assert.equal(isWorking(s('a', { state: 'open_in_terminal' }), undefined), false);
});

test('projectName trims trailing slashes; compareCodePoints is not UTF-16 order', () => {
  assert.equal(projectName('/src/aimux//'), 'aimux');
  assert.equal(projectName('/'), '/');
  // U+FF5E sorts before U+1F600 by code point, after it by UTF-16 code unit.
  assert.ok(compareCodePoints('～', '\u{1f600}') < 0);
  assert.equal(compareCodePoints('a', 'a'), 0);
});

// A fake clock for the 400 ms wait before the server's conversation search.
function harness({ health = { capabilities: ['search'] }, hits = async () => ({ results: [] }) } = {}) {
  const timers = [];
  const changes = [];
  const calls = [];
  const search = conversationSearch({
    api: async (method, path) => {
      calls.push(path);
      if (path === '/v1/health') return typeof health === 'function' ? health() : health;
      return hits(path);
    },
    onChange: () => changes.push({ ...search.state }),
    setTimer: (fn, ms) => { timers.push({ fn, ms, live: true }); return timers.length; },
    clearTimer: (n) => { if (n) timers[n - 1].live = false; },
  });
  return { search, timers, changes, calls };
}

test('conversation search: waits 400 ms after the last keystroke, then asks the server once', async () => {
  const { search, timers, calls } = harness({ hits: async () => ({ results: [{ id: 'h1', lastActive: '2026-10-31T10:00:00Z' }] }) });
  search.typed('log');
  search.typed('login');
  assert.deepEqual(timers.filter((t) => t.live).map((t) => t.ms), [400]);
  assert.equal(search.state.searching, true);
  await timers.filter((t) => t.live)[0].fn();
  assert.deepEqual(calls, ['/v1/health', '/v1/search?q=login&limit=30']);
  assert.deepEqual(search.state.hits.map((h) => h.id), ['h1']);
  assert.equal(search.state.hits[0].lastActive, apiDate('2026-10-31T10:00:00Z'));
  assert.equal(search.state.searching, false);
});

test('conversation search: a blank query clears and asks nothing; an older answer never lands', async () => {
  let answer;
  const { search, timers, calls } = harness({ hits: () => new Promise((r) => { answer = r; }) });
  search.typed('old');
  const running = timers[0].fn();
  await new Promise((r) => setTimeout(r, 0));
  search.typed('   ');
  answer({ results: [{ id: 'stale', lastActive: '2026-10-31T10:00:00Z' }] });
  await running;
  assert.deepEqual(search.state.hits, []);
  assert.equal(search.state.searching, false);
  assert.equal(calls.filter((c) => c.startsWith('/v1/search')).length, 1);
});

test('conversation search: no search capability means unsupported; a failure is kept as the error', async () => {
  const off = harness({ health: { capabilities: [] } });
  off.search.typed('x');
  await off.timers[0].fn();
  assert.equal(off.search.state.supported, false);
  assert.equal(off.calls.some((c) => c.startsWith('/v1/search')), false);

  const failing = harness({ hits: async () => { throw new Error('offline'); } });
  failing.search.typed('x');
  await failing.timers[0].fn();
  assert.equal(failing.search.state.error.message, 'offline');
  assert.equal(failing.search.state.searching, false);
});

test('now is required: a missing or non-finite clock is a clear error, not a silent mis-order', () => {
  const list = [s('a', { title: 'x', lastActive: 1 })];
  for (const bad of [undefined, NaN, Infinity, '2026-10-31']) {
    assert.throws(() => searchSessions(list, 'x', {}, bad), /now must be a finite time in milliseconds/);
    assert.throws(() => newestFirst(bad), /now must be a finite time in milliseconds/);
  }
  assert.deepEqual(searchSessions(list, 'x', {}, 5).other.map((x) => x.id), ['a']);
});
