import { test } from 'node:test';
import assert from 'node:assert/strict';
import { displayTitle } from './rows.mjs';

const id = 'd12ce59e-17e0-4534-8027-eac22f02c34f';

for (const [title, want] of [
  ['Session ' + id, 'Session d12ce59e'],
  ['', 'Session d12ce59e'],
  ['   ', 'Session d12ce59e'],
  [id, 'Session d12ce59e'],
  ['  Fix login page  ', 'Fix login page'],
  ['Session notes for Monday', 'Session notes for Monday'],
  ['SESSION ' + id.toUpperCase(), 'Session d12ce59e'],
]) {
  test(`displayTitle(${JSON.stringify(title)})`, () => assert.equal(displayTitle(title, id), want));
}

test('displayTitle tolerates a missing title', () => assert.equal(displayTitle(undefined, id), 'Session d12ce59e'));

import { rowPlace, rowState } from './rows.mjs';

test('rowState: working with its step, your turn since the last activity when open, none when closed', () => {
  const now = new Date(2026, 9, 30, 12, 0).getTime();
  const MIN = 60_000;
  const base = { id: 'a', lastActive: now - 4 * MIN };
  assert.equal(rowState({ ...base, state: 'busy' }, { kind: 'working', step: 'go test ./...' }, now).text, 'Working · go test ./...');
  assert.equal(rowState({ ...base, state: 'open_in_terminal', working: true }, { kind: 'none' }, now).text, 'Working');
  assert.equal(rowState({ ...base, state: 'idle' }, { kind: 'working', step: '' }, now).text, 'Working');
  assert.equal(rowState({ ...base, state: 'open_in_terminal' }, { kind: 'none' }, now).text, 'Your turn · 4 min');
  assert.equal(rowState({ ...base, state: 'idle', mac: {}, lastActive: now + MIN }, undefined, now).text, 'Your turn · just now');
  assert.equal(rowState({ ...base, state: 'idle' }, { kind: 'none' }, now), null);
});

test('rowPlace: the folder label (or the project name), then tab and pane', () => {
  const mac = { tab: { index: 3 }, pane: 2 };
  assert.equal(rowPlace({ project: '/src/aimux', folder: { label: 'aimux / clients · ios' }, mac }, false), 'aimux / clients · ios · pane 2');
  assert.equal(rowPlace({ project: '/src/aimux/', mac }, true), 'aimux · tab 3 · pane 2');
  assert.equal(rowPlace({ project: '/src/blog' }, true), 'blog');
});
