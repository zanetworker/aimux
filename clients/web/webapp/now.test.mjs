import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { apiDate } from './apiDate.mjs';
import { deriveActivity, descriptionLine, holdPlaces, nextSession, rankNow } from './now.mjs';

const dir = new URL('../../../api/testdata/clients/now/', import.meta.url);
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort();

test('the fixture folder is not empty', () => {
  assert.ok(files.length >= 26, `only ${files.length} fixtures found`);
});

const ids = (list) => list.map((x) => x.session?.id ?? x.id);

for (const file of files) {
  const fx = JSON.parse(readFileSync(new URL(file, dir), 'utf8'));
  test(`now/${file}: ${fx.name}`, () => {
    const activity = {};
    for (const [id, events] of Object.entries(fx.events)) activity[id] = deriveActivity(events);
    const sessions = fx.sessions.map((s) => ({ ...s, lastActive: apiDate(s.lastActive) }));
    const got = rankNow({ sessions, activity, now: apiDate(fx.now), query: fx.query, workingOnly: fx.workingOnly ?? false });
    const want = fx.expect;

    assert.deepEqual(ids(got.needsYou), want.needsYou, 'needsYou');
    assert.deepEqual(ids(got.pinned), want.pinned, 'pinned');
    assert.deepEqual(got.windows.map((w) => ({
      id: w.id, name: w.name,
      tabs: w.tabs.map((t) => ({ index: t.index, name: t.name, panes: t.panes, working: t.working, open: t.open, sessions: ids(t.sessions) })),
    })), want.windows, 'windows');
    assert.equal(got.hiddenTabs, want.hiddenTabs, 'hiddenTabs');
    assert.deepEqual(got.elsewhere.map((g) => ({ label: g.label, sessions: ids(g.sessions) })), want.elsewhere, 'elsewhere');
    assert.deepEqual(ids(got.other), want.other, 'other');
    assert.equal(got.working, want.working, 'working');
    assert.equal(got.open, want.open, 'open');
    for (const [id, step] of Object.entries(want.steps ?? {})) {
      assert.equal(activity[id].step ?? null, step, `step of ${id}`);
    }
    for (const [id, line] of Object.entries(want.descriptions ?? {})) {
      assert.equal(descriptionLine(sessions.find((s) => s.id === id)), line, `description of ${id}`);
    }
  });
}

const ev = (seq, turn, kind, data, time = '2026-10-26T08:00:00Z') => ({ seq, turn, kind, time, data });

test('deriveActivity: no events is none', () => {
  assert.deepEqual(deriveActivity([]), { kind: 'none' });
});

test('deriveActivity: the latest of several pending approvals wins', () => {
  const a = deriveActivity([
    ev(1, 't', 'turn_started', { text: 'x' }),
    ev(3, 't', 'approval', { approvalId: 'p2' }),
    ev(2, 't', 'approval', { approvalId: 'p1' }),
  ]);
  assert.equal(a.kind, 'needsApproval');
  assert.equal(a.approval.approvalId, 'p2');
});

test('deriveActivity: step is the newest title, or Thinking for a thought', () => {
  const base = [ev(1, 't', 'turn_started', { text: 'x' })];
  const upd = (seq, sessionUpdate, title) => ev(seq, 't', 'update', title === undefined ? { sessionUpdate } : { sessionUpdate, title });
  assert.equal(deriveActivity([...base, upd(2, 'tool_call', 'Edit a.go')]).step, 'Edit a.go');
  assert.equal(deriveActivity([...base, upd(2, 'tool_call', 'Edit a.go'), upd(3, 'agent_thought_chunk')]).step, 'Thinking');
  assert.equal(deriveActivity([...base, upd(2, 'agent_thought_chunk'), upd(3, 'tool_call_update', 'Run tests')]).step, 'Run tests');
  // a titleless update is skipped, so the earlier title shows
  assert.equal(deriveActivity([...base, upd(2, 'tool_call', 'Edit a.go'), upd(3, 'tool_call_update')]).step, 'Edit a.go');
  assert.equal(deriveActivity(base).step, undefined);
});

test('deriveActivity: the step comes from the running turn only', () => {
  const a = deriveActivity([
    ev(1, 't1', 'turn_started', { text: 'x' }),
    ev(2, 't1', 'update', { sessionUpdate: 'tool_call', title: 'Old' }),
    ev(3, 't1', 'turn_done', { stopReason: 'end_turn' }),
    ev(4, 't2', 'turn_started', { text: 'y' }),
  ]);
  assert.equal(a.kind, 'working');
  assert.equal(a.step, undefined);
});

test('deriveActivity: failed and finished carry their time', () => {
  const f = deriveActivity([ev(1, 't', 'turn_error', { error: 'boom' }, '2026-10-26T08:55:00Z')]);
  assert.deepEqual(f, { kind: 'failed', error: 'boom', at: apiDate('2026-10-26T08:55:00Z') });
  const d = deriveActivity([ev(1, 't', 'turn_done', { stopReason: 'end_turn' }, '2026-10-26T08:56:00Z')]);
  assert.deepEqual(d, { kind: 'finished', at: apiDate('2026-10-26T08:56:00Z') });
});

// holdPlaces: a list with no mac keeps the last places for 30 s (inclusive) after the last list that had some.
const place = (pane) => ({ window: { id: 'w1', index: 1, name: 'Window 1' }, tab: { index: 1, name: 'aimux' }, pane, panes: 2, lastInFront: false });
const listed = (id, mac) => (mac ? { id, state: 'open_in_terminal', mac } : { id, state: 'open_in_terminal' });

test('holdPlaces: a list with places replaces what was held and starts the clock', () => {
  const got = holdPlaces({ places: { a: place(9) }, since: 0 }, [listed('a', place(1)), listed('b')], 5000);
  assert.deepEqual(got.held, { places: { a: place(1) }, since: 5000 });
  assert.equal(got.sessions[0].mac.pane, 1);
  assert.equal(got.sessions[1].mac, undefined);
});

test('holdPlaces: within 30 s, inclusive, a list without places keeps them for sessions still listed', () => {
  const held = { places: { a: place(1), gone: place(2) }, since: 1000 };
  for (const now of [11_000, 31_000]) {
    const got = holdPlaces(held, [listed('a'), listed('b')], now);
    assert.equal(got.held, held, 'the clock is not restarted');
    assert.deepEqual(got.sessions.map((s) => s.mac?.pane ?? null), [1, null]);
  }
});

test('holdPlaces: after 30 s the places are dropped; nothing held stays nothing', () => {
  const got = holdPlaces({ places: { a: place(1) }, since: 1000 }, [listed('a')], 31_001);
  assert.equal(got.held, null);
  assert.equal(got.sessions[0].mac, undefined);
  const none = holdPlaces(null, [listed('a')], 0);
  assert.equal(none.held, null);
  assert.deepEqual(none.sessions, [listed('a')]);
});

// The chat's Next bar, like NextSessionTests in the iPhone app.
const item = (id) => ({ session: { id, state: 'idle' }, activity: { kind: 'none' } });
const view = ({ needsYou = [], pinned = [], windows = [], elsewhere = [] } = {}) =>
  ({ needsYou, pinned, windows, hiddenTabs: 0, elsewhere, other: [], working: 0, open: 0 });
const busy = (id) => ({ id, state: 'busy' });
const idle = (id) => ({ id, state: 'open_in_terminal' });
const tab = (...sessions) => ({ index: 1, name: 't', panes: sessions.length, working: 0, open: false, sessions });

test('nextSession: Needs you first, never the open session', () => {
  assert.equal(nextSession(view({ needsYou: [item('n1')], elsewhere: [{ label: 'x', sessions: [busy('w1')] }] }), 'x').session.id, 'n1');
  assert.equal(nextSession(view({ needsYou: [item('self')], elsewhere: [{ label: 'x', sessions: [busy('w1')] }] }), 'self').session.id, 'w1');
  assert.equal(nextSession(view({ needsYou: [item('self')] }), 'self'), null);
});

test('nextSession: the first working session in Now order: Pinned, the windows, then Elsewhere', () => {
  const v = view({
    pinned: [{ session: idle('p-idle'), activity: { kind: 'none' } }],
    windows: [{ id: 'w', name: 'Window 1', tabs: [tab(idle('t1'), busy('t2'))] }],
    elsewhere: [{ label: 'x', sessions: [busy('e1')] }],
  });
  assert.equal(nextSession(v, 'x').session.id, 't2');
  assert.equal(nextSession(v, 't2').session.id, 'e1');
  const byActivity = nextSession(v, 't2', new Set(), { t1: { kind: 'working', step: 'Edit' } });
  assert.equal(byActivity.session.id, 't1');
  assert.equal(byActivity.activity.step, 'Edit');
});

test('nextSession: a session whose approval was answered is skipped as a working target, not in Needs you', () => {
  const v = view({ elsewhere: [{ label: 'x', sessions: [busy('answered'), busy('w2')] }] });
  assert.equal(nextSession(v, 'self', new Set(['answered'])).session.id, 'w2');
  assert.equal(nextSession(view({ elsewhere: [{ label: 'x', sessions: [busy('answered')] }] }), 'self', new Set(['answered'])), null);
  assert.equal(nextSession(view({ needsYou: [item('answered')] }), 'self', new Set(['answered'])).session.id, 'answered');
});
