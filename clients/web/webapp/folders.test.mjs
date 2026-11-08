import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { apiDate } from './apiDate.mjs';
import { folderTree } from './folders.mjs';

const dir = new URL('../../../api/testdata/clients/all/', import.meta.url);
const fixtures = readdirSync(dir).filter((f) => f.endsWith('.json')).sort()
  .map((file) => ({ file, fx: JSON.parse(readFileSync(new URL(file, dir), 'utf8')) }))
  .filter(({ fx }) => fx.sort === 'folders');

test('the Folders fixtures are there', () => {
  assert.ok(fixtures.length >= 3, `only ${fixtures.length} Folders fixtures found`);
});

const ids = (list) => list.map((s) => s.id);

for (const { file, fx } of fixtures) {
  test(`all/${file}: ${fx.name}`, () => {
    const sessions = fx.sessions.map((s) => ({ ...s, lastActive: apiDate(s.lastActive) }));
    const got = folderTree(sessions, apiDate(fx.now), fx.project);
    assert.deepEqual(got.map((r) => ({
      repoPath: r.repoPath, name: r.name, count: r.count, working: r.working, open: r.open, sessions: ids(r.sessions),
      worktrees: r.worktrees.map((w) => ({ name: w.name, count: w.count, working: w.working, open: w.open, sessions: ids(w.sessions) })),
    })), fx.expect.repos);
  });
}

const s = (id, ago, extra = {}) => ({ id, project: '/src/' + id, state: 'idle', lastActive: 1_000_000 - ago, ...extra });

test('folderTree: empty input and a filter that matches nothing give no repos', () => {
  assert.deepEqual(folderTree([], 1_000_000), []);
  assert.deepEqual(folderTree([s('a', 0)], 1_000_000, '/src/b'), []);
});

test('folderTree: activity plays no part in working; a time after now counts as now', () => {
  const tree = folderTree([s('a', -5000, { state: 'open_in_terminal' }), s('b', 0, { project: '/src/a', working: true })], 1_000_000);
  assert.equal(tree.length, 1);
  assert.deepEqual(ids(tree[0].sessions), ['a', 'b'], 'equal times (after now counts as now) order by id');
  assert.equal(tree[0].working, 1);
  assert.equal(tree[0].open, true);
});
