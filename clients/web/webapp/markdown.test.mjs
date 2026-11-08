import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { parseMarkdown } from './markdown.mjs';

const dir = new URL('../../../api/testdata/clients/markdown/', import.meta.url);
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort();
const fixture = (f) => JSON.parse(readFileSync(new URL(f, dir), 'utf8'));

test('the markdown fixture folder is not empty', () => {
  assert.ok(files.length >= 22, `only ${files.length} fixtures found`);
});

for (const file of files) {
  const fx = fixture(file);
  test(`markdown/${file}: ${fx.name}`, () => {
    assert.deepEqual(parseMarkdown(fx.markdown), fx.blocks);
  });
}

test('every prefix of every fixture parses (the streaming model)', () => {
  for (const file of files) {
    const src = fixture(file).markdown;
    for (let i = 0; i <= src.length; i++) {
      const blocks = parseMarkdown(src.slice(0, i));
      assert.ok(Array.isArray(blocks), `${file} prefix ${i}`);
    }
  }
});

test('an 8 KiB reply parses in under 20 ms', () => {
  const unit = fixture('claude-reply.json').markdown;
  let src = '';
  while (src.length < 8192) src += unit + '\n\n';
  src = src.slice(0, 8192);
  parseMarkdown(src); // warm up the JIT
  const runs = 5;
  const start = performance.now();
  for (let i = 0; i < runs; i++) parseMarkdown(src);
  const each = (performance.now() - start) / runs;
  assert.ok(each < 20, `took ${each.toFixed(2)} ms`);
});

test('pathological input stays fast (unclosed delimiters, brackets, backticks)', () => {
  const evil = '*a _b [c `d '.repeat(800) + '\n' + '> '.repeat(200) + 'x\n' + '- '.repeat(200) + 'y\n';
  const start = performance.now();
  parseMarkdown(evil);
  assert.ok(performance.now() - start < 200, 'took too long');
});

test('soft breaks become line breaks when asked (user bubbles keep typed lines)', () => {
  assert.deepEqual(parseMarkdown('one\ntwo', { breaks: true }), [
    { type: 'paragraph', inlines: [{ t: 'text', v: 'one' }, { t: 'br' }, { t: 'text', v: 'two' }] },
  ]);
  assert.deepEqual(parseMarkdown('one\ntwo'), [{ type: 'paragraph', inlines: [{ t: 'text', v: 'one two' }] }]);
});

test('empty and whitespace-only text is an empty tree', () => {
  assert.deepEqual(parseMarkdown(''), []);
  assert.deepEqual(parseMarkdown('  \n\n \t\n'), []);
});

test('no new module writes HTML from strings', () => {
  for (const f of ['markdown.mjs', 'references.mjs', 'render.mjs', 'chatView.mjs']) {
    const src = readFileSync(new URL(f, import.meta.url), 'utf8');
    for (const bad of ['innerHTML', 'outerHTML', 'insertAdjacentHTML', 'document.write']) {
      assert.ok(!src.includes(bad), `${f} uses ${bad}`);
    }
  }
});

// Hostile input: deep nesting must not overflow the stack, and nothing may be quadratic, since a
// streaming reply is parsed again on every update. Each case: no throw, under 100 ms.
// Best of five timed runs after a warm-up, so one slow scheduling slice does not fail a test.
export function best(fn, runs = 5) {
  let out;
  out = fn();
  let ms = Infinity;
  for (let i = 0; i < runs; i++) {
    const start = performance.now();
    out = fn();
    ms = Math.min(ms, performance.now() - start);
  }
  return { ms, out };
}
const timed = (src) => best(() => parseMarkdown(src));
const LIMIT = 500; // ms, an absolute bound far above the expected few ms; linearity is checked apart

const DEEP = {
  'lists 10,000 deep': '- '.repeat(10000) + 'y',
  'quotes 10,000 deep': '> '.repeat(10000) + 'x',
  'quotes and lists mixed 5,000 deep': '> - '.repeat(5000) + 'z',
  'ordered lists 10,000 deep': '1. '.repeat(10000) + 'n',
  'emphasis 10,000 deep': '**a '.repeat(10000) + 'b**'.repeat(10000),
  'em, strong and del 10,000 deep': '*~~**a '.repeat(10000) + 'b**~~*'.repeat(10000),
  'images 10,000 deep': '![x'.repeat(10000) + '](https://e.x/i.png)'.repeat(10000),
};
for (const [name, src] of Object.entries(DEEP)) {
  test(`deep nesting does not throw and stays fast: ${name}`, () => {
    const { ms, out } = timed(src);
    assert.ok(Array.isArray(out));
    assert.ok(ms < LIMIT, `took ${ms.toFixed(1)} ms`);
  });
}

test('containers nest at most 100 deep; deeper markers are text', () => {
  let depth = 0;
  let b = parseMarkdown('> '.repeat(150) + 'x')[0];
  while (b.type === 'quote') { depth++; b = b.blocks[0]; }
  assert.equal(depth, 100);
  assert.equal(b.type, 'paragraph');
  assert.equal(b.inlines[0].v, '> '.repeat(50) + 'x');
  depth = 0;
  let l = parseMarkdown('- '.repeat(150) + 'y')[0];
  while (l && l.type === 'list') { depth++; l = l.items[0].blocks[0]; }
  assert.equal(depth, 100);
});

const LINEAR = {
  'many unclosed link destinations': '[a]('.repeat(12000),
  'a URL with many closing parentheses': 'https://x.test/' + ')'.repeat(60000),
  'many soft breaks after delimiters': '*a\n'.repeat(40000),
  'many emphasis pairs': '*a* '.repeat(15000),
  'many strong pairs and code': '**a** `b` '.repeat(8000),
  'many unclosed brackets, then many links': '['.repeat(20000) + '[a](https://b.test)'.repeat(4000),
  'many unmatched backtick runs': '` `` ``` '.repeat(8000),
  'many autolink-looking angle brackets': '<a'.repeat(30000),
  'many entities': '&amp;&#35;&nope; '.repeat(5000),
  'many table rows': '| a | b |\n|---|---|\n' + '| 1 | 2 |\n'.repeat(8000),
  'many list items': '- item\n'.repeat(12000),
  'a long paragraph': 'word '.repeat(20000),
};
for (const [name, src] of Object.entries(LINEAR)) {
  test(`linear time (${(src.length / 1024).toFixed(0)} KB): ${name}`, () => {
    const { ms } = timed(src);
    assert.ok(ms < LIMIT, `took ${ms.toFixed(1)} ms`);
  });
}

// Linearity: four times the input takes less than ten times as long (a quadratic path takes
// sixteen). The base size keeps the smaller run well above timer and GC noise.
const GROWTH = {
  'soft breaks after delimiters': (n) => '*a\n'.repeat(n),
  'emphasis pairs': (n) => '*a* '.repeat(n),
  'nested emphasis': (n) => '**a '.repeat(n) + 'b**'.repeat(n),
  'unclosed link destinations': (n) => '[a]('.repeat(n),
  'lists': (n) => '- item\n'.repeat(n),
};
for (const [name, make] of Object.entries(GROWTH)) {
  test(`linear growth: ${name}`, () => {
    const n = 20000;
    const small = timed(make(n)).ms, large = timed(make(4 * n)).ms;
    assert.ok(large < 10 * Math.max(small, 2), `n: ${small.toFixed(2)} ms, 4n: ${large.toFixed(2)} ms`);
  });
}
