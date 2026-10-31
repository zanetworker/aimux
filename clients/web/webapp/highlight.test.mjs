import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { highlight } from './highlight.mjs';

const DIR = new URL('../../../api/testdata/clients/highlight/', import.meta.url);
const files = readdirSync(DIR).filter((f) => f.endsWith('.json')).sort();

test('the highlight fixtures are there', () => assert.ok(files.length >= 10));

for (const f of files) {
  const c = JSON.parse(readFileSync(new URL(f, DIR), 'utf8'));
  test('highlight/' + f + ': ' + c.name, () => {
    const got = highlight(c.code, c.lang).map((t) => [t.k, t.v]);
    assert.deepEqual(got, c.tokens);
    assert.equal(got.map((t) => t[1]).join(''), c.code, 'lossless');
  });
}

test('language names are case-insensitive and aliases work', () => {
  assert.deepEqual(highlight('return 1', 'GO'), highlight('return 1', 'golang'));
  assert.deepEqual(highlight('', 'go'), []);
});
