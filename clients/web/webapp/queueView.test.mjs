import { test } from 'node:test';
import assert from 'node:assert/strict';

// A small DOM: elements with a parent, children, attributes, listeners, focus and a textarea's
// value and selection. Enough for queueView.mjs, which builds nodes, never HTML.
function fakeDocument() {
  const doc = { activeElement: null };
  const el = (tag) => {
    const e = {
      tagName: tag.toUpperCase(), children: [], parentNode: null, attrs: {}, listeners: {}, className: '', id: '',
      hidden: false, disabled: false, tabIndex: 0, value: '', selectionStart: 0, selectionEnd: 0, _text: '',
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

const item = (id, text, state = 'waiting') => ({ id, text, state, created: '2026-03-02T09:01:00Z' });
const running = (...items) => ({ state: 'running', items });

async function setup({ touch = false, folded = false } = {}) {
  globalThis.document = fakeDocument();
  const { createQueueView } = await import('./queueView.mjs');
  const box = document.createElement('div');
  const errorNode = document.createElement('p');
  const lost = [];
  const lostFocus = [];
  const queues = [];
  const calls = [];
  const answers = {}; // kind -> function returning the answer (or a promise of it)
  const client = {};
  for (const kind of ['edit', 'move', 'remove', 'resume']) {
    client[kind] = async (...args) => { calls.push([kind, ...args]); return answers[kind] ? answers[kind](...args) : { ok: true, queue: null }; };
  }
  const view = createQueueView({
    box, errorNode, client: () => client, isTouch: () => touch,
    onEditLost: (text, hadFocus) => { lost.push(text); lostFocus.push(hadFocus); }, onQueue: (q) => queues.push(q),
    open: !folded,
  });
  return { view, box, errorNode, lost, lostFocus, queues, calls, answers };
}
const rows = (box) => all(box, (n) => n.tagName === 'LI');
const rowOf = (box, id) => rows(box).find((r) => contains(r, one(r, (n) => n.attrs['data-queue-focus'] === 'text:' + id || n.attrs['data-queue-focus'] === 'edit:' + id)));
const button = (box, key) => one(box, (n) => n.attrs['data-queue-focus'] === key);
const editor = (box) => one(box, cls('queue-edit'));

test('rows: an ordered list, labels, tools described by the text span; in-flight tools are placeholders', async () => {
  const { view, box } = await setup();
  view.update({ state: 'running', items: [item('q1', 'Run the linter', 'answering'), item('q2', 'Summarize'), item('q3', 'Ship it')] });
  assert.equal(one(box, (n) => n.tagName === 'OL') !== null, true);
  assert.equal(rows(box).length, 3);
  assert.deepEqual(all(box, cls('queue-state')).map((n) => n.textContent), ['Answer in progress', 'Waiting', 'Waiting']);
  const up = button(box, 'up:q2');
  const span = one(box, (n) => n.id === up.attrs['aria-describedby']);
  assert.equal(span.tagName, 'SPAN', 'aria-describedby names the text span, not the button');
  assert.equal(span.textContent, 'Summarize');
  assert.equal(up.disabled, true, 'the first waiting one cannot go up');
  assert.equal(button(box, 'down:q3').disabled, true);
  const inflight = rows(box)[0];
  const tools = all(inflight, cls('queue-tool'));
  assert.equal(tools.length, 3, 'placeholders keep the columns aligned');
  for (const t of tools) {
    assert.equal(t.attrs['aria-hidden'], 'true');
    assert.equal(t.tabIndex, -1);
    assert.ok(t.className.includes('placeholder'));
  }
});

test('a paused queue: Delete of the message in flight is a real button', async () => {
  const { view, box } = await setup();
  view.update({ state: 'paused', reason: 'No answer seen yet', items: [item('q1', 'Run it', 'answering'), item('q2', 'Next')] });
  const del = button(box, 'delete:q1');
  assert.ok(del && !del.className.includes('placeholder') && del.attrs['aria-hidden'] === undefined);
  assert.equal(one(box, cls('queue-line')).textContent, 'No answer seen yet');
  assert.ok(one(box, cls('queue-resume')));
});

test('opening the editor: focus in it, the caret at the end, once', async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'Rename the helper')));
  fire(button(box, 'text:q1'), 'click');
  const ed = editor(box);
  assert.equal(ed.value, 'Rename the helper');
  assert.equal(document.activeElement, ed);
  assert.equal(ed.selectionStart, 'Rename the helper'.length);
});

test('a queue event mid-edit keeps the editor node, its value and selection, and redraws the rest', async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'Rename the helper'), item('q2', 'Add a test')));
  fire(button(box, 'text:q1'), 'click');
  const ed = editor(box);
  ed.value = 'Rename the helper now';
  fire(ed, 'input');
  ed.setSelectionRange(3, 7);
  view.update(running(item('q1', 'Rename the helper'), item('q2', 'Add a test'), item('q3', 'Ship it')));
  assert.equal(editor(box), ed, 'the same node');
  assert.ok(contains(box, ed));
  assert.equal(ed.value, 'Rename the helper now');
  assert.deepEqual([ed.selectionStart, ed.selectionEnd], [3, 7]);
  assert.equal(document.activeElement, ed, 'focus never left it');
  assert.equal(rows(box).length, 3);
  assert.equal(rows(box)[0], ed.parentNode.tagName === 'LI' ? ed.parentNode : ed.parentNode.parentNode, 'still first');
});

test('a queue event while Save has focus leaves focus on Save', async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'Rename'), item('q2', 'More')));
  fire(button(box, 'text:q1'), 'click');
  const save = button(box, 'save:q1');
  save.focus();
  view.update(running(item('q1', 'Rename'), item('q2', 'More'), item('q3', 'Third')));
  assert.equal(document.activeElement, save);
});

test('a queue event while a row button has focus gives focus back to the same button', async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'a'), item('q2', 'b')));
  button(box, 'delete:q2').focus();
  view.update(running(item('q1', 'a'), item('q2', 'b'), item('q3', 'c')));
  assert.equal(document.activeElement, button(box, 'delete:q2'));
});

test('while an input method composes, no update touches the section; it applies at compositionend', async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'Rename')));
  fire(button(box, 'text:q1'), 'click');
  const ed = editor(box);
  fire(ed, 'compositionstart');
  view.update(running(item('q1', 'Rename'), item('q2', 'More')));
  assert.equal(rows(box).length, 1, 'held while composing');
  fire(ed, 'compositionend');
  assert.equal(rows(box).length, 2);
  assert.equal(editor(box), ed);
});

test('an edited message that is sent meanwhile hands its text over; an unchanged one closes quietly', async () => {
  const { view, box, lost } = await setup();
  view.update(running(item('q1', 'Rename'), item('q2', 'More')));
  fire(button(box, 'text:q1'), 'click');
  editor(box).value = 'Rename it all';
  fire(editor(box), 'input');
  view.update(running(item('q1', 'Rename', 'sending'), item('q2', 'More')));
  assert.deepEqual(lost, ['Rename it all'], 'being sent: the edit cannot apply');
  assert.equal(editor(box), null);
  fire(button(box, 'text:q2'), 'click');
  view.update(running());
  assert.deepEqual(lost, ['Rename it all'], 'unchanged: nothing to hand over');
  assert.equal(box.children.length, 0, 'an empty queue shows nothing');
});

test('tapping another row while the open edit has changes keeps the first open', async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'Rename'), item('q2', 'More')));
  fire(button(box, 'text:q1'), 'click');
  editor(box).value = 'Rename it';
  fire(editor(box), 'input');
  fire(button(box, 'text:q2'), 'click');
  assert.equal(editor(box).value, 'Rename it');
  assert.ok(rowOf(box, 'q1') && one(rowOf(box, 'q1'), cls('queue-edit')), 'still on the first');
  // Without changes, a tap on another row moves the editor there.
  editor(box).value = 'Rename';
  fire(editor(box), 'input');
  fire(button(box, 'text:q2'), 'click');
  assert.equal(editor(box).value, 'More');
});

test('Enter saves on a keyboard; on a touch device it is a newline', async () => {
  for (const touch of [false, true]) {
    const { view, box, calls } = await setup({ touch });
    view.update(running(item('q1', 'Rename')));
    fire(button(box, 'text:q1'), 'click');
    editor(box).value = 'Rename it';
    fire(editor(box), 'input');
    const ev = fire(editor(box), 'keydown', { key: 'Enter' });
    await tick();
    assert.deepEqual(calls, touch ? [] : [['edit', 'q1', 'Rename it']]);
    assert.equal(!!ev.prevented, !touch);
  }
});

test('Escape cancels and focus goes back to the message', async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'Rename')));
  fire(button(box, 'text:q1'), 'click');
  fire(editor(box), 'keydown', { key: 'Escape' });
  assert.equal(editor(box), null);
  assert.equal(document.activeElement, button(box, 'text:q1'));
});

test('Save: the queue from the answer, the editor closed, focus on the message', async () => {
  const { view, box, answers, queues } = await setup();
  view.update(running(item('q1', 'Rename')));
  const after = running(item('q1', 'Rename it'));
  answers.edit = () => ({ ok: true, queue: after });
  fire(button(box, 'text:q1'), 'click');
  editor(box).value = 'Rename it';
  fire(editor(box), 'input');
  fire(button(box, 'save:q1'), 'click');
  await tick();
  assert.equal(editor(box), null);
  assert.deepEqual(queues, [after]);
  assert.equal(button(box, 'text:q1').textContent, 'Rename it');
  assert.equal(document.activeElement, button(box, 'text:q1'));
});

test('a second tap while a request runs does nothing; the button is not disabled and keeps focus', async () => {
  const { view, box, calls, answers } = await setup();
  view.update(running(item('q1', 'a'), item('q2', 'b'), item('q3', 'c')));
  let finish;
  answers.move = () => new Promise((r) => { finish = r; });
  const down = button(box, 'down:q1');
  down.focus();
  fire(down, 'click');
  fire(down, 'click');
  assert.equal(calls.length, 1);
  assert.equal(down.disabled, false);
  assert.equal(document.activeElement, down);
  finish({ ok: true, queue: running(item('q2', 'b'), item('q1', 'a'), item('q3', 'c')) });
  await tick();
  assert.deepEqual(calls, [['move', 'q1', 1]]);
  assert.equal(document.activeElement, button(box, 'down:q1'), 'focus follows the moved message');
});

test('errors go to the one live region outside the list, and a queue change clears them', async () => {
  const { view, box, errorNode, answers } = await setup();
  view.update(running(item('q1', 'Rename', 'sending'), item('q2', 'More')));
  answers.remove = () => ({ ok: false, message: "This message is being sent and can't be changed." });
  fire(button(box, 'delete:q2'), 'click');
  await tick();
  assert.equal(errorNode.textContent, "This message is being sent and can't be changed.");
  assert.ok(!contains(box, errorNode));
  view.update(running(item('q1', 'Rename', 'sending'), item('q2', 'More')));
  assert.equal(errorNode.textContent, "This message is being sent and can't be changed.", 'the same queue again changes nothing');
  view.update(running(item('q2', 'More')));
  assert.equal(errorNode.textContent, '');
});

test('Delete moves focus to the next message; Resume to the heading', async () => {
  const { view, box, answers } = await setup();
  view.update({ state: 'paused', reason: 'Session not running', items: [item('q1', 'a'), item('q2', 'b')] });
  answers.remove = () => ({ ok: true, queue: { state: 'paused', reason: 'Session not running', items: [item('q2', 'b')] } });
  fire(button(box, 'delete:q1'), 'click');
  await tick();
  assert.equal(document.activeElement, button(box, 'text:q2'));
  answers.resume = () => ({ ok: true, queue: running(item('q2', 'b')) });
  fire(one(box, cls('queue-resume')), 'click');
  await tick();
  assert.equal(document.activeElement, one(box, cls('queue-title')));
});

test('editOffer: a lost edit keeps its text with Add to queue and Dismiss', async () => {
  globalThis.document = fakeDocument();
  const { editOfferNode } = await import('./queueView.mjs');
  const queued = [];
  let dismissed = 0;
  const node = editOfferNode('Rename it all', { onQueue: (t) => queued.push(t), onDismiss: () => dismissed++ });
  assert.equal(one(node, cls('edit-offer-line')).textContent, 'This message was sent before your edit was saved.');
  assert.equal(one(node, cls('edit-offer-text')).textContent, 'Rename it all');
  const buttons = all(node, (n) => n.tagName === 'BUTTON');
  assert.deepEqual(buttons.map((b) => b.textContent), ['Dismiss', 'Add to queue']);
  fire(buttons[1], 'click');
  assert.deepEqual(queued, ['Rename it all']);
  fire(buttons[0], 'click');
  assert.equal(dismissed, 1);
});

test("Escape while composing is the input method's, not a cancel", async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'Rename')));
  fire(button(box, 'text:q1'), 'click');
  fire(editor(box), 'compositionstart');
  fire(editor(box), 'keydown', { key: 'Escape', isComposing: true });
  assert.ok(editor(box), 'still open');
});

test('Cancel mid-composition: the editor closes and the held update is drawn; later updates are not frozen', async () => {
  const { view, box } = await setup();
  view.update(running(item('q1', 'Rename')));
  fire(button(box, 'text:q1'), 'click');
  fire(editor(box), 'compositionstart');
  view.update(running(item('q1', 'Rename'), item('q2', 'Held')));
  fire(button(box, 'cancel:q1'), 'click');
  assert.equal(editor(box), null);
  assert.equal(rows(box).length, 2, 'the held update is drawn');
  view.update(running(item('q1', 'Rename'), item('q2', 'Held'), item('q3', 'Later')));
  assert.equal(rows(box).length, 3, 'not frozen');
});

test('Save mid-composition: after it, updates are not frozen', async () => {
  const { view, box, answers } = await setup();
  view.update(running(item('q1', 'Rename')));
  answers.edit = () => ({ ok: true, queue: running(item('q1', 'Renamed')) });
  fire(button(box, 'text:q1'), 'click');
  fire(editor(box), 'compositionstart');
  editor(box).value = 'Renamed';
  fire(editor(box), 'input');
  fire(button(box, 'save:q1'), 'click');
  await tick();
  assert.equal(editor(box), null);
  view.update(running(item('q1', 'Renamed'), item('q2', 'Later')));
  assert.equal(rows(box).length, 2, 'not frozen');
});

test('Cancel while a save is on its way does nothing', async () => {
  const { view, box, answers } = await setup();
  view.update(running(item('q1', 'Rename')));
  let finish;
  answers.edit = () => new Promise((r) => { finish = r; });
  fire(button(box, 'text:q1'), 'click');
  editor(box).value = 'Renamed';
  fire(editor(box), 'input');
  fire(button(box, 'save:q1'), 'click');
  fire(button(box, 'cancel:q1'), 'click');
  assert.ok(editor(box), 'still open while the save runs');
  finish({ ok: true, queue: running(item('q1', 'Renamed')) });
  await tick();
  assert.equal(editor(box), null);
});

test('a lost edit says whether the editor had focus', async () => {
  const { view, box, lost, lostFocus } = await setup();
  view.update(running(item('q1', 'a'), item('q2', 'b')));
  fire(button(box, 'text:q1'), 'click');
  editor(box).value = 'a changed';
  fire(editor(box), 'input');
  view.update(running(item('q2', 'b')));
  fire(button(box, 'text:q2'), 'click');
  editor(box).value = 'b changed';
  fire(editor(box), 'input');
  document.activeElement = null; // the person moved on to the composer
  view.update(running());
  assert.deepEqual(lost, ['a changed', 'b changed']);
  assert.deepEqual(lostFocus, [true, false]);
});

test('editOffersNode: two lost edits are two offers, each with its own buttons', async () => {
  globalThis.document = fakeDocument();
  const { editOffersNode } = await import('./queueView.mjs');
  const queued = [];
  const dismissed = [];
  const node = editOffersNode(['first edit', 'second edit'], { onQueue: (i) => queued.push(i), onDismiss: (i) => dismissed.push(i) });
  const offers = all(node, cls('edit-offer'));
  assert.deepEqual(offers.map((o) => one(o, cls('edit-offer-text')).textContent), ['first edit', 'second edit']);
  fire(one(offers[1], cls('edit-offer-queue')), 'click');
  fire(one(offers[0], cls('edit-offer-dismiss')), 'click');
  assert.deepEqual(queued, [1]);
  assert.deepEqual(dismissed, [0]);
});

test('the queue is one line until its heading is clicked', async () => {
  const { view, box } = await setup({ folded: true });
  view.update(running(item('q1', 'Check the docs links'), item('q2', 'b')));
  const title = one(box, cls('queue-title'));
  assert.equal(one(box, cls('queue-count')).textContent, '2 queued · next: Check the docs links');
  assert.equal(one(box, cls('queue-list')).hidden, true);
  assert.equal(title.attrs['aria-expanded'], 'false');
  fire(title, 'click');
  assert.equal(one(box, cls('queue-list')).hidden, false);
  assert.equal(one(box, cls('queue-count')).textContent, '2 queued');
  assert.equal(title.attrs['aria-expanded'], 'true');
  fire(title, 'click');
  assert.equal(document.activeElement, title, 'the toggle keeps focus');
  assert.notEqual(title.tabIndex, -1, 'the toggle stays in the tab order');
});
