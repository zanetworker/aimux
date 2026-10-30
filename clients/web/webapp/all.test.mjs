import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { apiDate } from './apiDate.mjs';
import { allSections } from './all.mjs';

const dir = new URL('../../../api/testdata/clients/all/', import.meta.url);
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort();

test('the fixture folder is not empty', () => {
  assert.ok(files.length >= 6, `only ${files.length} fixtures found`);
});

for (const file of files) {
  const fx = JSON.parse(readFileSync(new URL(file, dir), 'utf8'));
  test(`all/${file}: ${fx.name}`, () => {
    const sessions = fx.sessions.map((s) => ({ ...s, lastActive: apiDate(s.lastActive) }));
    const got = allSections({
      sessions, project: fx.project, sort: fx.sort, now: apiDate(fx.now), timeZone: fx.timeZone,
    });
    assert.deepEqual(
      got.map((s) => ({ day: s.day, ids: s.sessions.map((x) => x.id) })),
      fx.expect.sections,
    );
  });
}

test('empty input and a filter matching nothing give no sections', () => {
  const now = apiDate('2026-10-26T09:00:00Z');
  const base = { project: null, sort: 'newest', now, timeZone: 'UTC' };
  assert.deepEqual(allSections({ ...base, sessions: [] }), []);
  const s = [{ id: 'a', project: '/x', state: 'idle', lastActive: now }];
  assert.deepEqual(allSections({ ...base, sessions: s, project: '/y' }), []);
});
