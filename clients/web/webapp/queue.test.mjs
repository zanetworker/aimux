import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { createLogin } from './login.mjs';
import {
  adoptDetail, composerAction, createQueueClient, movePosition, queueActive, queueErrorText, queueItems, queueSection,
  sendRefusal, setQueue,
} from './queue.mjs';

const DIR = new URL('../../../api/testdata/clients/queue/', import.meta.url);
const fixtures = readdirSync(DIR).filter((f) => f.endsWith('.json')).map((f) => JSON.parse(readFileSync(new URL(f, DIR), 'utf8')));
const Q = '/v1/sessions/s%201/queue';
const err = (code, message = 'm', status = 409) => Object.assign(new Error(message), { code, status });

test('the queue fixtures are all there', () => {
  assert.equal(fixtures.length, 37);
});

for (const f of fixtures) {
  test('fixture ' + f.name, async () => {
    const refusedBusy = !!(f.send && f.send.error === 'busy');
    assert.deepEqual(composerAction(f.session, { refusedBusy }), f.expect.composer, 'composer');
    assert.deepEqual(queueSection(f.session.queue), f.expect.queue, 'queue section');
    assert.deepEqual(queueItems(f.session.queue), f.expect.items, 'items');
    if (f.send) assert.deepEqual(sendRefusal(err(f.send.error)), f.expect.offer, 'offer');
    if (f.delete) assert.equal(queueErrorText(err(f.delete.error)), f.expect.message, 'message');
    if (f.add) {
      const { client } = harness({ ['POST ' + Q]: [409, { error: 'refused', code: f.add.error }] });
      const r = await client.add('Some text');
      assert.deepEqual({ keepText: r.keepText, message: r.message }, { keepText: f.expect.keepText, message: f.expect.message }, 'refused add');
    }
  });
}

test('composerAction: the offer after busy ends when the session is free again (caller clears it); a free session sends', () => {
  assert.deepEqual(composerAction({ state: 'idle' }), { action: 'send', label: 'Send' });
  assert.deepEqual(composerAction({ state: 'idle' }, { refusedBusy: true }), { action: 'queue', label: 'Add to queue' });
  assert.deepEqual(composerAction(null), { action: 'send', label: 'Send' }, 'no detail yet: nothing to queue behind');
  const unknown = { state: 'open_in_terminal', terminal: { status: 'unknown', canType: true, working: false } };
  assert.deepEqual(composerAction(unknown), { action: 'queue', label: 'Add to queue' }, 'an unknown terminal is never typed into directly');
});

test('queueSection and queueItems: no queue, or only done items, shows nothing', () => {
  assert.deepEqual(queueSection(undefined), { visible: false });
  assert.deepEqual(queueItems(undefined), []);
  const onlyDone = { state: 'running', items: [{ id: 'q1', text: '', state: 'done', created: '2026-03-02T09:01:00Z' }] };
  assert.deepEqual(queueSection(onlyDone), { visible: false });
  assert.deepEqual(queueItems(onlyDone), []);
});

test('sendRefusal: only busy is an offer to queue; other refusals stay failures', () => {
  assert.equal(sendRefusal(err('in_use')), null);
  assert.equal(sendRefusal(Object.assign(new Error('Failed to fetch'), { code: '' })), null);
});

test('queueErrorText: fixed lines for the queue refusals; others say what failed', () => {
  assert.equal(queueErrorText(err('unknown_item', 'no such item', 404)), 'This message is no longer in the queue.');
  assert.equal(queueErrorText(err('internal', 'disk full', 500)), 'Could not change the queue: disk full');
  assert.equal(queueErrorText(Object.assign(new Error('Failed to fetch'), { code: '' })), 'Could not change the queue: Failed to fetch');
});

test('movePosition: counts only the messages not in flight, null at either end', () => {
  const q = {
    state: 'running',
    items: [
      { id: 'q1', text: 'a', state: 'answering' },
      { id: 'q2', text: 'b', state: 'waiting' },
      { id: 'q3', text: 'c', state: 'waiting' },
      { id: 'q4', text: 'd', state: 'waiting' },
    ],
  };
  assert.equal(movePosition(q, 'q3', -1), 0, 'q3 is second among waiting: up is position 0');
  assert.equal(movePosition(q, 'q3', 1), 2);
  assert.equal(movePosition(q, 'q2', -1), null, 'the first waiting one cannot go up (above the one in flight)');
  assert.equal(movePosition(q, 'q4', 1), null, 'the last cannot go down');
  assert.equal(movePosition(q, 'q1', 1), null, 'the one in flight never moves');
  assert.equal(movePosition(q, 'nope', 1), null);
});

// A fake fetch behind the real request path (login.mjs): answers per "METHOD path", records calls.
function harness(answers) {
  const calls = [];
  const fetch = async (path, opts) => {
    calls.push({ method: opts.method, path, body: opts.body ? JSON.parse(opts.body) : undefined, headers: opts.headers });
    const a = answers[opts.method + ' ' + path];
    const many = Array.isArray(a) && (Array.isArray(a[0]) || a[0] instanceof Error); // a list of answers, in turn
    const next = many ? a.shift() : a;
    if (next instanceof Error) throw next;
    const [status, body] = next || [500, { error: 'no answer', code: 'internal' }];
    return { ok: status < 400, status, json: async () => body };
  };
  const login = createLogin({ fetch, credentials: {}, storage: { getItem: () => null, setItem() {}, removeItem() {} }, hostname: 'x' });
  let n = 0;
  const client = createQueueClient(login.request, 's 1', { newKey: () => 'k' + ++n });
  return { calls, client };
}

const status = (items, extra = {}) => ({ state: 'running', items, ...extra });
const item = (id, text, state = 'waiting') => ({ id, text, state, created: '2026-03-02T09:01:00Z' });

test('add: POSTs text with a key, then reads the queue; never with the interaction header', async () => {
  const after = status([item('q1', 'Rename it')]);
  const { calls, client } = harness({ ['POST ' + Q]: [200, item('q1', 'Rename it')], ['GET ' + Q]: [200, after] });
  const r = await client.add('Rename it');
  assert.deepEqual(r, { ok: true, queue: after });
  assert.deepEqual(calls.map((c) => c.method + ' ' + c.path), ['POST ' + Q, 'GET ' + Q]);
  assert.deepEqual(calls[0].body, { text: 'Rename it', key: 'k1' });
  assert.ok(calls.every((c) => !c.headers['Aimux-Interaction']));
});

test('add: queue_full and bad_text keep the text and show the fixed line', async () => {
  const { client } = harness({ ['POST ' + Q]: [[409, { error: 'full', code: 'queue_full' }], [409, { error: 'bad', code: 'bad_text' }]] });
  assert.deepEqual(await client.add('x'), { ok: false, keepText: true, message: 'The queue is full (20 messages). Delete one to add another.' });
  assert.deepEqual(await client.add('/x'), { ok: false, keepText: true, message: "This message can't be queued (it starts with ! or /, holds control characters, or is too long)." });
});

test('add: a retry of the same text after a lost answer reuses its key; other text gets a new one', async () => {
  const { calls, client } = harness({
    ['POST ' + Q]: [new TypeError('Failed to fetch'), [200, item('q1', 'a')], [200, item('q2', 'b')]],
    ['GET ' + Q]: [200, status([])],
  });
  const first = await client.add('a');
  assert.equal(first.ok, false);
  assert.equal(first.keepText, true);
  assert.equal(first.message, 'Could not change the queue: Failed to fetch');
  await client.add('a');
  await client.add('b');
  const keys = calls.filter((c) => c.method === 'POST').map((c) => c.body.key);
  assert.deepEqual(keys, ['k1', 'k1', 'k2']);
});

test('add: a successful read failing after the add still reports the add', async () => {
  const { client } = harness({ ['POST ' + Q]: [200, item('q1', 'a')], ['GET ' + Q]: [500, { error: 'x', code: 'internal' }] });
  assert.deepEqual(await client.add('a'), { ok: true, queue: null });
});

test('edit, move, delete, resume: the right request, the queue from the answer', async () => {
  const after = status([item('q1', 'new')]);
  const { calls, client } = harness({
    ['PATCH ' + Q + '/q%2F1']: [[200, after], [200, after]],
    ['DELETE ' + Q + '/q%2F1']: [200, after],
    ['POST ' + Q + '/resume']: [200, after],
  });
  assert.deepEqual(await client.edit('q/1', 'new'), { ok: true, queue: after });
  assert.deepEqual(await client.move('q/1', 2), { ok: true, queue: after });
  assert.deepEqual(await client.remove('q/1'), { ok: true, queue: after });
  assert.deepEqual(await client.resume(), { ok: true, queue: after });
  assert.deepEqual(calls.map((c) => [c.method, c.path, c.body]), [
    ['PATCH', Q + '/q%2F1', { text: 'new' }],
    ['PATCH', Q + '/q%2F1', { position: 2 }],
    ['DELETE', Q + '/q%2F1', undefined],
    ['POST', Q + '/resume', undefined],
  ]);
});

test('edit and delete refused while sending, or gone: the fixed line, no queue', async () => {
  const { client } = harness({
    ['PATCH ' + Q + '/q1']: [409, { error: 'sending', code: 'item_sending' }],
    ['DELETE ' + Q + '/q1']: [404, { error: 'no such item', code: 'unknown_item' }],
  });
  assert.deepEqual(await client.edit('q1', 'x'), { ok: false, message: "This message is being sent and can't be changed." });
  assert.deepEqual(await client.remove('q1'), { ok: false, message: 'This message is no longer in the queue.' });
});

test('edit: an empty text is refused here, with no request', async () => {
  const { calls, client } = harness({});
  assert.deepEqual(await client.edit('q1', '  '), { ok: false, message: 'A queued message needs some text.' });
  assert.deepEqual(await client.add(''), { ok: false, keepText: true, message: 'A queued message needs some text.' });
  assert.equal(calls.length, 0);
});

test('queueActive: in flight in any state, or running with messages waiting (the server refuses a direct send then)', () => {
  const w = { id: 'q1', text: 'a', state: 'waiting' };
  assert.equal(queueActive(null), false, 'no queue');
  assert.equal(queueActive({ state: 'running', items: [] }), false, 'empty');
  assert.equal(queueActive({ state: 'paused', items: [w] }), false, 'paused, waiting only');
  assert.equal(queueActive({ state: 'running', items: [{ ...w, state: 'done' }] }), false, 'only a hidden done item');
  assert.equal(queueActive({ state: 'running', items: [w] }), true, 'running with one waiting');
  assert.equal(queueActive({ state: 'paused', items: [{ ...w, state: 'answering' }] }), true, 'paused with one in flight');
});

test('a detail read before a queue change keeps the newer queue; otherwise it says the queue now', () => {
  const chat = { queue: { state: 'running', items: [{ id: 'q1' }] } };
  const before = chat.queueRev || 0;
  const paused = { state: 'paused', reason: 'Check the session.', items: [{ id: 'q1' }] };
  setQueue(chat, paused); // a queue event while the detail was read
  adoptDetail(chat, { id: 's1', queue: { state: 'running', items: [{ id: 'q1' }] } }, before);
  assert.equal(chat.queue, paused);
  assert.equal(chat.detail.id, 's1');

  const now = chat.queueRev;
  adoptDetail(chat, { id: 's1' }, now); // nothing changed meanwhile: an empty queue is absent
  assert.equal(chat.queue, null);
  setQueue(chat, null);
  assert.equal(chat.queue, null);
  assert.ok(chat.queueRev > now);
});
