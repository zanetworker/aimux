import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { apiDate } from './apiDate.mjs';
import { buildTimeline, samePrompt } from './timeline.mjs';

const dir = new URL('../../../api/testdata/clients/chat/', import.meta.url);
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort();

test('the chat fixture folder is not empty', () => {
  assert.ok(files.length >= 15, `only ${files.length} fixtures found`);
});

for (const file of files) {
  const fx = JSON.parse(readFileSync(new URL(file, dir), 'utf8'));
  test(`chat/${file}: ${fx.name}`, () => {
    const got = buildTimeline({ turns: fx.turns, events: fx.events, now: apiDate(fx.now) });
    assert.deepEqual(got, fx.expect);
  });
}

test('samePrompt folds whitespace, accepts a cut prompt, and never matches an empty one', () => {
  assert.equal(samePrompt('Fix  the\nbug', ' Fix the bug '), true);
  assert.equal(samePrompt('Fix the…', 'Fix the bug'), true);
  assert.equal(samePrompt('Fix the bug', 'Fix the'), false);
  assert.equal(samePrompt('', 'anything'), false);
  assert.equal(samePrompt('…', 'anything'), false);
});

const ev = (seq, turn, kind, time, data) => ({ seq, turn, kind, time, data });

test('buildTimeline: no turns and no events is empty and idle', () => {
  assert.deepEqual(buildTimeline({ turns: [], events: [], now: 0 }), { items: [], working: false, pendingApproval: null });
});

test('buildTimeline: events are ordered by seq, not by array position', () => {
  const got = buildTimeline({
    turns: [],
    events: [
      ev(2, 't', 'update', '2026-10-26T08:00:01Z', { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'b' } }),
      ev(1, 't', 'turn_started', '2026-10-26T08:00:00Z', { text: 'a' }),
    ],
    now: 0,
  });
  assert.deepEqual(got.items, [{ kind: 'user', text: 'a' }, { kind: 'reply', text: 'b' }]);
});

test('buildTimeline: a history turn just over 2 s before the running start is kept', () => {
  const got = buildTimeline({
    turns: [{ number: 1, time: '2026-10-26T07:59:57.999Z', user: 'Go', reply: 'Old', tools: [] }],
    events: [ev(1, 't', 'turn_started', '2026-10-26T08:00:00Z', { text: 'Go' })],
    now: 0,
  });
  assert.deepEqual(got.items, [{ kind: 'user', text: 'Go' }, { kind: 'reply', text: 'Old' }, { kind: 'user', text: 'Go' }]);
});

test('buildTimeline: an error with no start and no history stands alone', () => {
  const got = buildTimeline({ turns: [], events: [ev(1, 't', 'turn_error', '2026-10-26T08:00:00Z', { error: 'boom' })], now: 0 });
  assert.deepEqual(got, { items: [{ kind: 'error', text: 'boom' }], working: false, pendingApproval: null });
});

test('buildTimeline: turn_done other than cancelled adds nothing', () => {
  const got = buildTimeline({
    turns: [],
    events: [
      ev(1, 't', 'turn_started', '2026-10-26T08:00:00Z', { text: 'Go' }),
      ev(2, 't', 'turn_done', '2026-10-26T08:00:01Z', { stopReason: 'max_tokens' }),
    ],
    now: 0,
  });
  assert.deepEqual(got.items, [{ kind: 'user', text: 'Go' }]);
});

test('buildTimeline: an unparseable time gives null seconds, not NaN', () => {
  const got = buildTimeline({
    turns: [],
    events: [
      ev(1, 't', 'turn_started', '2026-10-26T08:00:00Z', { text: 'Go' }),
      ev(2, 't', 'update', 'not a time', { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Hm.' } }),
      ev(3, 't', 'update', 'not a time', { sessionUpdate: 'tool_call', toolCallId: 'c', title: 'Read a', kind: 'read' }),
    ],
    now: 0,
  });
  assert.deepEqual(got.items, [
    { kind: 'user', text: 'Go' },
    { kind: 'thought', text: 'Hm.', seconds: null },
    { kind: 'steps', count: 1, seconds: null, filesChanged: [], steps: [{ kind: 'read', target: 'Read a', status: 'running', detail: { input: '', error: '', old: '', new: '' } }] },
  ]);
});

test('buildTimeline: a turn_started without data or text does not crash and adds no user item', () => {
  const t = '2026-10-26T08:00:00Z';
  const running = buildTimeline({
    turns: [{ number: 1, time: t, user: 'Go', reply: 'Ok', tools: [] }],
    events: [{ seq: 1, turn: 'r', kind: 'turn_started', time: t }],
    now: 0,
  });
  assert.deepEqual(running, { items: [{ kind: 'user', text: 'Go' }, { kind: 'reply', text: 'Ok' }], working: true, pendingApproval: null });
  const ended = buildTimeline({
    turns: [{ number: 1, time: t, user: 'Go', reply: 'Ok', tools: [] }],
    events: [ev(1, 'e', 'turn_started', '2026-10-26T08:01:00Z', {}), { seq: 2, turn: 'e', kind: 'turn_error', time: '2026-10-26T08:01:01Z' }],
    now: 0,
  });
  assert.deepEqual(ended.items, [{ kind: 'user', text: 'Go' }, { kind: 'reply', text: 'Ok' }, { kind: 'error', text: '' }]);
});

test('buildTimeline: option fields and outcome are always present; a resolve without outcome leaves it pending', () => {
  const got = buildTimeline({
    turns: [],
    events: [
      ev(1, 't', 'turn_started', '2026-10-26T08:00:00Z', { text: 'Go' }),
      ev(2, 't', 'approval', '2026-10-26T08:00:01Z', { approvalId: 'a', title: 'X', options: [{ optionId: 'allow' }], toolCall: { toolCallId: 'c' } }),
      ev(3, 't', 'approval_resolved', '2026-10-26T08:00:02Z', { approvalId: 'a' }),
    ],
    now: 0,
  });
  assert.deepEqual(got.items[1], {
    kind: 'approval', approvalId: 'a', title: 'X', command: null,
    options: [{ optionId: 'allow', name: '', kind: '' }], outcome: null,
  });
  assert.equal(got.pendingApproval, 'a');
});

// A sent message waits as pending until the timeline shows it (the iPhone ChatModel's rule).
test('pendingShown: by turn id in the events, or a history turn with its prompt from 2 s before the send on', async () => {
  const { pendingShown } = await import('./timeline.mjs');
  const sentAt = Date.parse('2026-10-26T10:00:00Z');
  const p = { text: 'run the  tests', turn: null, sentAt, failure: null };
  const turn = (time, user) => ({ time, user, reply: '', tools: [] });
  assert.equal(pendingShown(p, [], []), false, 'nothing yet');
  assert.equal(pendingShown({ ...p, turn: 't9' }, [{ seq: 1, turn: 't9', kind: 'turn_started' }], []), true, 'its turn is in the events');
  assert.equal(pendingShown({ ...p, turn: 't9' }, [{ seq: 1, turn: 't8', kind: 'turn_started' }], []), false, 'another turn');
  assert.equal(pendingShown(p, [], [turn('2026-10-26T09:59:58Z', 'run the tests')]), true, 'history 2 s before the send (inclusive)');
  assert.equal(pendingShown(p, [], [turn('2026-10-26T09:59:57.9Z', 'run the tests')]), false, 'history older than the window');
  assert.equal(pendingShown(p, [], [turn('2026-10-26T10:00:05Z', 'something else')]), false, 'another prompt');
  assert.equal(pendingShown({ ...p, turn: 't9', failure: new Error('x') }, [{ seq: 1, turn: 't9' }], []), false, 'a failed send stays');
});

// A message typed into a terminal on the Mac is saved with Claude Code's wrappers; the shared
// cases (api/testdata/clients/terminal/saved-prompt-match.json) pin how it is recognised.
const terminalFixture = JSON.parse(readFileSync(new URL('../../../api/testdata/clients/terminal/saved-prompt-match.json', import.meta.url), 'utf8'));

test('the terminal saved-prompt fixture has its cases', () => {
  assert.ok(terminalFixture.cases.length >= 7, `only ${terminalFixture.cases.length} cases`);
});

for (const c of terminalFixture.cases) {
  test(`terminal/saved-prompt-match: ${c.name}`, async () => {
    const { pendingShownInHistory } = await import('./timeline.mjs');
    assert.equal(pendingShownInHistory(c.saved, c.pending), c.match);
  });
}

test('unwrapSavedPrompt: drops the working prefix and the paste tags, keeps the pasted text', async () => {
  const { unwrapSavedPrompt } = await import('./timeline.mjs');
  assert.equal(unwrapSavedPrompt('The user sent a new message while you were working:\nhi'), 'hi');
  assert.equal(unwrapSavedPrompt('<pasted_content id="1">\na\n</pasted_content>'), 'a');
  assert.equal(unwrapSavedPrompt('<pasted_content>a</pasted_content>'), 'a');
  assert.equal(unwrapSavedPrompt('say The user sent a new message while you were working: x'), 'say The user sent a new message while you were working: x', 'only a leading prefix');
  assert.equal(unwrapSavedPrompt('<pasted_contentx>a'), '<pasted_contentx>a', 'another tag stays');
  assert.equal(unwrapSavedPrompt(''), '');
});

test('pendingShown: a wrapped history turn shows the message', async () => {
  const { pendingShown } = await import('./timeline.mjs');
  const sentAt = Date.parse('2026-10-26T10:00:00Z');
  const p = { text: 'are you still blocked?', turn: null, sentAt, failure: null };
  const turn = (user) => ({ time: '2026-10-26T10:00:03Z', user, reply: '', tools: [] });
  assert.equal(pendingShown(p, [], [turn('The user sent a new message while you were working:\nare you still blocked?')]), true);
  assert.equal(pendingShown(p, [], [turn('<pasted_content id="9">\nare you still blocked?\n</pasted_content>')]), true);
  assert.equal(pendingShown(p, [], [turn('The user sent a new message while you were working:\nstop')]), false);
});

// How a saved user message shows in its bubble: Claude Code's wrappers removed, image placeholders
// as chips (api/testdata/clients/display/user-bubble.json).
const displayDir = new URL('../../../api/testdata/clients/display/', import.meta.url);
const displayFiles = readdirSync(displayDir).filter((f) => f.endsWith('.json')).sort();

test('the display fixture folder holds the user-bubble cases', () => {
  assert.deepEqual(displayFiles, ['user-bubble.json']);
});

for (const file of displayFiles) {
  const fx = JSON.parse(readFileSync(new URL(file, displayDir), 'utf8'));
  for (const c of fx.cases) {
    test(`display/${file}: ${c.name}`, async () => {
      const { userDisplay } = await import('./timeline.mjs');
      const got = userDisplay(c.saved);
      const parts = c.display.parts ?? (c.display.text ? [{ kind: 'text', text: c.display.text }] : []);
      assert.deepEqual({ text: got.text, images: got.images.length, parts: got.parts },
        { text: c.display.text, images: c.display.images, parts });
    });
  }
}

test('userDisplay labels each chip: "Image n" when numbered, "Image" otherwise, in order', async () => {
  const { userDisplay } = await import('./timeline.mjs');
  assert.deepEqual(userDisplay('[Image #2] and [Image: source: /tmp/a.png] then [Image #10]').images, ['Image 2', 'Image', 'Image 10']);
  assert.deepEqual(userDisplay('no pictures').images, []);
  assert.deepEqual(userDisplay(''), { text: '', images: [], parts: [] });
});

test('buildTimeline: an event of no turn (a queue snapshot) is not a turn, even after a turn ended', () => {
  const done = buildTimeline({ turns: [], events: [
    ev(1, 't1', 'turn_started', '2026-10-26T08:00:00Z', { text: 'hi' }),
    ev(2, 't1', 'turn_done', '2026-10-26T08:00:05Z', {}),
  ], now: 0 });
  const got = buildTimeline({ turns: [], events: [
    ev(1, 't1', 'turn_started', '2026-10-26T08:00:00Z', { text: 'hi' }),
    ev(2, 't1', 'turn_done', '2026-10-26T08:00:05Z', {}),
    ev(3, '', 'queue', '2026-10-26T08:00:06Z', { state: 'running', items: [] }),
  ], now: 0 });
  assert.deepEqual(got, done);
  assert.equal(got.working, false);
});
