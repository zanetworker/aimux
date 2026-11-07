// Run: node --test web/src/components/*.test.ts (Node 22 strips the types).
import test from 'node:test';
import assert from 'node:assert/strict';
import { stateLabel, STATUS_WORDS } from './stateLabel.ts';

test('the state service activity gives the TUI and phone words', () => {
  assert.equal(stateLabel({ Status: 2, Activity: 'needs_you' }), 'needs you');
  assert.equal(stateLabel({ Status: 0, Activity: 'working' }), 'working');
  assert.equal(stateLabel({ Status: 1, Activity: 'idle' }), 'idle');
});

test('an error says more than idle', () => {
  assert.equal(stateLabel({ Status: 3, Activity: 'idle' }), 'error');
});

test('gone, unknown or missing activity falls back to the status word', () => {
  assert.equal(stateLabel({ Status: 1, Activity: 'gone' }), 'idle');
  assert.equal(stateLabel({ Status: 0 }), 'working');
  assert.equal(stateLabel({ Status: 2, Activity: 'something new' }), 'needs you');
  assert.equal(stateLabel({ Status: 4 }), 'unknown');
  assert.equal(stateLabel({ Status: 99 }), 'unknown');
});

test('the status words the filters use are the same words', () => {
  assert.deepEqual(STATUS_WORDS, { 0: 'working', 1: 'idle', 2: 'needs you', 3: 'error', 4: 'unknown' });
});

test('no component keeps its own status words: they all go through stateLabel', async () => {
  const { readdirSync, readFileSync } = await import('node:fs');
  const dir = new URL('.', import.meta.url);
  const old = /['"](ACTIVE|WAITING|Active|Waiting)['"]/;
  for (const f of readdirSync(dir).filter((n: string) => n.endsWith('.tsx'))) {
    const src = readFileSync(new URL(f, dir), 'utf8');
    assert.doesNotMatch(src, old, `${f} still has an old status word`);
  }
});
