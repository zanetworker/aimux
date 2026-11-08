import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { apiDate } from './apiDate.mjs';
import { allSections } from './all.mjs';

const dir = new URL('../../../api/testdata/clients/all/', import.meta.url);
const fixtures = readdirSync(dir).filter((f) => f.endsWith('.json')).sort()
  .map((file) => ({ file, fx: JSON.parse(readFileSync(new URL(file, dir), 'utf8')) }))
  .filter(({ fx }) => fx.sort === 'recent');

test('the Recent fixtures are there', () => {
  assert.ok(fixtures.length >= 3, `only ${fixtures.length} Recent fixtures found`);
});

for (const { file, fx } of fixtures) {
  test(`all/${file}: ${fx.name}`, () => {
    const sessions = fx.sessions.map((s) => ({ ...s, lastActive: apiDate(s.lastActive) }));
    const got = allSections({ sessions, project: fx.project, now: apiDate(fx.now), timeZone: fx.timeZone });
    assert.deepEqual(got.map((s) => ({ day: s.day, ids: s.sessions.map((x) => x.id) })), fx.expect.sections);
  });
}

test('empty input and a filter matching nothing give no sections', () => {
  const now = apiDate('2026-10-26T09:00:00Z');
  const base = { project: null, now, timeZone: 'UTC' };
  assert.deepEqual(allSections({ ...base, sessions: [] }), []);
  const s = [{ id: 'a', project: '/x', state: 'idle', lastActive: now }];
  assert.deepEqual(allSections({ ...base, sessions: s, project: '/y' }), []);
});
