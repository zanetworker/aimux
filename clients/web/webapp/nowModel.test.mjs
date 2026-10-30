import { test } from 'node:test';
import assert from 'node:assert/strict';
import { NowModel } from './nowModel.mjs';

const NOW = Date.parse('2026-10-26T12:00:00Z');
const iso = (agoSeconds) => new Date(NOW - agoSeconds * 1000).toISOString();
const session = (id, state, agoSeconds, extra = {}) =>
  ({ id, provider: 'claude', title: id, project: '/p/' + id, state, lastActive: iso(agoSeconds), ...extra });
const httpError = (status, code = '') => Object.assign(new Error('HTTP ' + status), { status, code });

const approvalEvents = (approvalId) => [
  { seq: 1, turn: 't', kind: 'turn_started', time: iso(60), data: { text: 'go' } },
  { seq: 2, turn: 't', kind: 'approval', time: iso(50), data: { approvalId, title: 'Run it?', options: [{ optionId: 'ok', name: 'Allow', kind: 'allow_once' }] } },
];
const workingEvents = [{ seq: 1, turn: 't', kind: 'turn_started', time: iso(30), data: { text: 'go' } }];

// A fake /v1: `sessions` is the list, `events[id]` a page's events or a function that throws.
// `details[id]` is a session's detail, or a function that throws.
function fakeAPI({ sessions, events = {}, details = {}, onPost }) {
  const calls = [];
  const api = async (method, path, body) => {
    calls.push({ method, path, body });
    if (method === 'GET' && path.startsWith('/v1/sessions?')) {
      if (typeof sessions === 'function') return sessions();
      return { sessions };
    }
    const d = path.match(/^\/v1\/sessions\/([^/?]+)\?turns=/);
    if (method === 'GET' && d) {
      const detail = details[decodeURIComponent(d[1])];
      if (typeof detail === 'function') return detail();
      if (detail) return detail;
    }
    const m = path.match(/^\/v1\/sessions\/([^/]+)\/events\?wait=0$/);
    if (m) {
      const e = events[decodeURIComponent(m[1])];
      if (typeof e === 'function') return e();
      return { events: e ?? [], log: 'L' };
    }
    if (method === 'POST') return onPost ? onPost(path, body) : {};
    throw httpError(404);
  };
  return { api, calls };
}

function memoryStorage(initial = {}) {
  const data = { ...initial };
  return { getItem: (k) => (k in data ? data[k] : null), setItem: (k, v) => { data[k] = String(v); }, data };
}

const model = (api, storage = memoryStorage()) => new NowModel({ api, storage, now: () => NOW });
const snapshotted = (calls) => calls.filter((c) => c.path.includes('/events')).map((c) => decodeURIComponent(c.path.split('/')[3]));

test('snapshots busy and recently active sessions, never terminal ones, newest first, at most 10', async () => {
  const sessions = [
    session('term', 'open_in_terminal', 10),
    session('old-idle', 'idle', 901),
    session('edge-idle', 'idle', 900),
    session('old-busy', 'busy', 5000),
  ];
  for (let i = 0; i < 12; i++) sessions.push(session('r' + i, 'idle', 20 + i));
  const { api, calls } = fakeAPI({ sessions });
  await model(api).refresh();
  const got = snapshotted(calls);
  assert.equal(got.length, 10);
  assert.deepEqual(got, ['r0', 'r1', 'r2', 'r3', 'r4', 'r5', 'r6', 'r7', 'r8', 'r9']);
  assert.ok(!got.includes('term') && !got.includes('old-idle'));
});

test('a session at exactly 900 s is a candidate, and busy sessions are candidates at any age', async () => {
  const { api, calls } = fakeAPI({ sessions: [session('edge', 'idle', 900), session('old-busy', 'busy', 5000)] });
  await model(api).refresh();
  assert.deepEqual(snapshotted(calls), ['edge', 'old-busy']);
});

test('ranks what the snapshots say: an approval needs you', async () => {
  const { api } = fakeAPI({ sessions: [session('a', 'busy', 50)], events: { a: approvalEvents('ap') } });
  const m = model(api);
  await m.refresh();
  assert.deepEqual(m.view.needsYou.map((i) => i.session.id), ['a']);
  assert.equal(m.view.needsYou[0].activity.approval.approvalId, 'ap');
});

test('a refused snapshot is no activity; a network failure keeps the last known activity', async () => {
  let mode = 'ok';
  const events = { a: () => (mode === 'ok' ? { events: approvalEvents('ap'), log: 'L' } : mode === 'net' ? Promise.reject(new TypeError('Failed to fetch')) : Promise.reject(httpError(409))) };
  const { api } = fakeAPI({ sessions: [session('a', 'idle', 50)], events });
  const m = model(api);
  await m.refresh();
  assert.equal(m.view.needsYou.length, 1);
  mode = 'net';
  await m.refresh();
  assert.equal(m.view.needsYou.length, 1, 'network failure keeps the approval');
  mode = 'refused';
  await m.refresh();
  assert.equal(m.view.needsYou.length, 0, 'a 409 means no activity');
});

test('a failed session list keeps the last sessions and reports the error', async () => {
  let fail = false;
  const { api } = fakeAPI({ sessions: () => (fail ? Promise.reject(new TypeError('offline')) : { sessions: [session('a', 'busy', 10)] }) });
  const m = model(api);
  await m.refresh();
  fail = true;
  await m.refresh();
  assert.equal(m.sessions.length, 1);
  assert.ok(m.loadError);
  fail = false;
  await m.refresh();
  assert.equal(m.loadError, null);
});

test('refreshes never overlap: the second waits for the first', async () => {
  let release;
  let inFlight = 0, maxInFlight = 0, n = 0;
  const { api } = fakeAPI({
    sessions: async () => {
      inFlight++; maxInFlight = Math.max(maxInFlight, inFlight); n++;
      if (n === 1) await new Promise((r) => { release = r; });
      inFlight--;
      return { sessions: [session('s' + n, 'idle', 5000)] };
    },
  });
  const m = model(api);
  const first = m.refresh();
  const second = m.refresh();
  await new Promise((r) => setTimeout(r, 10));
  release();
  await Promise.all([first, second]);
  assert.equal(maxInFlight, 1);
  assert.equal(m.sessions[0].id, 's2', 'the newer result lands last');
});

test('answering hides the card; a second answer while one is in flight is ignored', async () => {
  let posts = 0;
  let release;
  const { api } = fakeAPI({
    sessions: [session('a', 'busy', 50)], events: { a: approvalEvents('ap') },
    onPost: async () => { posts++; await new Promise((r) => { release = r; }); return { status: 'ok' }; },
  });
  const m = model(api);
  await m.refresh();
  const approval = m.view.needsYou[0].activity.approval;
  const first = m.answer(approval, 'ok');
  assert.ok(m.answering.has('ap'));
  await m.answer(approval, 'ok');
  release();
  await first;
  assert.equal(posts, 1);
  assert.equal(m.answering.has('ap'), false);
  assert.equal(m.view.needsYou.length, 0);
});

test('an approval resolved elsewhere (404) hides the card; another failure shows an error and keeps it', async () => {
  let err = httpError(404, 'no_such_approval');
  const { api } = fakeAPI({ sessions: [session('a', 'busy', 50)], events: { a: approvalEvents('ap') }, onPost: () => Promise.reject(err) });
  const m = model(api);
  await m.refresh();
  const approval = m.view.needsYou[0].activity.approval;
  err = httpError(500);
  await m.answer(approval, 'ok');
  assert.equal(m.view.needsYou.length, 1);
  assert.ok(m.answerErrors.get('ap'));
  err = httpError(404, 'no_such_approval');
  await m.answer(approval, 'ok');
  assert.equal(m.view.needsYou.length, 0);
  assert.equal(m.answerErrors.has('ap'), false);
});

test('retry sends the last prompt with a fresh key per tap and records a failure', async () => {
  const keys = [];
  let fail = false;
  const failed = [
    { seq: 1, turn: 't', kind: 'turn_started', time: iso(60), data: { text: 'go' } },
    { seq: 2, turn: 't', kind: 'turn_error', time: iso(50), data: { error: 'boom' } },
  ];
  const { api } = fakeAPI({
    sessions: [session('f', 'idle', 50, { lastPrompt: 'do it again' })], events: { f: failed },
    details: { f: { id: 'f', turns: [{ number: 1, user: 'first' }, { number: 2, user: 'do it again' }] } },
    onPost: (path, body) => { keys.push(body); return fail ? Promise.reject(httpError(409, 'busy')) : { turn: 'x' }; },
  });
  const m = model(api);
  await m.refresh();
  const s = m.view.needsYou[0].session;
  await m.retry(s);
  await m.retry(s);
  assert.equal(keys.length, 2);
  assert.equal(keys[0].text, 'do it again');
  assert.ok(keys[0].key && keys[0].key !== keys[1].key);
  fail = true;
  await m.retry(s);
  assert.ok(m.retryErrors.get('f'));
});

test('retry sends the whole last message from the history, not the list preview', async () => {
  const long = 'x'.repeat(300);
  const sent = [];
  const { api } = fakeAPI({
    sessions: [session('f', 'idle', 50, { lastPrompt: long.slice(0, 160) + '…' })],
    details: { f: { id: 'f', turns: [
      { number: 1, user: 'The user sent a new message while you were working:\n' + long }, { number: 2, user: '' },
    ] } },
    onPost: (path, body) => { sent.push(body); return { turn: 'x' }; },
  });
  const m = model(api);
  await m.refresh();
  await m.retry(m.sessions[0]);
  assert.deepEqual(sent.map((b) => b.text), [long]);
});

test('retry sends nothing when the history cannot be read', async () => {
  const { api, calls } = fakeAPI({
    sessions: [session('f', 'idle', 50, { lastPrompt: 'go' })],
    details: { f: () => { throw httpError(500); } },
  });
  const m = model(api);
  await m.refresh();
  await m.retry(m.sessions[0]);
  assert.equal(calls.filter((c) => c.method === 'POST').length, 0);
  assert.ok(m.retryErrors.get('f'));
  assert.equal(m.retrying.has('f'), false);
});

test('retry does nothing without a last prompt', async () => {
  const { api, calls } = fakeAPI({ sessions: [session('f', 'idle', 50)] });
  const m = model(api);
  await m.refresh();
  await m.retry(m.sessions[0]);
  assert.equal(calls.filter((c) => c.method === 'POST').length, 0);
});

test('seen times persist in storage and hide Just finished', async () => {
  const done = [
    { seq: 1, turn: 't', kind: 'turn_started', time: iso(120), data: { text: 'go' } },
    { seq: 2, turn: 't', kind: 'turn_done', time: iso(100), data: { stopReason: 'end_turn' } },
  ];
  const storage = memoryStorage();
  const { api } = fakeAPI({ sessions: [session('d', 'idle', 100)], events: { d: done } });
  const m = model(api, storage);
  await m.refresh();
  assert.equal(m.view.finished.length, 1);
  m.markSeen('d');
  assert.equal(m.view.finished.length, 0);
  const again = model(api, storage);
  await again.refresh();
  assert.equal(again.view.finished.length, 0, 'a new model reads the stored seen time');
});

test('unreadable storage is no seen times, not a crash', async () => {
  const broken = { getItem: () => { throw new Error('denied'); }, setItem: () => { throw new Error('denied'); } };
  const { api } = fakeAPI({ sessions: [session('a', 'busy', 10)], events: { a: workingEvents } });
  const m = model(api, broken);
  await m.refresh();
  m.markSeen('a');
  assert.equal(m.view.summary.working, 1);
});

test('a 404 with another code is a failure, not an approval resolved elsewhere', async () => {
  const { api } = fakeAPI({ sessions: [session('a', 'busy', 50)], events: { a: approvalEvents('ap') }, onPost: () => Promise.reject(httpError(404, 'no_such_session')) });
  const m = model(api);
  await m.refresh();
  await m.answer(m.view.needsYou[0].activity.approval, 'ok');
  assert.equal(m.view.needsYou.length, 1);
  assert.equal(m.answerErrors.get('ap').code, 'no_such_session');
});

test('retry holds its guard through the refresh after the send, like the app', async () => {
  const failed = [
    { seq: 1, turn: 't', kind: 'turn_started', time: iso(60), data: { text: 'go' } },
    { seq: 2, turn: 't', kind: 'turn_error', time: iso(50), data: { error: 'boom' } },
  ];
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let lists = 0;
  let posts = 0;
  const { api } = fakeAPI({
    sessions: () => { lists += 1; return lists === 2 ? gate.then(() => ({ sessions: [s0] })) : { sessions: [s0] }; },
    events: { f: failed },
    details: { f: { id: 'f', turns: [{ number: 1, user: 'again' }] } },
    onPost: () => { posts += 1; return { turn: 'x' }; },
  });
  const s0 = session('f', 'idle', 50, { lastPrompt: 'again' });
  const m = model(api);
  await m.refresh();
  const first = m.retry(m.view.needsYou[0].session);
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(m.retrying.has('f'), true, 'still retrying while the follow-up refresh runs');
  await m.retry(m.view.needsYou[0].session);
  assert.equal(posts, 1, 'a second tap during the refresh sends nothing');
  release();
  await first;
  assert.equal(m.retrying.has('f'), false);
});

// Finding sessions: Last on your Mac, pins, owner edits and Update summary.

test('the list carries Last on your Mac into the view', async () => {
  const { api } = fakeAPI({ sessions: () => ({ sessions: [session('t', 'open_in_terminal', 3600)], lastOnMac: { id: 't', at: iso(240) } }) });
  const m = model(api);
  await m.refresh();
  assert.equal(m.view.lastOnMac.session.id, 't');
  assert.deepEqual(m.view.onMac, []);
});

test('a list without lastOnMac clears the one before', async () => {
  let withIt = true;
  const { api } = fakeAPI({ sessions: () => ({ sessions: [session('t', 'open_in_terminal', 3600)], ...(withIt ? { lastOnMac: { id: 't', at: iso(240) } } : {}) }) });
  const m = model(api);
  await m.refresh();
  withIt = false;
  await m.refresh();
  assert.equal(m.view.lastOnMac, null);
});

// A fake /v1 for the meta routes: `answers[method + ' ' + path]` is a result or a function that throws.
function metaAPI(sessions, answers) {
  const calls = [];
  const api = async (method, path, body) => {
    calls.push({ method, path, body });
    if (method === 'GET' && path.startsWith('/v1/sessions?')) return { sessions };
    if (method === 'GET' && path === '/v1/health') return answers.health ?? { capabilities: [] };
    const a = answers[method + ' ' + path];
    if (typeof a === 'function') return a();
    if (a) return a;
    if (path.includes('/events')) return { events: [] };
    throw httpError(404);
  };
  return { api, calls };
}

test('pin: PATCH meta with only pinned, then the session sits in Pinned', async () => {
  const { api, calls } = metaAPI([session('a', 'idle', 3600)], { 'PATCH /v1/sessions/a/meta': { title: 'a', pinned: true } });
  const m = model(api);
  await m.refresh();
  await m.setPinned(m.sessions[0], true);
  assert.deepEqual(calls.filter((c) => c.method === 'PATCH').map((c) => [c.path, c.body]), [['/v1/sessions/a/meta', { pinned: true }]]);
  assert.deepEqual(m.view.pinned.map((i) => i.session.id), ['a']);
  assert.equal(m.pinErrors.size, 0);
});

test('pin: a refusal is kept for the row and nothing moves', async () => {
  const { api } = metaAPI([session('a', 'idle', 3600)], { 'PATCH /v1/sessions/a/meta': () => { throw httpError(404, 'not_supported'); } });
  const m = model(api);
  await m.refresh();
  await m.setPinned(m.sessions[0], true);
  assert.deepEqual(m.view.pinned, []);
  assert.equal(m.pinErrors.get('a'), 'Could not pin (HTTP 404).');
  assert.equal(m.pinning.size, 0, 'not left in flight');
});

test('editMeta: sends only the fields given and keeps what the server says clients show', async () => {
  const { api, calls } = metaAPI([session('a', 'idle', 3600, { description: 'old' })], {
    'PATCH /v1/sessions/a/meta': { title: 'Fix the login redirect', pinned: false },
  });
  const m = model(api);
  await m.refresh();
  const r = await m.editMeta('a', { description: '' });
  assert.deepEqual(calls.at(-1).body, { description: '' });
  assert.equal(r.title, 'Fix the login redirect');
  assert.equal(m.sessions[0].title, 'Fix the login redirect');
  assert.equal(m.sessions[0].description, undefined, 'the server sent none: the session has none');
});

test('editMeta: an error is thrown to the caller and the session is unchanged', async () => {
  const { api } = metaAPI([session('a', 'idle', 3600)], { 'PATCH /v1/sessions/a/meta': () => { throw httpError(400, 'bad_request'); } });
  const m = model(api);
  await m.refresh();
  await assert.rejects(m.editMeta('a', { title: 'x' }), (e) => e.status === 400);
  assert.equal(m.sessions[0].title, 'a');
});

test('describe: POST, then the new title and description are the session’s', async () => {
  const { api, calls } = metaAPI([session('a', 'idle', 3600)], {
    'POST /v1/sessions/a/describe': { title: 'Plan the trip', description: 'Books the cabin.', changed: ['title', 'description'], kept: [] },
  });
  const m = model(api);
  await m.refresh();
  const r = await m.describe('a');
  assert.deepEqual(r.changed, ['title', 'description']);
  assert.equal(calls.at(-1).body, undefined);
  assert.equal(m.sessions[0].title, 'Plan the trip');
  assert.equal(m.sessions[0].description, 'Books the cabin.');
});

test('health: read once with the first refresh; describe and naming from it; read again after a failure', async () => {
  let fail = true;
  let reads = 0;
  const { api } = metaAPI([], {});
  const wrapped = async (method, path, body) => {
    if (path === '/v1/health') reads += 1;
    if (path === '/v1/health' && fail) { fail = false; throw new TypeError('offline'); }
    if (path === '/v1/health') return { capabilities: ['sessions', 'describe'], naming: { today: 4, limit: 20, failing: false } };
    return api(method, path, body);
  };
  const m = model(wrapped);
  await m.refresh();
  assert.equal(m.canDescribe, false);
  await m.refresh();
  assert.equal(m.canDescribe, true);
  assert.deepEqual(m.health.naming, { today: 4, limit: 20, failing: false });
  await m.refresh();
  assert.equal(reads, 2, 'not read again once known');
});

test('describe: one at a time per session; a second call while it runs sends nothing', async () => {
  let release;
  const { api, calls } = metaAPI([session('a', 'idle', 3600)], {
    'POST /v1/sessions/a/describe': () => new Promise((r) => { release = r; }),
  });
  const m = model(api);
  await m.refresh();
  const first = m.describe('a');
  assert.ok(m.describing.has('a'));
  assert.equal(await m.describe('a'), null);
  release({ title: 'T', changed: ['title'], kept: [] });
  await first;
  assert.equal(m.describing.has('a'), false);
  assert.equal(calls.filter((c) => c.path.endsWith('/describe')).length, 1);
});

test('pin errors are the fixed line for the row', async () => {
  const { api } = metaAPI([session('a', 'idle', 3600)], { 'PATCH /v1/sessions/a/meta': () => { throw httpError(500, 'internal'); } });
  const m = model(api);
  await m.refresh();
  await m.setPinned(m.sessions[0], true);
  assert.equal(m.pinErrors.get('a'), 'Could not pin (HTTP 500).');
});
