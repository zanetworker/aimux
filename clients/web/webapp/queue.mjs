// The chat queue: messages the Mac sends one at a time when the session is free
// (GET/POST /v1/sessions/{id}/queue, PATCH/DELETE …/queue/{item}, POST
// …/queue/resume). What the composer offers, the queue section and its rows
// follow the shared fixtures in api/testdata/clients/queue/; the iPhone app
// shows the same.

const SEND = { action: 'send', label: 'Send' };
const QUEUE = { action: 'queue', label: 'Add to queue' };
const LABEL = { waiting: 'Waiting', sending: 'Sending', answering: 'Answer in progress' };

// Fixed client lines, not the server's text.
const MESSAGES = {
  bad_text: "This message can't be queued (it starts with ! or /, holds control characters, or is too long).",
  queue_full: 'The queue is full (20 messages). Delete one to add another.',
  item_sending: "This message is being sent and can't be changed.",
  unknown_item: 'This message is no longer in the queue.',
};
const NO_TEXT = 'A queued message needs some text.';

// composerAction: Send while the session is free; Add to queue while it works,
// while its terminal cannot take typing (the Mac sends when it can), while its
// queue is sending (queueActive: the server refuses a direct send then), and
// after a direct send was refused busy (refusedBusy, until the next detail or
// event).
export function composerAction(session, { refusedBusy = false } = {}) {
  if (refusedBusy) return QUEUE;
  if (!session) return SEND;
  if (session.state === 'busy' || queueActive(session.queue)) return QUEUE;
  const t = session.terminal;
  if (t && (t.working || !t.canType || t.status === 'unknown')) return QUEUE;
  return SEND;
}

// queueActive: the queue has a message in flight, or is running with messages
// waiting; the server then answers a direct send 409 busy.
export function queueActive(queue) {
  const items = shown(queue);
  return items.some((it) => it.state !== 'waiting') || (items.length > 0 && queue.state === 'running');
}

// sendRefusal: what the composer offers after a direct send was refused: for
// 409 busy, Add to queue with the text kept and no message (nothing is queued
// by itself); null for any other refusal, which stays a failed send.
export function sendRefusal(e) {
  return e && e.code === 'busy' ? { ...QUEUE, keepText: true, message: null } : null;
}

// The messages shown: every one but `done` (the answer to a retried add).
const shown = (queue) => ((queue && queue.items) || []).filter((it) => it.state !== 'done');

// queueSection: hidden when empty; paused shows its reason and Resume, running
// its waiting note if any.
export function queueSection(queue) {
  if (!shown(queue).length) return { visible: false };
  if (queue.state === 'paused') {
    const out = { visible: true, state: 'paused', resume: true };
    if (queue.reason) out.line = queue.reason;
    return out;
  }
  const out = { visible: true, state: queue.state, resume: false };
  if (queue.note) out.line = queue.note;
  return out;
}

// queueItems: the rows in order, with their label and what may be done to
// them. The one in flight never changes; it is deleted only while paused.
export function queueItems(queue) {
  const paused = !!queue && queue.state === 'paused';
  return shown(queue).map(({ id, text, state }) => {
    const waiting = state === 'waiting';
    return { id, text, state, label: LABEL[state] || state, canEdit: waiting, canMove: waiting, canDelete: waiting || paused };
  });
}

// movePosition: the PATCH position for moving id by delta (-1 up, +1 down),
// counted among the messages not in flight; null when it cannot move so.
export function movePosition(queue, id, delta) {
  const waiting = shown(queue).filter((it) => it.state === 'waiting');
  const i = waiting.findIndex((it) => it.id === id);
  const to = i + delta;
  return i < 0 || to < 0 || to >= waiting.length ? null : to;
}

// queueErrorText: the line shown for a refused or failed queue request.
export function queueErrorText(e) {
  return MESSAGES[e && e.code] || 'Could not change the queue: ' + ((e && e.message) || 'unknown error');
}

// setQueue records a newer queue on a chat (a queue event, a change's answer) and counts it, so a
// detail read before it never puts the older queue back.
export function setQueue(chat, queue) {
  chat.queue = queue;
  chat.queueRev = (chat.queueRev || 0) + 1;
}

// adoptDetail takes a newly read detail on a chat. Its queue (absent when empty) says the queue
// now, unless the queue changed while it was read (readSince is chat.queueRev then).
export function adoptDetail(chat, detail, readSince) {
  chat.detail = detail;
  if ((chat.queueRev || 0) === readSince) chat.queue = detail.queue || null;
}

// createQueueClient wraps request (login.mjs request: a 401 locks and
// replays) for one session. Every call answers {ok: true, queue} (the queue
// after the change; null when the read after an add failed) or {ok: false,
// message}; a failed add also says keepText (the composer keeps it). A retry
// of the same text after a failed add reuses its idempotency key.
export function createQueueClient(request, session, { newKey }) {
  const base = '/v1/sessions/' + encodeURIComponent(session) + '/queue';
  const itemURL = (id) => base + '/' + encodeURIComponent(id);
  let failedAdd = null; // {text, key}

  async function change(method, path, body) {
    try {
      return { ok: true, queue: await request(method, path, body) };
    } catch (e) {
      return { ok: false, message: queueErrorText(e) };
    }
  }

  async function add(raw) {
    const text = String(raw || '').trim();
    if (!text) return { ok: false, keepText: true, message: NO_TEXT };
    const key = failedAdd && failedAdd.text === text ? failedAdd.key : newKey();
    try {
      await request('POST', base, { text, key });
    } catch (e) {
      failedAdd = { text, key };
      return { ok: false, keepText: true, message: queueErrorText(e) };
    }
    failedAdd = null;
    try {
      return { ok: true, queue: await request('GET', base) };
    } catch (_) {
      return { ok: true, queue: null }; // queued; the next detail or event shows it
    }
  }

  return {
    add,
    edit: (id, raw) => {
      const text = String(raw || '').trim();
      return text ? change('PATCH', itemURL(id), { text }) : Promise.resolve({ ok: false, message: NO_TEXT });
    },
    move: (id, position) => change('PATCH', itemURL(id), { position }),
    remove: (id) => change('DELETE', itemURL(id)),
    resume: () => change('POST', base + '/resume'),
  };
}
