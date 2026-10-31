import { el, icon } from './rows.mjs';
import { movePosition, queueItems, queueSection } from './queue.mjs';

// The chat queue under the composer: a heading, the paused reason or waiting note with Resume, and
// one row per message (queue.mjs says what each shows and allows). A waiting message's text is a
// button that opens it for editing in place (Save, Cancel; Enter saves on a keyboard, Escape
// cancels). Move up, Move down and Delete follow its state.
//
// The open editor's textarea is never rebuilt or detached while it is open: a queue update redraws
// the other rows around it, so its value, selection and focus survive; while an input method is
// composing, updates wait until it ends. An edit whose message is sent meanwhile hands its text to
// onEditLost. Errors go to errorNode, one live region that stays in place; a new queue clears it.
//
// createQueueView({box, errorNode, client: () => queue client (queue.mjs), isTouch, onEditLost(text, hadFocus),
// onQueue(queue), onGone() (focus elsewhere: a change emptied the queue)}) returns {update(queue), reset()}.
export function createQueueView({ box, errorNode, client, isTouch = () => false, onEditLost = () => {}, onQueue = () => {}, onGone = () => {}, open = false }) {
  let queue = null;
  let expanded = open; // the rows are shown; folded, the queue is one line ("2 queued · next: ...")
  let drawn = '';      // what the section was last drawn from
  let editing = null;  // {id, original, text, row, box (the textarea)}
  let composing = false;
  let held = null;     // the latest queue while composing: {queue}
  let busy = false;    // a request is on its way; taps meanwhile do nothing
  let section = null;  // the persistent nodes: {node, count, status, list, title}
  let focusable = new Map(); // data-queue-focus key -> node, from the last draw

  function setError(text) { errorNode.textContent = text || ''; }

  function update(q) {
    if (composing) { held = { queue: q }; return; }
    queue = q;
    const items = queueItems(q);
    if (editing) {
      const it = items.find((x) => x.id === editing.id);
      if (!it || !it.canEdit) closeEditor({ lost: true }); // sent (or being sent) or deleted meanwhile
    }
    const key = JSON.stringify([queueSection(q), items, editing && editing.id]);
    if (key === drawn) return;
    if (drawn) setError('');
    drawn = key;
    draw();
  }

  function reset() {
    expanded = open;
    editing = null;
    composing = false;
    held = null;
    busy = false;
    queue = null;
    drawn = '';
    section = null;
    focusable = new Map();
    box.replaceChildren();
    setError('');
  }

  // closeEditor closes the open editor, ending any composition in it (so updates never stay
  // held), and returns the update held meanwhile ({queue} or null). lost: its message is gone, so
  // changed text is handed over, with whether the editor had focus.
  function closeEditor({ lost = false } = {}) {
    if (!editing) return null;
    const { text, original, row } = editing;
    const hadFocus = row.contains(document.activeElement);
    editing = null;
    composing = false;
    const h = held;
    held = null;
    row.remove();
    if (lost && text.trim() !== original.trim()) onEditLost(text, hadFocus);
    return h;
  }

  // draw brings the section up to date with queue, keeping the editor's row in place.
  function draw(focusKey) {
    const view = queueSection(queue);
    const items = queueItems(queue);
    const active = document.activeElement;
    const had = active && box.contains(active) ? active.getAttribute('data-queue-focus') : null;
    if (!view.visible) {
      section = null;
      focusable = new Map();
      box.replaceChildren();
      if (focusKey) onGone();
      return;
    }
    if (!section) section = buildSection();
    section.node.className = 'queue' + (view.state === 'paused' ? ' paused' : '');
    section.count.textContent = items.length + ' queued' + (!expanded && items[0] ? ' · next: ' + items[0].text : '');
    section.title.setAttribute('aria-expanded', String(expanded));
    section.list.hidden = !(expanded || editing);
    focusable = new Map();
    section.status.replaceChildren();
    section.status.hidden = !(view.line || view.resume);
    if (view.line) section.status.append(auto(el('span', 'queue-line', view.line)));
    if (view.resume) section.status.append(button('queue-resume', 'Resume', 'resume', () => run((c) => c.resume(), 'resume')));

    const next = items.map((it) => (editing && editing.id === it.id ? editing.row : row(it)));
    const keep = editing ? editing.row : null;
    for (const c of [...section.list.children]) if (c !== keep) c.remove();
    // The editor's row stays attached: rows before it go in before it, the rest after.
    const attached = !!keep && keep.parentNode === section.list;
    let passed = false;
    for (const n of next) {
      if (n === keep) { if (!attached) section.list.append(keep); passed = true; continue; }
      if (attached && !passed) section.list.insertBefore(n, keep);
      else section.list.append(n);
    }
    if (!box.contains(section.node)) box.replaceChildren(section.node);

    if (!focusKey && document.activeElement && box.contains(document.activeElement)) return; // focus was not on a redrawn node
    const want = focusKey || had;
    if (!want) return;
    const id = want.slice(want.indexOf(':') + 1);
    const node = usable(want) || usable('text:' + id);
    if (node) node.focus({ preventScroll: true });
    else section.title.focus({ preventScroll: true }); // a button: focusable, and it stays in the tab order
  }

  const usable = (key) => { const n = focusable.get(key); return n && !n.disabled ? n : null; };

  function buildSection() {
    const node = el('section', 'queue');
    node.setAttribute('aria-labelledby', 'queue-heading');
    const head = el('div', 'queue-head');
    // The one line, as a button: a tap unfolds or folds the rows.
    const title = el('button', 'queue-title');
    title.type = 'button';
    title.id = 'queue-heading';
    const count = el('span', 'queue-count');
    title.append(count);
    title.addEventListener('click', () => { expanded = !expanded; draw('title'); });
    head.append(title);
    const status = el('div', 'queue-status');
    const list = el('ol', 'queue-list');
    node.append(head, status, list);
    return { node, title, count, status, list };
  }

  function button(cls, text, key, onClick, label) {
    const b = el('button', cls);
    b.type = 'button';
    if (text) b.textContent = text;
    if (label) b.setAttribute('aria-label', label);
    b.setAttribute('data-queue-focus', key);
    b.addEventListener('click', onClick);
    focusable.set(key, b);
    return b;
  }

  function row(it) {
    const li = el('li', 'queue-item ' + it.state);
    const span = auto(el('span', 'queue-text-inner', it.text));
    span.id = 'queue-text-' + it.id;
    let text;
    if (it.canEdit) {
      text = button('queue-text', '', 'text:' + it.id, () => openEditor(it.id));
      text.setAttribute('aria-label', 'Edit: ' + it.text);
      text.append(span);
    } else {
      text = el('span', 'queue-text');
      text.append(span);
    }
    const tools = el('span', 'queue-tools');
    const tool = (iconName, label, kind, shown, enabled, onClick) => {
      const b = button('queue-tool', '', kind + ':' + it.id, onClick, label);
      b.append(icon(iconName));
      if (!shown) { // keeps the columns aligned; not there for a screen reader or the keyboard
        b.className = 'queue-tool placeholder';
        b.setAttribute('aria-hidden', 'true');
        b.tabIndex = -1;
        b.disabled = true;
        focusable.delete(kind + ':' + it.id);
        return b;
      }
      b.setAttribute('aria-describedby', span.id);
      b.disabled = !enabled;
      return b;
    };
    tools.append(
      tool('moveUp', 'Move up', 'up', it.canMove, movePosition(queue, it.id, -1) !== null, () => move(it.id, -1)),
      tool('moveDown', 'Move down', 'down', it.canMove, movePosition(queue, it.id, 1) !== null, () => move(it.id, 1)),
      tool('trash', 'Delete', 'delete', it.canDelete, true, () => remove(it.id)),
    );
    li.append(text, el('span', 'queue-state', it.label), tools);
    return li;
  }

  function openEditor(id) {
    if (editing) {
      if (editing.text.trim() !== editing.original.trim()) return; // unsaved changes: the open one stays
      closeEditor();
    }
    const it = queueItems(queue).find((x) => x.id === id);
    if (!it || !it.canEdit) return;
    const li = el('li', 'queue-item waiting editing');
    const ta = el('textarea', 'queue-edit');
    ta.rows = 2;
    ta.dir = 'auto';
    ta.value = it.text;
    ta.setAttribute('aria-label', 'Edit queued message');
    ta.setAttribute('data-queue-focus', 'edit:' + id);
    ta.addEventListener('input', () => { if (editing) editing.text = ta.value; });
    ta.addEventListener('compositionstart', () => { composing = true; });
    ta.addEventListener('compositionend', () => {
      composing = false;
      if (editing) editing.text = ta.value;
      if (held) { const h = held; held = null; update(h.queue); }
    });
    ta.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !e.isComposing && e.keyCode !== 229) { e.preventDefault(); cancel(); return; } // while composing, Escape is the input method's
      // Enter saves on a keyboard; on a phone it stays a newline, as in the composer.
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229 && !isTouch()) { e.preventDefault(); save(); }
    });
    const actions = el('div', 'queue-edit-actions');
    const cancelB = el('button', 'queue-cancel', 'Cancel');
    cancelB.type = 'button';
    cancelB.setAttribute('data-queue-focus', 'cancel:' + id);
    cancelB.addEventListener('click', cancel);
    const saveB = el('button', 'queue-save', 'Save');
    saveB.type = 'button';
    saveB.setAttribute('data-queue-focus', 'save:' + id);
    saveB.addEventListener('click', save);
    actions.append(cancelB, saveB);
    li.append(ta, actions);
    editing = { id, original: it.text, text: it.text, row: li, box: ta };
    drawn = '';
    update(queue);
    ta.focus({ preventScroll: true });
    ta.setSelectionRange(ta.value.length, ta.value.length); // only when it opens
  }

  function cancel() {
    if (!editing || busy) return;
    const id = editing.id;
    const h = closeEditor();
    drawn = '';
    setError('');
    update(h ? h.queue : queue);
    draw('text:' + id);
  }

  async function save() {
    if (!editing || busy) return;
    const { id, text } = editing;
    await run((c) => c.edit(id, text), 'text:' + id, () => closeEditor());
  }

  function move(id, delta) {
    const position = movePosition(queue, id, delta);
    if (position === null) return;
    run((c) => c.move(id, position), (delta < 0 ? 'up:' : 'down:') + id);
  }

  function remove(id) {
    const items = queueItems(queue);
    const i = items.findIndex((it) => it.id === id);
    const near = items[i + 1] || items[i - 1];
    run((c) => c.remove(id), near ? 'text:' + near.id : 'resume');
  }

  // run sends one change; on success the answer's queue is drawn (onOk first) and focus goes to
  // focusKey (or its message's text, or the heading); on failure the error shows and nothing moves.
  async function run(request, focusKey, onOk) {
    if (busy) return;
    busy = true;
    setError('');
    let r;
    try { r = await request(client()); } finally { busy = false; }
    if (!r.ok) { setError(r.message); return; }
    const h = onOk ? onOk() : null; // the update held while composing, if any
    if (r.queue) { onQueue(r.queue); queue = r.queue; } else if (h) queue = h.queue;
    drawn = '';
    update(queue);
    draw(focusKey);
  }

  return { update, reset };
}

function auto(node) { node.dir = 'auto'; return node; }

// editOfferNode: an edit whose message was sent before it was saved, when the composer already
// holds other text. The edited text is kept with Add to queue (queues it) and Dismiss; it stays
// until one of them is used. {onQueue(text), onDismiss()}.
export function editOfferNode(text, { onQueue, onDismiss }) {
  const node = el('div', 'edit-offer');
  node.append(el('div', 'edit-offer-line', 'This message was sent before your edit was saved.'));
  node.append(auto(el('div', 'edit-offer-text', text)));
  const actions = el('div', 'edit-offer-actions');
  const dismiss = el('button', 'edit-offer-dismiss', 'Dismiss');
  dismiss.type = 'button';
  dismiss.addEventListener('click', onDismiss);
  const queue = el('button', 'edit-offer-queue', 'Add to queue');
  queue.type = 'button';
  queue.addEventListener('click', () => onQueue(text));
  actions.append(dismiss, queue);
  node.append(actions);
  return node;
}

// editOffersNode: every lost edit still kept, oldest first; onQueue(i) and onDismiss(i) name the one.
export function editOffersNode(texts, { onQueue, onDismiss }) {
  const node = el('div', 'edit-offers');
  texts.forEach((text, i) => node.append(editOfferNode(text, { onQueue: () => onQueue(i), onDismiss: () => onDismiss(i) })));
  return node;
}
