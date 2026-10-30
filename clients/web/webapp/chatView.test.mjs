import { test } from 'node:test';
import assert from 'node:assert/strict';

// A small DOM: elements with children, a class, attributes and text. chatView.mjs builds nodes,
// never HTML, so this is enough to read back what a bubble draws.
function fakeDocument() {
  const el = (tag) => ({
    tagName: tag.toUpperCase(), children: [], attrs: {}, className: '', dir: '', _text: '',
    append(...xs) { for (const x of xs) this.children.push(typeof x === 'string' ? { textContent: x, children: [] } : x); },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    addEventListener() {},
    get textContent() { return this._text + this.children.map((c) => c.textContent).join(''); },
    set textContent(v) { this.children = []; this._text = String(v); },
  });
  return { createElement: el, createElementNS: (_, tag) => el(tag) };
}
globalThis.document = fakeDocument();
const { userItemNode } = await import('./chatView.mjs');

function all(node, pred, out = []) {
  if (pred(node)) out.push(node);
  for (const c of node.children || []) all(c, pred, out);
  return out;
}
const byClass = (node, c) => all(node, (n) => typeof n.className === 'string' && n.className.split(' ').includes(c));

test('a slash command shows its name in monospace and its args as plain text, not its tags', () => {
  const row = userItemNode('<command-name>/tidy</command-name>\n<command-message>tidy</command-message>\n<command-args>make it **short**</command-args>');
  assert.ok(!row.textContent.includes('<command'), row.textContent);
  const [name] = byClass(row, 'bubble-command-name');
  assert.equal(name.tagName, 'CODE');
  assert.equal(name.textContent, '/tidy');
  const [args] = byClass(row, 'bubble-command-args');
  assert.equal(args.textContent, 'make it **short**', 'args are not Markdown');
});

test('output is a plain monospace block; error output carries a label', () => {
  const ok = userItemNode('<local-command-stdout><b>x</b>\nline two</local-command-stdout>');
  const [pre] = byClass(ok, 'bubble-output-text');
  assert.equal(pre.tagName, 'PRE');
  assert.equal(pre.textContent, '<b>x</b>\nline two');
  assert.equal(byClass(ok, 'bubble-output-label').length, 0);
  const err = userItemNode('<bash-stdout></bash-stdout><bash-stderr>no such file</bash-stderr>');
  assert.equal(byClass(err, 'bubble-output-label')[0].textContent, 'Error output');
  assert.equal(byClass(err, 'bubble-output-text')[0].textContent, 'no such file');
});

test('parts keep their order next to text; nothing left shows no bubble', () => {
  const row = userItemNode('first\n<bash-input>make</bash-input>\nlast');
  const order = all(row, (n) => ['md-plain', 'bubble-command'].some((c) => (n.className || '').split(' ').includes(c)) || n.tagName === 'P')
    .map((n) => n.textContent);
  assert.deepEqual(order.filter((t) => t), ['first', '!make', 'last']);
  assert.equal(userItemNode('<local-command-caveat>generated</local-command-caveat>'), null);
  assert.equal(userItemNode('<bash-stdout> </bash-stdout><bash-stderr></bash-stderr>'), null);
});
