import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { apiDate } from './apiDate.mjs';
import { deriveActivity, descriptionLine, rankNow } from './now.mjs';

const dir = new URL('../../../api/testdata/clients/now/', import.meta.url);
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort();

test('the fixture folder is not empty', () => {
  assert.ok(files.length >= 19, `only ${files.length} fixtures found`);
});

const ids = (items) => items.map((i) => i.session.id);

for (const file of files) {
  const fx = JSON.parse(readFileSync(new URL(file, dir), 'utf8'));
  test(`now/${file}: ${fx.name}`, () => {
    const activity = {};
    for (const [id, events] of Object.entries(fx.events)) activity[id] = deriveActivity(events);
    const seen = Object.fromEntries(Object.entries(fx.seen).map(([k, v]) => [k, apiDate(v)]));
    const sessions = fx.sessions.map((s) => ({ ...s, lastActive: apiDate(s.lastActive) }));
    const lastOnMac = fx.lastOnMac ? { id: fx.lastOnMac.id, at: apiDate(fx.lastOnMac.at) } : null;
    const got = rankNow({ sessions, activity, seen, now: apiDate(fx.now), lastOnMac });
    const want = fx.expect;

    if ('lastOnMac' in want) assert.equal(got.lastOnMac?.session.id ?? null, want.lastOnMac, 'lastOnMac');
    if ('pinned' in want) assert.deepEqual(ids(got.pinned), want.pinned, 'pinned');
    for (const [id, line] of Object.entries(want.descriptions ?? {})) {
      assert.equal(descriptionLine(sessions.find((s) => s.id === id)), line, `description of ${id}`);
    }

    assert.deepEqual(ids(got.needsYou), want.needsYou, 'needsYou');
    assert.deepEqual(ids(got.finished), want.finished, 'finished');
    assert.deepEqual(got.onMac.map((s) => s.id), want.onMac, 'onMac');
    assert.equal(got.onMacTotal, want.onMacTotal, 'onMacTotal');
    assert.deepEqual(got.summary, want.summary, 'summary');
    for (const [id, step] of Object.entries(want.steps ?? {})) {
      assert.equal(activity[id].step ?? null, step, `step of ${id}`);
    }
    if (want.working.list) {
      assert.deepEqual(ids(got.working.list ?? []), want.working.list, 'working list');
      assert.ok((got.working.list ?? []).every((r) => r.kind === 'session'), 'list rows are sessions');
    } else {
      assert.equal(got.working.list, undefined, 'working should be grouped');
      assert.deepEqual(
        got.working.groups.map((g) => ({
          kind: g.kind, project: g.project, name: g.name, count: g.count, expanded: g.expanded,
          newest: g.newest.session.id, sessions: ids(g.items),
        })),
        want.working.groups,
        'working groups',
      );
      assert.deepEqual(got.working.more ?? null, want.working.more ?? null, 'working more');
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

// The chat's Next bar, like NextSessionTests in the iPhone app.
test('nextSession: Needs you before Working, never the open session, groups read, null when none', async () => {
  const { nextSession } = await import('./now.mjs');
  const item = (id) => ({ session: { id }, activity: { kind: 'none' } });
  const view = (needsYou, working) => ({ needsYou, finished: [], working, onMac: [], onMacTotal: 0, summary: {} });
  assert.equal(nextSession(view([item('n1')], { list: [item('w1')] }), 'x').session.id, 'n1');
  assert.equal(nextSession(view([item('self')], { list: [item('w1')] }), 'self').session.id, 'w1');
  const groups = { groups: [{ project: '/p', name: 'p', count: 2, newest: item('self'), items: [item('self'), item('w2')] }] };
  assert.equal(nextSession(view([], groups), 'self').session.id, 'w2');
  assert.equal(nextSession(view([item('self')], { list: [] }), 'self'), null);
});

test('Last on your Mac and Pinned rows keep the session status for their dot', () => {
  const now = Date.parse('2026-10-30T12:00:00Z');
  const s = (id, state, extra = {}) => ({ id, provider: 'claude', title: id, project: '/p', lastActive: now - 60_000, state, ...extra });
  const got = rankNow({
    sessions: [s('w', 'busy', { pinned: true }), s('i', 'idle', { pinned: true }), s('t', 'open_in_terminal')],
    activity: {}, seen: {}, now, lastOnMac: { id: 't', at: now },
  });
  assert.equal(got.lastOnMac.status, 'working', 'a terminal active in the last 900 s is working');
  assert.deepEqual(got.pinned.map((i) => [i.session.id, i.status]), [['i', 'idle'], ['w', 'working']]);
});

test('rankNow without lastOnMac shows no Last on your Mac row', () => {
  const got = rankNow({ sessions: [], activity: {}, seen: {}, now: 0 });
  assert.equal(got.lastOnMac, null);
  assert.deepEqual(got.pinned, []);
});
