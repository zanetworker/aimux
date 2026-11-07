// Run: node --test web/src/components/*.test.ts (Node 22 strips the types).
import test from 'node:test';
import assert from 'node:assert/strict';
import { filterSessions, totalShowable, defaultSwitches, type FilterableSession, type SessionFilter } from './sessionFilter.ts';

const row = (id: string, more: Partial<FilterableSession> = {}): FilterableSession =>
  ({ id, starred: false, project: '/w', hidden: [], ...more });

const rows: FilterableSession[] = [
  row('plain', { shownTitle: 'Fix the cache' }),
  row('star', { starred: true, shownTitle: 'Starred one' }),
  row('sub', { hidden: ['subagents'], shownTitle: 'cache subagent' }),
  row('an', { hidden: ['analyzers'] }),
  row('empty', { hidden: ['search'], shownTitle: 'tiny cache' }),
  row('other', { project: '/elsewhere', shownTitle: 'cache elsewhere' }),
  row('auto', { hidden: ['automated'], shownTitle: 'nightly cache' }),
];

const none: SessionFilter = { starredOnly: false, showSubagents: false, showAutomated: false, showAnalyzers: false, dirFilter: '', filter: '', deepMatches: null };
const ids = (f: Partial<SessionFilter>) => filterSessions(rows, { ...none, ...f }).map(s => s.id).sort().join(' ');

test('default: hidden rows stay hidden', () => {
  assert.equal(ids({}), 'other plain star');
});

test('a reveal shows its rows; a search shows near-empty ones', () => {
  assert.equal(ids({ showSubagents: true }), 'other plain star sub');
  assert.equal(ids({ showAnalyzers: true }), 'an other plain star');
  assert.equal(ids({ showAutomated: true }), 'auto other plain star');
  assert.equal(ids({ filter: 'cache' }), 'empty other plain');
});

test('deep search keeps only base rows: starred-only, hidden and path still apply', () => {
  const deep = new Map([['sub', 'x'], ['other', 'x'], ['plain', 'x'], ['star', 'x']]);
  assert.equal(ids({ deepMatches: deep }), 'other plain star');
  assert.equal(ids({ deepMatches: deep, starredOnly: true }), 'star');
  assert.equal(ids({ deepMatches: deep, dirFilter: '/w' }), 'plain star');
  assert.equal(ids({ deepMatches: deep, showSubagents: true }), 'other plain star sub');
});

test('text filter and deep search: a row matching either is kept', () => {
  const deep = new Map([['star', 'x']]);
  assert.equal(ids({ deepMatches: deep, filter: 'fix' }), 'plain star');
});

test('empty deep result with no text filter shows nothing', () => {
  assert.equal(ids({ deepMatches: new Map() }), '');
});

test('a row without hidden is shown', () => {
  const r = [{ id: 'old', starred: false, project: '' } as FilterableSession];
  assert.equal(filterSessions(r, none).length, 1);
});

test('the tab count at load uses the default switches, as the table total does', () => {
  assert.equal(totalShowable(rows, defaultSwitches), totalShowable(rows, none));
  assert.equal(totalShowable(rows, defaultSwitches), 4);
  // filters other than the switches do not change the total
  assert.equal(totalShowable(rows, { ...none, starredOnly: true, filter: 'zzz', dirFilter: '/nope' }), 4);
});

test('the table total counts only rows the current switches could show', () => {
  // near-empty rows count (a search shows them); switch-hidden rows only when on
  assert.equal(totalShowable(rows, none), 4); // plain star empty other
  assert.equal(totalShowable(rows, { ...none, showAutomated: true }), 5);
  assert.equal(totalShowable(rows, { ...none, showSubagents: true, showAnalyzers: true, showAutomated: true }), rows.length);
  assert.equal(totalShowable([], none), 0);
});
