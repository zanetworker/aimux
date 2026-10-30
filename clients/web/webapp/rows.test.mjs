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

import { sinceText } from './rows.mjs';

test('sinceText: just now, minutes and hours ago; a time ahead of the clock is just now', () => {
  const now = new Date(2026, 9, 30, 12, 0).getTime();
  const MIN = 60_000;
  assert.equal(sinceText(now - 30_000, now), 'just now');
  assert.equal(sinceText(now + 5 * MIN, now), 'just now');
  assert.equal(sinceText(now - 4 * MIN, now), '4 min ago');
  assert.equal(sinceText(now - 3 * 60 * MIN, now), '3 h ago');
  assert.equal(sinceText(now - 13 * 60 * MIN, now), 'Yesterday');
});
