import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readChat } from './chatLoad.mjs';

// A fake /v1 that answers the chat's reads and records each call's interaction mark.
function fakeAPI({ state = 'idle', sinceEvents = [] } = {}) {
  const calls = [];
  const api = async (method, path, body, opts) => {
    calls.push({ method, path, interaction: !!(opts && opts.interaction) });
    if (path.includes('/events?wait=0&after=')) return { events: sinceEvents, log: 'L' };
    if (path.includes('/events?wait=0')) return { events: [{ seq: 1, kind: 'text' }], log: 'L' };
    return { id: 's1', state };
  };
  return { api, calls };
}

// The server's rule: a GET on /events without wait=0 is a stream and never counts as use.
const isStream = (path) => /\/events(\?|$)/.test(path) && !/[?&]wait=0(&|$)/.test(path);

test('opening a chat marks the first detail read, a route the server counts', async () => {
  const { api, calls } = fakeAPI();
  const got = await readChat(api, 's1', { opened: true });
  assert.equal(got.detail.id, 's1');
  const marked = calls.filter((c) => c.interaction);
  assert.deepEqual(marked.map((c) => c.path), ['/v1/sessions/s1?turns=30']);
  assert.ok(marked.every((c) => c.method === 'GET' && !isStream(c.path)));
});

test('a reload (not opened by the person) marks nothing', async () => {
  const { api, calls } = fakeAPI();
  await readChat(api, 's1', { opened: false });
  assert.ok(calls.length >= 2);
  assert.ok(calls.every((c) => !c.interaction));
});

test('a turn that started meanwhile reads again, marking only the first detail read', async () => {
  const { api, calls } = fakeAPI({ sinceEvents: [{ seq: 2, kind: 'turn_started' }] });
  await readChat(api, 's1', { opened: true });
  const details = calls.filter((c) => c.path === '/v1/sessions/s1?turns=30');
  assert.equal(details.length, 3); // at most three attempts
  assert.deepEqual(details.map((c) => c.interaction), [true, false, false]);
});

test('a session open in a terminal stops after the detail read', async () => {
  const { api, calls } = fakeAPI({ state: 'open_in_terminal' });
  await readChat(api, 's1', { opened: true });
  assert.equal(calls.length, 2);
});

test('a navigation away mid-load returns null and reads no further', async () => {
  const { api, calls } = fakeAPI();
  assert.equal(await readChat(api, 's1', { opened: true, stale: () => true }), null);
  assert.equal(calls.length, 1);
});
