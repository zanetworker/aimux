// Run: node --test web/src/components/*.test.ts (Node 22 strips the types).
import test from 'node:test';
import assert from 'node:assert/strict';
import { meterNotes, activityText, ago, buildNumber, engineWords, isCommand, shortTokens, verdictLine } from './system.ts';

test('token counts are shortened to what a table can hold', () => {
  assert.equal(shortTokens(0), '0');
  assert.equal(shortTokens(950), '950');
  assert.equal(shortTokens(1000), '1.0k');
  assert.equal(shortTokens(136082), '136k');
  assert.equal(shortTokens(7917071), '7.9M');
  assert.equal(shortTokens(43491369), '43M');
  assert.equal(shortTokens(undefined), '-');
});

test('a time is said as how long ago, largest unit only', () => {
  const now = Date.parse('2026-11-27T10:00:00Z');
  assert.equal(ago('2026-11-27T09:59:55Z', now), '5s');
  assert.equal(ago('2026-11-27T09:57:00Z', now), '3m');
  assert.equal(ago('2026-11-27T08:00:00Z', now), '2h');
  assert.equal(ago('2026-11-23T10:00:00Z', now), '4d');
  // A clock a little ahead is now, not a negative age; no time or a broken one is a dash.
  assert.equal(ago('2026-11-27T10:00:02Z', now), '0s');
  assert.equal(ago(undefined, now), '-');
  assert.equal(ago('not a time', now), '-');
});

test('a build is its number, as aimux status shows it', () => {
  assert.equal(buildNumber('v0.14.0-326-g2b30bc09'), '326');
  assert.equal(buildNumber('v0.14.0'), 'v0.14.0');
  assert.equal(buildNumber('dev'), 'dev');
  assert.equal(buildNumber(undefined), '-');
});

test('an engine row says what the file states and what runs, in the words of aimux engine list', () => {
  assert.deepEqual(engineWords({ name: 'codex', on: true, type: 'codex-app-server', stated: true, running: true }), { file: 'on', running: 'yes', differs: false });
  assert.deepEqual(engineWords({ name: 'anthropic', on: false, type: 'messages', stated: true, running: true }), { file: 'off', running: 'yes', differs: true });
  assert.deepEqual(engineWords({ name: 'anthropic', on: true, type: 'messages', stated: false, running: true }), { file: 'not stated', running: 'yes', differs: false });
  // The proxy did not answer: nothing is said about running, and nothing differs.
  assert.deepEqual(engineWords({ name: 'enmaas', on: true, type: 'messages', stated: true }), { file: 'on', running: '-', differs: false });
  assert.deepEqual(engineWords({ name: 'byhand', on: false, type: 'not in the file', stated: true, running: true }), { file: '-', running: 'yes', differs: true });
});

test('the diagnosis has one line that says how many things are failing or worth a look', () => {
  const f = (level: string) => ({ id: 'x', level, detail: '' });
  assert.equal(verdictLine([f('ok'), f('ok')]), 'Nothing to fix.');
  assert.equal(verdictLine([f('warn')]), '1 thing to look at; nothing is failing.');
  assert.equal(verdictLine([f('fail'), f('fail')]), '2 things are failing.');
  assert.equal(verdictLine([f('fail'), f('warn'), f('warn')]), '1 thing is failing, 2 things to look at.');
  assert.equal(verdictLine([]), 'Nothing to fix.');
});

test('a fix that is a command is told from one that is advice', () => {
  assert.equal(isCommand('aimux upgrade model-proxy'), true);
  assert.equal(isCommand('claude auth login'), true);
  assert.equal(isCommand('codex login'), true);
  assert.equal(isCommand('in a session that stays stuck: /compact, or /model to a model with a larger window'), false);
  assert.equal(isCommand(''), false);
});

test('last activity is the age and the note, or whichever there is', () => {
  const now = Date.parse('2026-11-27T10:00:00Z');
  assert.equal(activityText({ last_activity: '2026-11-27T09:50:00Z', activity_note: 'turn' }, now), '10m ago · turn');
  assert.equal(activityText({ last_activity: '2026-11-27T09:50:00Z' }, now), '10m ago');
  assert.equal(activityText({ activity_note: '0 leases' }, now), '0 leases');
  assert.equal(activityText({}, now), '-');
});

test('the meter says what its figures leave out', () => {
  const full = { rows: [{ key: 'main', requests: 2 }], files: 2 };
  assert.deepEqual(meterNotes(full), []);
  assert.deepEqual(meterNotes({ rows: [], files: 0 }), ['No ledger has been written on this machine yet.']);
  assert.deepEqual(meterNotes({ rows: [], files: 2 }), ['No model calls passed the proxy in this time.']);
  assert.deepEqual(meterNotes({ ...full, unmatched: 1, skipped: 2, torn: 3, no_usage: 4, incomplete: true }), [
    'The ledger has lines that could not be counted, so these figures are low.',
    'Not in the table: 1 with no session, 2 of another schema, 3 torn.',
    '4 of the calls counted carried no token figures.',
  ]);
  assert.deepEqual(meterNotes({ rows: [{ key: 'main', requests: 2, estimated: true }], files: 1 }), ['~ marks tokens that are partly estimated.']);
});
