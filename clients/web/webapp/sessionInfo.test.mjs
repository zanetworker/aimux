import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ago, formatCost, formatDuration, formatTokens, infoSections } from './sessionInfo.mjs';

// The same cases as StatsFormatTests in the iPhone app.
test('cost: cents, and <$0.01 below a cent', () => {
  for (const [usd, text] of [[0.42, '$0.42'], [12.5, '$12.50'], [0.004, '<$0.01'], [0, '$0.00'], [0.01, '$0.01']]) {
    assert.equal(formatCost(usd), text, String(usd));
  }
});

test('tokens: plain under a thousand, then k, then M', () => {
  for (const [n, text] of [[0, '0'], [999, '999'], [1000, '1k'], [34_200, '34k'], [1_200_000, '1.2M'], [2_000_000, '2M'], [999_999, '1M']]) {
    assert.equal(formatTokens(n), text, String(n));
  }
});

test('duration: minutes, hours and minutes, <1m', () => {
  for (const [min, text] of [[72, '1h 12m'], [12.4, '12m'], [0.3, '<1m'], [120, '2h'], [59.6, '1h']]) {
    assert.equal(formatDuration(min), text, String(min));
  }
});

test('ago: just now, minutes, hours, days', () => {
  const now = Date.UTC(2026, 9, 26, 12);
  const MIN = 60_000;
  assert.equal(ago(now - 30_000, now), 'just now');
  assert.equal(ago(now - 5 * MIN, now), '5m ago');
  assert.equal(ago(now - 14 * 60 * MIN, now), '14h ago');
  assert.equal(ago(now - 3 * 24 * 60 * MIN, now), '3d ago');
  assert.equal(ago(now + 5 * MIN, now), 'just now', 'a clock ahead of ours');
});

const time = (ms) => 'T' + ms;
const values = (sections) => Object.fromEntries(sections.flat().map((r) => [r.label, r.value]));

test('info: every row from the stats, folder and last active from the detail', () => {
  const detail = {
    project: '/Users/me/aimux', lastActive: '2026-10-26T10:00:00Z',
    stats: {
      costUsd: 0.42, tokensIn: 1_200_000, tokensOut: 34_200, cacheReadTokens: 880_000, cacheWriteTokens: 62_000,
      turns: 14, startTime: '2026-10-26T08:48:00Z', durationMin: 72, model: 'claude-opus-4', gitBranch: 'fix/sse',
    },
  };
  const sections = infoSections(detail, time);
  assert.equal(sections.length, 3);
  assert.deepEqual(values(sections), {
    Cost: '$0.42', 'Tokens in': '1.2M', 'Tokens out': '34k', 'Cache read': '880k', 'Cache write': '62k',
    Model: 'claude-opus-4', Turns: '14', Duration: '1h 12m',
    Started: 'T' + Date.parse('2026-10-26T08:48:00Z'), 'Last active': 'T' + Date.parse('2026-10-26T10:00:00Z'),
    Branch: 'fix/sse', Folder: '/Users/me/aimux',
  });
  assert.deepEqual(sections[2].map((r) => r.mono), [true, true], 'branch and folder in monospace');
});

test('info: Not available when the session has no stats yet', () => {
  const v = values(infoSections({ project: '/p', lastActive: '2026-10-26T10:00:00Z' }, time));
  for (const label of ['Cost', 'Tokens in', 'Tokens out', 'Cache read', 'Cache write', 'Model', 'Turns', 'Duration', 'Started', 'Branch']) {
    assert.equal(v[label], 'Not available', label);
  }
  assert.equal(v.Folder, '/p');
  assert.equal(v['Last active'], 'T' + Date.parse('2026-10-26T10:00:00Z'));
});

test('info: an empty model or branch is Not available, an unparsable time too', () => {
  const v = values(infoSections({ project: '', lastActive: 'nope', stats: {
    costUsd: 0, tokensIn: 0, tokensOut: 0, cacheReadTokens: 0, cacheWriteTokens: 0, turns: 0, startTime: 'nope', durationMin: 0, model: '',
  } }, time));
  assert.equal(v.Model, 'Not available');
  assert.equal(v.Branch, 'Not available');
  assert.equal(v.Started, 'Not available');
  assert.equal(v['Last active'], 'Not available');
  assert.equal(v.Folder, 'Not available');
  assert.equal(v.Cost, '$0.00');
  assert.equal(v.Turns, '0');
});

test('info: Started is Not available when the stats have no start time', () => {
  const v = values(infoSections({ project: '/p', lastActive: '2026-10-26T10:00:00Z', stats: {
    costUsd: 0.42, tokensIn: 1000, tokensOut: 200, cacheReadTokens: 0, cacheWriteTokens: 0, turns: 2, durationMin: 3,
  } }, time));
  assert.equal(v.Started, 'Not available');
  assert.equal(v.Cost, '$0.42');
  assert.equal(v.Duration, '3m');
});

import { describeErrorText, describeResultText, namingLine, ownerTextError } from './sessionInfo.mjs';

test('Update summary: a fixed message for every error code, and the network', () => {
  const err = (status, code) => Object.assign(new Error('raw server text'), { status, code });
  const codes = ['describe_running', 'owner_set', 'no_content', 'naming_timeout', 'naming_auth', 'naming_model',
    'naming_unreadable', 'naming_failed', 'naming_unavailable'];
  const texts = codes.map((c) => describeErrorText(err(409, c)));
  assert.equal(new Set(texts).size, codes.length, 'each code has its own message');
  for (const t of texts) assert.ok(!t.includes('raw server text'), t);
  assert.equal(describeErrorText(new TypeError('Failed to fetch')), 'Could not reach your Mac.');
  assert.equal(describeErrorText(err(500, 'internal')), 'Could not update the summary (HTTP 500).');
});

test('Update summary: what changed and what was kept', () => {
  assert.equal(describeResultText({ changed: ['title', 'description'], kept: [] }), 'Updated the title and description.');
  assert.equal(describeResultText({ changed: ['description'], kept: ['title'] }), 'Updated the description. The title stays as it was.');
  assert.equal(describeResultText({ changed: ['title'], kept: ['description'] }), 'Updated the title. The description stays as it was.');
  assert.equal(describeResultText({ changed: [], kept: ['title', 'description'] }), 'Nothing changed.');
  assert.equal(describeResultText({}), 'Nothing changed.');
});

test('naming line: today of the limit, paused, absent', () => {
  assert.equal(namingLine({ today: 4, limit: 20, failing: false }), 'Named 4 of 20 today');
  assert.equal(namingLine({ today: 20, limit: 20, failing: true, reason: 'the model call failed', paused: true }), 'Naming paused');
  assert.equal(namingLine(undefined), null);
  assert.equal(namingLine(null), null);
});

test('owner text: at most the limit in characters, one line', () => {
  assert.equal(ownerTextError('a'.repeat(80), 80), null);
  assert.equal(ownerTextError('a'.repeat(81), 80), 'At most 80 characters.');
  assert.equal(ownerTextError('😀'.repeat(80), 80), null, 'characters, not UTF-16 units');
  assert.equal(ownerTextError('  ' + 'a'.repeat(80) + '  ', 80), null, 'trimmed first, as the server does');
  assert.equal(ownerTextError('one\ntwo', 300), 'One line, without control characters.');
  assert.equal(ownerTextError('', 80), null, 'empty clears the owner value');
});

// Review round 1: the sheet's state is per opened session; announcements; fixed pin errors.
import { announce, createInfoSheet, pinErrorText, renderMetaCard } from './sessionInfo.mjs';

// A fake model whose describe and editMeta answers are released by the test.
function gatedModel() {
  const calls = [];
  const gates = [];
  const model = {
    describing: new Set(),
    describe(id) {
      if (this.describing.has(id)) return null;
      this.describing.add(id);
      calls.push(['describe', id]);
      return new Promise((resolve, reject) => gates.push({ resolve, reject })).finally(() => this.describing.delete(id));
    },
    editMeta(id, fields) {
      calls.push(['editMeta', id, fields]);
      return new Promise((resolve, reject) => gates.push({ resolve, reject }));
    },
  };
  return { model, calls, gates };
}

function sheetHarness() {
  const { model, calls, gates } = gatedModel();
  const changes = [], took = [], said = [], alerts = [];
  const sheet = createInfoSheet({
    model,
    changed: (st, key) => changes.push([st.id, key]),
    took: (id, r) => took.push([id, r.title]),
    announce: (t) => said.push(t),
    alert: (t) => alerts.push(t),
  });
  return { sheet, model, calls, gates, changes, took, said, alerts };
}
const tick = () => new Promise((r) => setTimeout(r, 0));

test('sheet: an Update summary for A that finishes after B opened leaves B alone', async () => {
  const h = sheetHarness();
  h.sheet.open('A');
  const running = h.sheet.describe();
  const b = h.sheet.open('B');
  h.changes.length = 0;
  h.gates[0].resolve({ title: 'New A', changed: ['title'], kept: [] });
  await running;
  assert.equal(b.note, '', 'B shows nothing of A');
  assert.deepEqual(h.changes, [], 'B is not redrawn');
  assert.deepEqual(h.said, [], 'nothing announced for a sheet that is closed');
  assert.deepEqual(h.took, [['A', 'New A']], 'A still takes its new title');
});

test('sheet: reopening A while its Update summary runs does not send a second one, and shows the result', async () => {
  const h = sheetHarness();
  h.sheet.open('A');
  const running = h.sheet.describe();
  const again = h.sheet.open('A');
  await h.sheet.describe();
  assert.equal(h.calls.filter((c) => c[0] === 'describe').length, 1);
  h.gates[0].resolve({ title: 'New A', changed: ['title'], kept: ['description'] });
  await running;
  assert.equal(again.note, 'Updated the title. The description stays as it was.');
  assert.deepEqual(h.said, [again.note]);
});

test('sheet: an Update summary error is the fixed line, announced', async () => {
  const h = sheetHarness();
  const a = h.sheet.open('A');
  const running = h.sheet.describe();
  h.gates[0].reject(Object.assign(new Error('raw'), { status: 504, code: 'naming_timeout' }));
  await running;
  assert.equal(a.note, 'The model took too long. Try again.');
  assert.deepEqual(h.said, ['The model took too long. Try again.']);
});

test('sheet: Save with the value unchanged sends nothing and closes the field', async () => {
  const h = sheetHarness();
  const a = h.sheet.open('A');
  h.sheet.edit('title', 'Fix the login redirect');
  h.sheet.draft('  Fix the login redirect ');
  await h.sheet.save();
  assert.equal(h.calls.length, 0);
  assert.equal(a.editing, null);
  assert.deepEqual(h.changes.at(-1), ['A', 'edit:title']);
});

test('sheet: a Save for A that finishes after B opened leaves B alone; a failed Save is alerted on its own sheet', async () => {
  const h = sheetHarness();
  h.sheet.open('A');
  h.sheet.edit('description', '');
  h.sheet.draft('Finds the loop');
  const saving = h.sheet.save();
  await h.sheet.save(); // a second tap while saving is ignored
  assert.equal(h.calls.length, 1);
  const b = h.sheet.open('B');
  h.changes.length = 0;
  h.gates[0].resolve({ title: 'A', description: 'Finds the loop', pinned: false });
  await saving;
  assert.deepEqual(h.changes, []);
  assert.equal(b.editing, null);
  assert.deepEqual(h.took, [['A', 'A']]);

  const c = h.sheet.open('C');
  h.sheet.edit('title', 'old');
  h.sheet.draft('new');
  const failing = h.sheet.save();
  h.gates[1].reject(Object.assign(new Error('title holds control characters (it is one line)'), { status: 400, code: 'bad_request' }));
  await failing;
  assert.equal(c.editing, 'title', 'the field stays open with the text');
  assert.equal(c.error, 'Could not save: title holds control characters (it is one line)');
  assert.deepEqual(h.alerts, [c.error]);
});

test('sheet: pin sends only pinned; a failure is the fixed line; a later success clears it', async () => {
  const h = sheetHarness();
  const a = h.sheet.open('A');
  const first = h.sheet.pin(true);
  h.gates[0].reject(new TypeError('Failed to fetch'));
  await first;
  assert.equal(a.pinError, 'Could not pin: your Mac could not be reached.');
  const second = h.sheet.pin(true);
  h.gates[1].resolve({ title: 'A', pinned: true });
  await second;
  assert.equal(a.pinError, '');
  assert.deepEqual(h.calls.map((c) => c[2]), [{ pinned: true }, { pinned: true }]);
  assert.deepEqual(h.said, ['Could not pin: your Mac could not be reached.', 'Pinned']);
});

test('pinErrorText: fixed lines, never the server text', () => {
  assert.equal(pinErrorText(new TypeError('x'), true), 'Could not pin: your Mac could not be reached.');
  assert.equal(pinErrorText(Object.assign(new Error('raw'), { status: 404, code: 'not_supported' }), false), 'Could not unpin (HTTP 404).');
});

test('announce: the same text twice is still a change, so it is read again', () => {
  const node = { textContent: '' };
  announce(node, 'Saved');
  assert.equal(node.textContent, 'Saved');
  announce(node, 'Saved');
  assert.notEqual(node.textContent, 'Saved');
  assert.equal(node.textContent.trim(), 'Saved');
});

// A minimal DOM: elements with attributes, dataset, children, text and listeners.
function fakeDocument() {
  const el = (tag) => ({
    tagName: tag, children: [], attrs: {}, listeners: {}, dataset: {}, className: '', _text: '',
    append(...xs) { for (const x of xs) this.children.push(typeof x === 'string' ? { text: x } : x); },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    addEventListener(k, f) { this.listeners[k] = f; },
    get textContent() { return this._text + this.children.map((c) => c.textContent ?? c.text ?? '').join(''); },
    set textContent(v) { this._text = v; this.children = []; },
  });
  return { createElement: el, createElementNS: (_, tag) => el(tag) };
}
function findAll(node, pred, out = []) {
  if (pred(node)) out.push(node);
  for (const c of node.children || []) findAll(c, pred, out);
  return out;
}

test('meta card: while Update summary runs its button stays enabled, aria-disabled, and ignores taps; no live region inside', () => {
  globalThis.document = fakeDocument();
  let asked = 0;
  const nodes = renderMetaCard({
    meta: { title: 'A', description: '', pinned: false },
    state: { editing: null, draft: '', saving: false, error: '', note: 'Updating', pinning: true, pinError: '' },
    describing: true, canEdit: true, naming: null,
    on: { describe: () => { asked += 1; }, pin: () => { asked += 1; }, edit() {}, draft() {}, save() {}, cancel() {} },
  });
  const all = nodes.flatMap((n) => findAll(n, () => true));
  const update = all.find((n) => n.dataset?.focus === 'describe');
  const pin = all.find((n) => n.dataset?.focus === 'pin');
  for (const b of [update, pin]) {
    assert.notEqual(b.disabled, true, 'not disabled: focus stays on it');
    assert.equal(b.getAttribute('aria-disabled'), 'true');
    b.listeners.click();
  }
  assert.equal(asked, 0, 'taps while running are ignored');
  assert.equal(update.textContent, 'Updating summary…');
  assert.equal(all.filter((n) => n.getAttribute?.('role') === 'status' || n.getAttribute?.('aria-live')).length, 0, 'announcements go to the persistent regions');
});

test('meta card: Save while saving stays enabled and aria-disabled; Escape while composing does not cancel', () => {
  globalThis.document = fakeDocument();
  let saved = 0, cancelled = 0;
  const nodes = renderMetaCard({
    meta: { title: 'A', description: '', pinned: false },
    state: { editing: 'title', draft: 'B', saving: true, error: '', note: '', pinning: false, pinError: '' },
    describing: false, canEdit: true, naming: null,
    on: { describe() {}, pin() {}, edit() {}, draft() {}, save: () => { saved += 1; }, cancel: () => { cancelled += 1; } },
  });
  const all = nodes.flatMap((n) => findAll(n, () => true));
  const save = all.find((n) => n.dataset?.focus === 'save:title');
  assert.notEqual(save.disabled, true);
  assert.equal(save.getAttribute('aria-disabled'), 'true');
  save.listeners.click();
  assert.equal(saved, 0);
  const input = all.find((n) => n.dataset?.focus === 'input:title');
  const key = (k, composing) => input.listeners.keydown({ key: k, isComposing: composing, shiftKey: false, preventDefault() {}, stopPropagation() {} });
  key('Escape', true);
  assert.equal(cancelled, 0, 'Escape that ends an IME composition is not Cancel');
  key('Escape', false);
  assert.equal(cancelled, 1);
});

test('sheet: clearing a value the owner did not write announces Nothing to clear, not Saved', async () => {
  const h = sheetHarness();
  h.sheet.open('A');
  h.sheet.edit('title', 'Fix the parser bug');
  h.sheet.draft('');
  const saving = h.sheet.save();
  h.gates[0].resolve({ title: 'Fix the parser bug', pinned: false }); // the model's title stays
  await saving;
  assert.deepEqual(h.said, ['Nothing to clear: this title was not written by you.']);

  // clearing the owner's own value falls back to another one: Saved
  h.sheet.edit('description', 'Mine');
  h.sheet.draft(' ');
  const clearing = h.sheet.save();
  h.gates[1].resolve({ title: 'Fix the parser bug', description: 'Fixes the parser.', pinned: false });
  await clearing;
  assert.equal(h.said.at(-1), 'Saved');
});
