import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { linkReferences, mediaCards, referenceContext } from './references.mjs';
import { parseMarkdown } from './markdown.mjs';
import { bubbleBlocks, formatsAsMarkdown, MAX_FORMAT_CHARS, safeHref } from './render.mjs';

const dir = new URL('../../../api/testdata/clients/references/', import.meta.url);
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort();

test('the references fixture folder is not empty', () => {
  assert.ok(files.length >= 7, `only ${files.length} fixtures found`);
});

for (const file of files) {
  const fx = JSON.parse(readFileSync(new URL(file, dir), 'utf8'));
  for (const c of fx.cases) {
    test(`references/${file}: ${c.name}`, () => {
      const blocks = [{ type: 'paragraph', inlines: c.inlines }];
      assert.deepEqual(linkReferences(blocks, c.context), [{ type: 'paragraph', inlines: c.expect }]);
    });
  }
}

const mediaDir = new URL('../../../api/testdata/clients/media/', import.meta.url);
const mediaFiles = readdirSync(mediaDir).filter((f) => f.endsWith('.json')).sort();
const noContext = { repo: null, jiraBase: null, jiraProjects: [], project: null };

test('the media fixture folder is not empty', () => {
  assert.ok(mediaFiles.length >= 2, `only ${mediaFiles.length} fixtures found`);
});

for (const file of mediaFiles) {
  const fx = JSON.parse(readFileSync(new URL(file, mediaDir), 'utf8'));
  for (const c of fx.cases) {
    test(`media/${file}: ${c.name}`, () => {
      assert.deepEqual(mediaCards(linkReferences(parseMarkdown(c.markdown), noContext)), c.blocks);
    });
  }
}

test('media cards leave code blocks, tables and rules alone and keep the input unchanged', () => {
  const input = [
    { type: 'code', lang: null, text: 'https://example.com/a.mp4' },
    { type: 'table', align: ['left'], header: [[{ t: 'link', href: 'https://example.com/a.mp4', c: [{ t: 'text', v: 'x' }] }]], rows: [] },
    { type: 'rule' },
  ];
  const copy = JSON.parse(JSON.stringify(input));
  assert.deepEqual(mediaCards(input), copy);
  const para = [{ type: 'paragraph', inlines: [{ t: 'link', href: 'https://youtu.be/x', c: [{ t: 'text', v: 'https://youtu.be/x' }] }] }];
  mediaCards(para);
  assert.equal(para[0].type, 'paragraph');
});

const ctx = {
  repo: { web: 'https://github.com/acme/widget', branch: 'main', root: '/r' },
  jiraBase: null, jiraProjects: [], project: '/r',
};

test('the pass reaches headings, table cells, list items and quotes, never code blocks', () => {
  const t = (v) => [{ t: 'text', v }];
  const ref = { t: 'ref', kind: 'pr', v: '#7', href: 'https://github.com/acme/widget/issues/7' };
  const got = linkReferences([
    { type: 'heading', level: 2, inlines: t('#7') },
    { type: 'table', align: ['left'], header: [t('#7')], rows: [[t('#7')]] },
    { type: 'list', ordered: false, start: null, items: [{ task: null, blocks: [{ type: 'paragraph', inlines: t('#7') }] }] },
    { type: 'quote', blocks: [{ type: 'paragraph', inlines: t('#7') }] },
    { type: 'code', lang: null, text: '#7' },
  ], ctx);
  assert.deepEqual(got, [
    { type: 'heading', level: 2, inlines: [ref] },
    { type: 'table', align: ['left'], header: [[ref]], rows: [[[ref]]] },
    { type: 'list', ordered: false, start: null, items: [{ task: null, blocks: [{ type: 'paragraph', inlines: [ref] }] }] },
    { type: 'quote', blocks: [{ type: 'paragraph', inlines: [ref] }] },
    { type: 'code', lang: null, text: '#7' },
  ]);
});

test('referenceContext reads the session detail and health links', () => {
  assert.deepEqual(referenceContext({ project: '/p', repo: { web: 'https://github.com/a/b', branch: 'x' } },
    { links: { jiraBase: 'https://j', jiraProjects: ['A'] } }),
  { repo: { web: 'https://github.com/a/b', branch: 'x' }, jiraBase: 'https://j', jiraProjects: ['A'], project: '/p' });
  assert.deepEqual(referenceContext(null, null), { repo: null, jiraBase: null, jiraProjects: [], project: null });
  assert.deepEqual(referenceContext({ project: '' }, {}), { repo: null, jiraBase: null, jiraProjects: [], project: null });
});

test('safeHref keeps only http and https', () => {
  assert.equal(safeHref('https://x.test/a'), 'https://x.test/a');
  assert.equal(safeHref('HTTP://x.test'), 'HTTP://x.test');
  assert.equal(safeHref('javascript:alert(1)'), null);
  assert.equal(safeHref(' https://x'), null);
  assert.equal(safeHref('data:text/html,x'), null);
  assert.equal(safeHref(undefined), null);
});

test('bubbleBlocks keeps a user message inline-only', () => {
  const p = (v) => ({ type: 'paragraph', inlines: [{ t: 'text', v }] });
  const got = bubbleBlocks([
    { type: 'heading', level: 1, inlines: [{ t: 'text', v: 'Title' }] },
    { type: 'code', lang: 'go', text: 'x := 1\ny := 2' },
    { type: 'list', ordered: true, start: 3, items: [
      { task: null, blocks: [p('a')] },
      { task: true, blocks: [p('b'), { type: 'list', ordered: false, start: null, items: [{ task: null, blocks: [p('c')] }] }] },
      { task: null, blocks: [] },
    ] },
    { type: 'quote', blocks: [p('q')] },
    { type: 'table', align: ['left', 'left'], header: [[{ t: 'text', v: 'h1' }], []], rows: [[[{ t: 'code', v: 'c' }], [{ t: 'text', v: 'd' }]]] },
    { type: 'rule' },
  ]);
  assert.deepEqual(got, [
    { type: 'paragraph', inlines: [{ t: 'text', v: 'Title' }] },
    { type: 'paragraph', code: true, inlines: [{ t: 'code', v: 'x := 1\ny := 2' }] },
    { type: 'paragraph', inlines: [{ t: 'text', v: '3. a' }] },
    { type: 'paragraph', inlines: [{ t: 'text', v: '4. [x] b' }] },
    { type: 'paragraph', inlines: [{ t: 'text', v: '• c' }] },
    { type: 'paragraph', inlines: [{ t: 'text', v: '5.' }] },
    { type: 'paragraph', inlines: [{ t: 'text', v: '> q' }] },
    { type: 'paragraph', inlines: [{ t: 'text', v: 'h1 | ' }] },
    { type: 'paragraph', inlines: [{ t: 'code', v: 'c' }, { t: 'text', v: ' | d' }] },
  ]);
});

test('reference hrefs encode each path segment and the branch, keeping its slashes', () => {
  const c = { repo: { web: 'https://github.com/acme/widget', branch: 'feat/a#b', root: '/r' }, jiraBase: null, jiraProjects: [], project: '/r' };
  const [, ref] = linkReferences([{ type: 'paragraph', inlines: [{ t: 'text', v: 'see src/a@b+c.go:3' }] }], c)[0].inlines;
  assert.equal(ref.href, 'https://github.com/acme/widget/blob/feat/a%23b/src/a%40b%2Bc.go#L3');
});

const timedRefs = (v) => {
  const blocks = [{ type: 'paragraph', inlines: [{ t: 'text', v }] }];
  linkReferences(blocks, ctx);
  let ms = Infinity;
  for (let i = 0; i < 5; i++) {
    const start = performance.now();
    linkReferences(blocks, ctx);
    ms = Math.min(ms, performance.now() - start);
  }
  return ms;
};
for (const [name, v] of Object.entries({
  'many issue numbers': 'see #1 '.repeat(15000),
  'many issue numbers after one long word': 'x'.repeat(50000) + ' #1'.repeat(15000),
  'many look-alike commits and paths': 'abc1234 src/a.go deadbeef x/y '.repeat(3000),
  'one long path-like run': 'a/'.repeat(40000) + 'b.go',
  'many repo references': 'acme/widget#12 '.repeat(6000),
})) {
  test(`references are linear (${(v.length / 1024).toFixed(0)} KB): ${name}`, () => {
    const ms = timedRefs(v);
    assert.ok(ms < 500, `took ${ms.toFixed(1)} ms`);
  });
}

test('no argument-spread overflow: 200 KB of issue numbers (over 100k pieces)', () => {
  const v = ' #1'.repeat(70000);
  const out = linkReferences([{ type: 'paragraph', inlines: [{ t: 'text', v }] }], ctx)[0].inlines;
  assert.equal(out.length, 140000);
  assert.equal(out.map((x) => x.v).join(''), v);
});

test('no argument-spread overflow: bubbleBlocks with 820 KB of quoted paragraphs and table cells', () => {
  const paras = Array.from({ length: 200000 }, () => ({ type: 'paragraph', inlines: [{ t: 'text', v: 'abc' }] }));
  const got = bubbleBlocks([{ type: 'quote', blocks: paras }]);
  assert.equal(got.length, 200000);
  assert.deepEqual(got[0], { type: 'paragraph', inlines: [{ t: 'text', v: '> abc' }] });
  const cells = Array.from({ length: 200000 }, () => [{ t: 'code', v: 'c' }]);
  const row = bubbleBlocks([{ type: 'table', align: cells.map(() => 'left'), header: cells, rows: [] }]);
  assert.equal(row[0].inlines.length, 2 * 200000 - 1);
});

test('text over 256 KiB is not parsed: it shows as plain preformatted text', () => {
  assert.equal(MAX_FORMAT_CHARS, 256 * 1024);
  assert.equal(formatsAsMarkdown('# hi'), true);
  assert.equal(formatsAsMarkdown('x'.repeat(MAX_FORMAT_CHARS)), true);
  assert.equal(formatsAsMarkdown('x'.repeat(MAX_FORMAT_CHARS + 1)), false);
});

// Thoughts, plan entries, step targets and errors: inline-only Markdown with references.
const repoCtx = { repo: { web: 'https://github.com/acme/widget', branch: 'main', root: '/Users/me/widget' }, jiraBase: null, jiraProjects: [], project: '/Users/me/widget' };

test('inlineBlocks: inline-only Markdown with references and typed lines', async () => {
  const { inlineBlocks } = await import('./render.mjs');
  assert.deepEqual(inlineBlocks('Check `retryAfter` in *src/stream.go*\nthen [the docs](https://example.com/d)', repoCtx), [
    { type: 'paragraph', inlines: [
      { t: 'text', v: 'Check ' }, { t: 'code', v: 'retryAfter' }, { t: 'text', v: ' in ' },
      { t: 'em', c: [{ t: 'ref', kind: 'path', v: 'src/stream.go', href: 'https://github.com/acme/widget/blob/main/src/stream.go' }] },
      { t: 'br' }, { t: 'text', v: 'then ' }, { t: 'link', href: 'https://example.com/d', c: [{ t: 'text', v: 'the docs' }] },
    ] },
  ]);
  assert.deepEqual(inlineBlocks('- a\n- b', null), [
    { type: 'paragraph', inlines: [{ t: 'text', v: '• a' }] }, { type: 'paragraph', inlines: [{ t: 'text', v: '• b' }] },
  ], 'blocks become paragraphs');
  assert.deepEqual(inlineBlocks('', repoCtx), []);
});

test('stepInlines: a target that is a path is that path reference, not Markdown', async () => {
  const { stepInlines } = await import('./render.mjs');
  const href = (p) => 'https://github.com/acme/widget/blob/main/' + p;
  assert.deepEqual(stepInlines('pkg/__init__.py', repoCtx), [{ t: 'ref', kind: 'path', v: 'pkg/__init__.py', href: href('pkg/__init__.py') }]);
  assert.deepEqual(stepInlines('/Users/me/widget/src/a.go:12', repoCtx), [{ t: 'ref', kind: 'path', v: '/Users/me/widget/src/a.go:12', href: href('src/a.go') + '#L12' }]);
  assert.deepEqual(stepInlines('pkg/__init__.py', null), [{ t: 'text', v: 'pkg/__init__.py' }], 'no repo: the target as typed');
  assert.deepEqual(stepInlines('**/*_test.go', repoCtx), [{ t: 'text', v: '**/*_test.go' }], 'a pattern stays as typed');
  assert.deepEqual(stepInlines('retryAfter', repoCtx), [{ t: 'text', v: 'retryAfter' }]);
});

test('stepInlines: other targets are inline Markdown on one line', async () => {
  const { stepInlines } = await import('./render.mjs');
  assert.deepEqual(stepInlines('Read `src/a.go`', repoCtx), [
    { t: 'text', v: 'Read ' }, { t: 'ref', kind: 'path', v: 'src/a.go', href: 'https://github.com/acme/widget/blob/main/src/a.go', code: true },
  ]);
  assert.deepEqual(stepInlines('go test ./...\n- then vet', null), [{ t: 'text', v: 'go test ./... • then vet' }], 'lines and blocks join with spaces');
  assert.deepEqual(stepInlines('`ls -la`', null), [{ t: 'code', v: 'ls -la' }]);
  assert.deepEqual(stepInlines('', repoCtx), []);
});

// A minimal DOM for render.mjs: elements with attributes, children, text and click listeners.
function fakeDocument() {
  const el = (tag) => {
    const e = {
      tagName: tag, children: [], attrs: {}, listeners: {}, className: '', _text: '',
      append(...xs) { for (const x of xs) this.children.push(typeof x === 'string' ? { text: x } : x); },
      setAttribute(k, v) { this.attrs[k] = String(v); },
      getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
      removeAttribute(k) { delete this.attrs[k]; },
      addEventListener(k, f) { this.listeners[k] = f; },
      get textContent() { return this._text + this.children.map((c) => c.textContent ?? c.text ?? '').join(''); },
      set textContent(v) { this._text = v; this.children = []; },
    };
    return e;
  };
  return { createElement: el, createElementNS: (_, tag) => el(tag), createDocumentFragment: () => el('#fragment') };
}

function find(node, pred) {
  if (pred(node)) return node;
  for (const c of node.children || []) { const f = find(c, pred); if (f) return f; }
  return null;
}

async function rendered(markdown) {
  globalThis.document = fakeDocument();
  const { renderBlocks } = await import('./render.mjs');
  return renderBlocks(mediaCards(linkReferences(parseMarkdown(markdown), noContext)));
}

test('code blocks and tables are labelled scrollable regions', async () => {
  const root = await rendered('```\nx = 1\n```\n\n| a | b |\n| - | - |\n| 1 | 2 |');
  const pre = find(root, (n) => n.tagName === 'pre');
  assert.equal(pre.tabIndex, 0);
  assert.equal(pre.getAttribute('role'), 'region');
  assert.equal(pre.getAttribute('aria-label'), 'Code');
  const wrap = find(root, (n) => n.className === 'md-table');
  assert.equal(wrap.tabIndex, 0);
  assert.equal(wrap.getAttribute('role'), 'region');
  assert.equal(wrap.getAttribute('aria-label'), 'Table');
});

test('the copy button says Copied to a screen reader while it shows it, then restores', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  Object.defineProperty(globalThis, 'navigator', { value: { clipboard: { writeText: async () => {} } }, configurable: true });
  const root = await rendered('```\nx = 1\n```');
  const b = find(root, (n) => n.className === 'md-copy');
  assert.equal(b.getAttribute('aria-label'), 'Copy code');
  await b.listeners.click();
  assert.equal(b.textContent, 'Copied');
  assert.equal(b.getAttribute('aria-label'), 'Copied');
  t.mock.timers.tick(1500);
  assert.equal(b.textContent, 'Copy');
  assert.equal(b.getAttribute('aria-label'), 'Copy code');
});

test('a failed copy is announced too', async () => {
  Object.defineProperty(globalThis, 'navigator', { value: { clipboard: { writeText: async () => { throw new Error('denied'); } } }, configurable: true });
  const root = await rendered('```\nx\n```');
  const b = find(root, (n) => n.className === 'md-copy');
  await b.listeners.click();
  assert.equal(b.getAttribute('aria-label'), 'Could not copy');
});

test('a media card names the host the tap goes to', async () => {
  const root = await rendered('[clip](https://Media.Example:8443/a.mp4)');
  const host = find(root, (n) => n.className === 'md-media-host');
  assert.equal(host.textContent, 'media.example');
  const card = find(root, (n) => n.className === 'md-media md-media-video');
  assert.equal(card.href, 'https://Media.Example:8443/a.mp4');
});

// Step detail: a step opens to its command, its error and an edit's lines (render.mjs stepDetail,
// chatView.mjs timelineNodes).
const findAll = (node, pred, out = []) => {
  if (pred(node)) out.push(node);
  for (const c of node.children || []) findAll(c, pred, out);
  return out;
};
const hasClass = (cls) => (n) => typeof n.className === 'string' && n.className.split(' ').includes(cls);
const detail = (d) => ({ input: '', error: '', old: '', new: '', ...d });

test('stepDetail: nothing to open when all four strings are empty', async () => {
  globalThis.document = fakeDocument();
  const { stepDetail } = await import('./render.mjs');
  assert.equal(stepDetail({ kind: 'run', target: 'ls', status: 'ok', detail: detail({}) }), null);
  assert.equal(stepDetail({ kind: 'run', target: 'ls', status: 'ok' }), null, 'no detail at all');
});

test('stepDetail: a command in a left-to-right pre after a dimmed $, hostile text as text', async () => {
  globalThis.document = fakeDocument();
  const { stepDetail } = await import('./render.mjs');
  const cmd = '<img src=x onerror=alert(1)>';
  const node = stepDetail({ kind: 'run', target: 'x', status: 'ok', detail: detail({ input: cmd }) });
  const pre = find(node, (n) => n.tagName === 'pre');
  assert.equal(pre.dir, 'ltr');
  assert.equal(pre.children[0].tagName, 'span');
  assert.equal(pre.children[0].className, 'dim');
  assert.equal(pre.children[0].textContent.trim(), '$');
  assert.equal(pre.textContent, '$ ' + cmd);
  assert.equal(find(node, (n) => n.tagName === 'img'), null, 'no img element');
});

test('stepDetail: an input that is not a command has no $', async () => {
  globalThis.document = fakeDocument();
  const { stepDetail } = await import('./render.mjs');
  const node = stepDetail({ kind: 'search', target: 'x', status: 'ok', detail: detail({ input: 'TODO' }) });
  const pre = find(node, (n) => n.tagName === 'pre');
  assert.equal(pre.textContent, 'TODO');
  assert.equal(pre.dir, 'ltr');
});

test('stepDetail: the error sits under "Failed" in the failed colour', async () => {
  globalThis.document = fakeDocument();
  const { stepDetail } = await import('./render.mjs');
  const node = stepDetail({ kind: 'run', target: 'make', status: 'failed', detail: detail({ input: 'make', error: 'boom\nError 1' }) });
  const box = find(node, hasClass('failed'));
  assert.ok(box, 'a block with class failed');
  assert.ok(box.textContent.startsWith('Failed'), box.textContent);
  assert.ok(box.textContent.includes('boom\nError 1'));
});

test('stepDetail: an edit shows removed then added lines with - and + marks, whitespace kept', async () => {
  globalThis.document = fakeDocument();
  const { stepDetail } = await import('./render.mjs');
  const node = stepDetail({ kind: 'edit', target: 'a.go', status: 'ok', detail: detail({ old: '\ta\nb\n', new: '\tc\n' }) });
  const lines = findAll(node, (n) => hasClass('del')(n) || hasClass('add')(n));
  assert.deepEqual(lines.map((n) => n.className.split(' ').includes('del') ? 'del' : 'add'), ['del', 'del', 'add']);
  assert.deepEqual(lines.map((n) => find(n, hasClass('diff-mark')).textContent + find(n, hasClass('diff-text')).textContent), ['-\ta', '-b', '+\tc']);
  const diff = find(node, hasClass('diff'));
  assert.equal(diff.dir, 'ltr');
  assert.equal(find(node, (n) => n.tagName === 'pre'), null, 'no input: none given');
  const same = stepDetail({ kind: 'edit', target: '/a.go', status: 'ok', detail: detail({ input: '/a.go', old: 'x\n' }) });
  assert.equal(find(same, (n) => n.tagName === 'pre'), null, 'an edit path the line already shows is not repeated');
  const other = stepDetail({ kind: 'edit', target: 'Edit a.go', status: 'ok', detail: detail({ input: '/src/a.go', old: 'x\n' }) });
  assert.equal(find(other, (n) => n.tagName === 'pre').textContent, '/src/a.go', 'a fuller path is shown');
});

test('stepDetail: 60 diff lines show 40, then "20 more lines"', async () => {
  globalThis.document = fakeDocument();
  const { stepDetail } = await import('./render.mjs');
  const many = (c, n) => Array.from({ length: n }, (_, i) => c + i).join('\n') + '\n';
  const node = stepDetail({ kind: 'edit', target: 'a.go', status: 'ok', detail: detail({ old: many('o', 30), new: many('n', 30) }) });
  const lines = findAll(node, (n) => hasClass('del')(n) || hasClass('add')(n));
  assert.equal(lines.length, 40);
  assert.equal(lines.filter(hasClass('del')).length, 30, 'removed lines first');
  const more = find(node, hasClass('diff-more'));
  assert.equal(more.textContent, '20 more lines');
  const exact = stepDetail({ kind: 'edit', target: 'a.go', status: 'ok', detail: detail({ old: many('o', 20), new: many('n', 20) }) });
  assert.equal(find(exact, hasClass('diff-more')), null, 'exactly 40: no more line');
  const one = stepDetail({ kind: 'edit', target: 'a.go', status: 'ok', detail: detail({ old: many('o', 21), new: many('n', 20) }) });
  assert.equal(find(one, hasClass('diff-more')).textContent, '1 more line');
});

test('stepDetail: live text is capped as drawn: 4000 code points of input, 2000 of error and of each diff line, then …', async () => {
  globalThis.document = fakeDocument();
  const { stepDetail } = await import('./render.mjs');
  const write = stepDetail({ kind: 'edit', target: 'a.txt', status: 'ok', detail: detail({ new: 'w'.repeat(10000) }) });
  const text = find(write, hasClass('diff-text')).textContent;
  assert.equal(text, 'w'.repeat(2000) + '…', 'a 10 000-character single-line Write');
  const run = stepDetail({ kind: 'search', target: 'x', status: 'failed', detail: detail({ input: 'i'.repeat(10000), error: 'e'.repeat(10000) }) });
  assert.equal(find(run, (n) => n.tagName === 'pre').textContent, 'i'.repeat(4000) + '…');
  assert.equal(find(run, hasClass('step-error-text')).textContent, 'e'.repeat(2000) + '…');
  const atCap = stepDetail({ kind: 'search', target: 'x', status: 'ok', detail: detail({ input: 'i'.repeat(4000), old: 'o'.repeat(2000) }) });
  assert.equal(find(atCap, (n) => n.tagName === 'pre').textContent, 'i'.repeat(4000), 'exactly at the cap: whole');
  assert.equal(find(atCap, hasClass('diff-text')).textContent, 'o'.repeat(2000));
  // Counted by code points: 2000 emoji (4000 UTF-16 units) is at the cap, one more is over it.
  const emoji = stepDetail({ kind: 'edit', target: 'a', status: 'ok', detail: detail({ old: '😀'.repeat(2000), new: '😀'.repeat(2001) }) });
  const texts = findAll(emoji, hasClass('diff-text')).map((n) => n.textContent);
  assert.deepEqual(texts, ['😀'.repeat(2000), '😀'.repeat(2000) + '…']);
});

test('stepDetail: each diff line tells a screen reader it was removed or added; the mark is hidden', async () => {
  globalThis.document = fakeDocument();
  const { stepDetail } = await import('./render.mjs');
  const node = stepDetail({ kind: 'edit', target: 'a.go', status: 'ok', detail: detail({ old: 'a\n', new: 'b\n' }) });
  const lines = findAll(node, hasClass('diff-line'));
  const hidden = lines.map((l) => find(l, hasClass('visually-hidden')));
  assert.deepEqual(hidden.map((n) => n && n.textContent), ['Removed: ', 'Added: ']);
  for (const l of lines) assert.equal(find(l, hasClass('diff-mark')).getAttribute('aria-hidden'), 'true');
  assert.deepEqual(lines.map((l) => find(l, hasClass('diff-text')).textContent), ['a', 'b']);
});

function stepsCtx(over = {}) {
  return {
    refs: null, working: false, openSteps: new Set([0]), openStepDetails: new Set(), openThoughts: new Set(),
    answered: new Set(), answering: new Set(), answerErrors: new Map(), chosen: new Map(),
    onToggleSteps() {}, onToggleThought() {}, onToggleStepDetail() {}, onAnswer() {}, ...over,
  };
}
const stepsItem = (steps) => ({ kind: 'steps', count: steps.length, seconds: null, filesChanged: [], steps });

test('a step with detail is a button row that opens; one without is a plain row', async () => {
  globalThis.document = fakeDocument();
  const { timelineNodes } = await import('./chatView.mjs');
  const items = [stepsItem([
    { kind: 'run', target: 'go test', status: 'ok', detail: detail({ input: 'go test ./... -count=1' }) },
    { kind: 'read', target: 'a.go', status: 'ok', detail: detail({}) },
  ])];
  let toggled = null;
  const closed = timelineNodes(items, stepsCtx({ onToggleStepDetail: (k) => { toggled = k; } }))[0];
  const rows = findAll(closed, hasClass('step'));
  assert.equal(rows.length, 2);
  assert.equal(rows[0].tagName, 'button');
  assert.equal(rows[0].type, 'button');
  assert.equal(rows[0].getAttribute('aria-expanded'), 'false');
  assert.equal(rows[1].tagName, 'div', 'nothing to open: not a button');
  assert.equal(rows[1].getAttribute('aria-expanded'), null);
  assert.equal(find(closed, hasClass('step-detail')), null, 'closed shows no detail');
  rows[0].listeners.click();
  assert.equal(toggled, '0:0', 'keyed by group and step index');

  const open = timelineNodes(items, stepsCtx({ openStepDetails: new Set(['0:0']) }))[0];
  const row = findAll(open, hasClass('step'))[0];
  assert.equal(row.getAttribute('aria-expanded'), 'true');
  assert.ok(find(open, hasClass('step-detail')).textContent.includes('go test ./... -count=1'));
});

test('a failed step takes the failed class on its line', async () => {
  globalThis.document = fakeDocument();
  const { timelineNodes } = await import('./chatView.mjs');
  const node = timelineNodes([stepsItem([{ kind: 'run', target: 'make', status: 'failed', detail: detail({}) }])], stepsCtx())[0];
  const row = find(node, hasClass('step'));
  assert.ok(row.className.split(' ').includes('failed'), row.className);
});

test('an openable step with a path target has no link inside its button; the open detail offers it', async () => {
  globalThis.document = fakeDocument();
  const { timelineNodes } = await import('./chatView.mjs');
  const items = [stepsItem([{ kind: 'edit', target: 'src/a.go', status: 'ok', detail: detail({ old: 'x\n', new: 'y\n' }) }])];
  const closed = timelineNodes(items, stepsCtx({ refs: repoCtx }))[0];
  const button = find(closed, (n) => n.tagName === 'button' && hasClass('step')(n));
  assert.equal(find(button, (n) => n.tagName === 'a'), null, 'no a inside the button');
  assert.equal(find(button, hasClass('step-target')).textContent, 'src/a.go', 'the target as plain text');
  assert.equal(button.getAttribute('data-step-key'), '0:0', 'the key, to give focus back after a render');
  const open = timelineNodes(items, stepsCtx({ refs: repoCtx, openStepDetails: new Set(['0:0']) }))[0];
  const openButton = find(open, (n) => n.tagName === 'button' && hasClass('step')(n));
  assert.equal(find(openButton, (n) => n.tagName === 'a'), null, 'still no a inside the open button');
  const links = findAll(find(open, hasClass('step-detail')), (n) => n.tagName === 'a');
  assert.equal(links.length, 1, 'one link in the detail');
  assert.equal(links[0].href, 'https://github.com/acme/widget/blob/main/src/a.go');
  assert.equal(links[0].textContent, 'src/a.go');
  assert.equal(links[0].rel, 'noopener noreferrer');
});

test('an openable step whose target resolves to no link shows no link line', async () => {
  globalThis.document = fakeDocument();
  const { timelineNodes } = await import('./chatView.mjs');
  const items = [stepsItem([{ kind: 'run', target: 'Read `src/a.go` now', status: 'ok', detail: detail({ input: 'cat src/a.go' }) }])];
  const none = timelineNodes(items, stepsCtx({ openStepDetails: new Set(['0:0']) }))[0];
  assert.equal(find(none, (n) => n.tagName === 'a'), null, 'no refs context: no link anywhere');
  const withRefs = timelineNodes(items, stepsCtx({ refs: repoCtx, openStepDetails: new Set(['0:0']) }))[0];
  const button = find(withRefs, (n) => n.tagName === 'button' && hasClass('step')(n));
  assert.equal(find(button, (n) => n.tagName === 'a'), null, 'an inline reference is flattened in the button');
  assert.ok(find(button, (n) => n.tagName === 'code'), 'its code look stays');
  assert.equal(findAll(withRefs, (n) => n.tagName === 'a').length, 1, 'and offered once in the detail');
});

test('a step that cannot open keeps its path link on the line', async () => {
  globalThis.document = fakeDocument();
  const { timelineNodes } = await import('./chatView.mjs');
  const node = timelineNodes([stepsItem([{ kind: 'read', target: 'src/a.go', status: 'ok', detail: detail({}) }])], stepsCtx({ refs: repoCtx }))[0];
  assert.equal(findAll(node, (n) => n.tagName === 'a').length, 1);
  assert.equal(find(node, (n) => n.tagName === 'button' && hasClass('step')(n)), null);
});
