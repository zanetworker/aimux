import { test } from 'node:test';
import assert from 'node:assert/strict';
import { NowModel } from './nowModel.mjs';
import { renderNow } from './nowView.mjs';

// A small DOM: elements with a parent, children, attributes, listeners, focus and a textarea's
// value and selection. Enough for nowView.mjs, which builds nodes, never HTML.
function fakeDocument() {
  const doc = { activeElement: null };
  const el = (tag) => {
    const e = {
      tagName: tag.toUpperCase(), children: [], parentNode: null, attrs: {}, listeners: {}, className: '', id: '',
      hidden: false, disabled: false, dataset: {}, tabIndex: 0, value: '', selectionStart: 0, selectionEnd: 0, _text: '',
      append(...xs) { for (const x of xs) this.insertBefore(typeof x === 'string' ? text(x) : x, null); },
      insertBefore(x, ref) {
        if (x.parentNode) x.remove();
        const i = ref ? this.children.indexOf(ref) : -1;
        if (i < 0) this.children.push(x); else this.children.splice(i, 0, x);
        x.parentNode = this;
        return x;
      },
      remove() {
        if (!this.parentNode) return;
        if (contains(this, doc.activeElement)) doc.activeElement = null; // a detached node loses focus
        this.parentNode.children.splice(this.parentNode.children.indexOf(this), 1);
        this.parentNode = null;
      },
      replaceChildren(...xs) { for (const c of [...this.children]) c.remove(); this.append(...xs); },
      contains(x) { return contains(this, x); },
      setAttribute(k, v) { this.attrs[k] = String(v); },
      getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
      removeAttribute(k) { delete this.attrs[k]; },
      addEventListener(k, f) { (this.listeners[k] = this.listeners[k] || []).push(f); },
      focus() { doc.activeElement = this; },
      setSelectionRange(a, b) { this.selectionStart = a; this.selectionEnd = b; },
      get textContent() { return this._text + this.children.map((c) => c.textContent).join(''); },
      set textContent(v) { for (const c of [...this.children]) c.remove(); this._text = String(v); },
    };
    return e;
  };
  const text = (t) => ({ textContent: t, parentNode: null, children: [], remove() { const p = this.parentNode; if (p) { p.children.splice(p.children.indexOf(this), 1); this.parentNode = null; } } });
  doc.createElement = el;
  doc.createElementNS = (_, tag) => el(tag);
  return doc;
}
function contains(root, x) {
  for (let n = x; n; n = n.parentNode) if (n === root) return true;
  return false;
}
function all(node, pred, out = []) {
  if (pred(node)) out.push(node);
  for (const c of node.children || []) all(c, pred, out);
  return out;
}
const one = (node, pred) => all(node, pred)[0] || null;
const cls = (c) => (n) => typeof n.className === 'string' && n.className.split(' ').includes(c);
const fire = (node, type, props = {}) => {
  const ev = { type, key: '', shiftKey: false, isComposing: false, keyCode: 0, preventDefault() { this.prevented = true; }, ...props };
  for (const f of node.listeners[type] || []) f(ev);
  return ev;
};

const tick = () => new Promise((r) => setTimeout(r, 0));
const NOW = Date.parse('2026-10-31T12:00:00Z');
const iso = (s) => new Date(NOW - s * 1000).toISOString();

test('answering on a Now card notes the session as answered, so the Next bar skips it', async () => {
  globalThis.document = fakeDocument();
  const events = [
    { seq: 1, turn: 't', kind: 'turn_started', time: iso(60), data: { text: 'go' } },
    { seq: 2, turn: 't', kind: 'approval', time: iso(50), data: { approvalId: 'ap', title: 'Run it?', options: [{ optionId: 'ok', name: 'Allow', kind: 'allow_once' }] } },
  ];
  const api = async (method, path) => {
    if (path.startsWith('/v1/sessions?')) return { sessions: [{ id: 'a', provider: 'claude', title: 'A', project: '/src/a', state: 'busy', lastActive: iso(50) }] };
    if (path.includes('/events')) return { events };
    return {};
  };
  const model = new NowModel({ api, storage: { getItem: () => null, setItem: () => {} }, now: () => NOW });
  await model.refresh();
  const ui = { openCards: new Set(), menuFor: null, storage: {}, rerender() {}, refresh() {}, open() {}, announce() {}, conversations: () => [] };
  const root = document.createElement('div');
  root.append(...renderNow(model, ui).body);
  const allow = one(root, cls('primary'));
  assert.equal(allow.textContent, 'Allow');
  fire(allow, 'click');
  await tick();
  await tick();
  assert.deepEqual([...model.answeredSessions], ['a']);
});
