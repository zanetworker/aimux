// aimux web client: Now, All and search (tabs.mjs), follow a session,
// continue it and answer its approvals. Served by `aimux remote serve` at /app
// and talking to /v1 on the same origin; tailscale serve supplies the
// identity, and with device login a passkey unlocks the session (login.mjs).
import { createTabs, poller } from './tabs.mjs';
import { newKey } from './nowModel.mjs';
import { nextSession } from './now.mjs';
import { displayTitle, dot, el, icon } from './rows.mjs';
import { buildTimeline, pendingShown } from './timeline.mjs';
import { chatStatus, headerContent, nextBar, pendingNode, promptCard, readOnlyLine, timelineNodes } from './chatView.mjs';
import { adoptDetail, composerAction, createQueueClient, sendRefusal, setQueue } from './queue.mjs';
import { createQueueView, editOffersNode } from './queueView.mjs';
import { terminalErrorText, terminalPollInterval, terminalReasonLine, terminalWorkingLine } from './terminal.mjs';
import { announce, createInfoSheet, renderInfoSheet, renderMetaCard } from './sessionInfo.mjs';
import { referenceContext } from './references.mjs';
import { createLogin } from './login.mjs';
import { readChat } from './chatLoad.mjs';

const $ = (id) => document.getElementById(id);

let events = null;       // EventSource for the open session
let wakeLock = null;

// API: every /v1 call goes through the login, which holds a call answered
// 401 login_required until the unlock and sends it again. opts.interaction
// marks something the person did (it keeps the login alive); polls never.

function localStore() {
  try { return window.localStorage; } catch (_) { return { getItem: () => null, setItem: () => {}, removeItem: () => {} }; }
}

const login = createLogin({
  fetch: (path, opts) => fetch(path, opts),
  credentials: navigator.credentials,
  storage: localStore(),
  hostname: location.hostname,
  publicKeyCredential: window.PublicKeyCredential,
});

const api = (method, path, body, opts) => login.request(method, path, body, opts);
const asked = { interaction: true };

function setStatus(text, cls) {
  const s = $('status');
  s.textContent = text || '';
  s.className = cls || '';
}

// Screens: the tabs (Now, All, search), the new-session sheet, a session.

const tabs = createTabs(api, { headerButtons: () => (login.deviceLogin ? [lockButton()] : []) });

// The title, description and pin actions: state per opened sheet, so an answer for one session
// never lands in another's sheet (sessionInfo.mjs).
const infoSheet = createInfoSheet({
  model: tabs.model,
  changed: (_, focusKey) => { if (infoOpen) drawInfo(false, focusKey); },
  took: (id, result) => takeMeta(id, result),
  announce: (text) => announce($('info-status'), text),
  alert: (text) => announce($('info-alert'), text),
});

const TAB_ROUTES = { '#/all': 'all', '#/search': 'search' };
let lastTab = '#/'; // where Back returns to

function showView(name) {
  $('tabs').hidden = name !== 'tabs';
  $('newsheet').hidden = name !== 'new';
  $('session').hidden = name !== 'session';
  $('topbar').hidden = name !== 'new';
  if (name !== 'tabs') tabs.hide();
}

function showTabs(route) {
  if (chat) drafts.set(chat.id, $('prompt').value);
  closeSession();
  lastTab = route;
  showView('tabs');
  document.title = 'aimux';
  tabs.show(TAB_ROUTES[route] || 'now');
}

// New session: pick one of the recent project folders, say what to do.

let newProject = '';

async function showNew() {
  if (chat) drafts.set(chat.id, $('prompt').value);
  closeSession();
  showView('new');
  $('title').textContent = 'New session';
  $('newerror').hidden = true;
  const box = $('projects');
  // Nothing to start in until this visit's projects are listed.
  newProject = '';
  $('newstart').disabled = true;
  box.replaceChildren(el('div', 'empty', 'Loading projects…'));
  const gen = nav;
  let data;
  try {
    data = await api('GET', '/v1/projects', undefined, asked);
  } catch (e) {
    if (gen !== nav) return;
    box.replaceChildren(el('div', 'empty', 'Could not load projects: ' + e.message));
    return;
  }
  if (gen !== nav) return; // left the sheet, or opened it again, meanwhile
  if (!data.projects.length) {
    box.replaceChildren(el('div', 'empty', 'No recent projects. Start a session in a folder on your Mac first.'));
    return;
  }
  newProject = data.projects[0].path;
  $('newstart').disabled = false;
  box.replaceChildren(...data.projects.map((p) => {
    const b = el('button', 'project' + (p.path === newProject ? ' chosen' : ''));
    b.type = 'button';
    b.setAttribute('role', 'radio');
    b.setAttribute('aria-checked', String(p.path === newProject));
    b.append(el('span', 'name', p.name), el('span', 'path', p.path.replace(/^\/Users\/[^/]+/, '~')));
    b.addEventListener('click', () => {
      newProject = p.path;
      box.querySelectorAll('.project').forEach((x) => { x.classList.remove('chosen'); x.setAttribute('aria-checked', 'false'); });
      b.classList.add('chosen');
      b.setAttribute('aria-checked', 'true');
    });
    return b;
  }));
  $('newprompt').focus();
}

let creating = false;
// createReq keeps a failed request's key with what it asked for: a retry of
// the same project and text reuses it (so a lost response cannot create two
// sessions); anything else gets a new key.
let createReq = null; // {key, cwd, text}

async function startNew(ev) {
  ev.preventDefault();
  const text = $('newprompt').value.trim();
  if (!text || !newProject || creating || $('newsheet').hidden) return;
  dictationGen++; // as for a message: a late dictation result must not start another session
  Voice.stopListening();
  const cwd = newProject;
  const req = createReq && createReq.cwd === cwd && createReq.text === text ? createReq : { key: newKey(), cwd, text };
  const gen = nav;
  creating = true;
  $('newstart').disabled = true;
  $('newerror').hidden = true;
  try {
    const r = await api('POST', '/v1/sessions', { cwd, text, key: req.key });
    createReq = null;
    if (gen !== nav) return; // the user left the sheet; the session is in the list
    if ($('newprompt').value.trim() === text) $('newprompt').value = '';
    location.hash = '#/s/' + encodeURIComponent(r.id);
  } catch (e) {
    createReq = req;
    if (gen !== nav) return;
    $('newerror').textContent = 'Could not start the session: ' + e.message;
    $('newerror').hidden = false;
  } finally {
    creating = false;
    $('newstart').disabled = false;
  }
}

// Session view. The timeline is built from the history turns and the event log
// (timeline.mjs) and drawn again in full whenever either changes.

// The open session: its detail (history turns, state, stats), the events
// loaded and streamed so far, the log they belong to, and how it is followed:
// 'loading', 'live', 'reconnecting', 'readOnly', 'terminal' (open in a terminal
// on the Mac that this server can type into: read again on a timer, no stream)
// or 'failed' (failMsg says why). In a terminal session, lastSend is when a
// message was last typed into it from here, answeredPrompts the prompts
// answered here (collapsed until the next read), and pressKeys the idempotency
// key of a failed prompt answer per "<promptId>/<option>", kept for a retry.
// queue is the session's chat queue (QueueStatus, from the detail, a queue
// event or a queue request's answer), queueClient its requests (queue.mjs),
// and busyOffer says a direct send was refused busy: the composer offers Add
// to queue until the next detail or the turn's end. lostEdits are edited texts whose message was
// sent before it was saved, kept with its own Add to queue while the box held other text.
let chat = null; // {id, detail, events, log, lastSeq, mode, failMsg, lastSend, answeredPrompts, pressKeys, queue, queueClient, busyOffer, lostEdits}
let result = { items: [], working: false };
let pendingApproval = null;
let running = false;    // a turn is in progress
let following = false;  // the event stream is open: replies will show

// nav counts navigations and reloads; a response for an older one is
// dropped, so a slow session can never take over the view of a newer one.
let nav = 0;
let retryTimer = null;
let retryDelay = 2000;

const drafts = new Map(); // session id -> unsent text, so a draft never goes to another session
// A message sent from here that the timeline does not show yet, per session:
// {text, key, turn, sentAt, failure}. A failed one keeps its key, so Retry
// sends it again under the same key and the server runs it at most once.
const pendings = new Map();
const sending = new Set(); // sessions with a message POST in flight
const answering = new Set();      // approval ids with an answer on its way
const answerErrors = new Map();   // approval id -> why its last answer failed
const answered = new Set();       // approvals answered here (or already elsewhere)
const chosen = new Map();         // approval id -> the option tapped
let openSteps = new Set();        // timeline indexes of opened step groups
let openThoughts = new Set();
let openStepDetails = new Set(); // opened steps, "group index:step index"
let stopError = '';
let notice = '';
let infoOpen = false;
let adding = false;     // an add to the queue is on its way
let queueNotice = '';   // why the text in the box was not queued; cleared when it changes

function closeSession() {
  nav++;
  clearTimeout(retryTimer);
  retryDelay = 2000;
  following = false;
  if (events) { events.close(); events = null; }
  chat = null;
  infoOpen = false;
  infoSheet.close();
  $('infosheet').replaceChildren();
  nowPoll.stop();
  termPoll.stop();
  setRunning(false);
}

// /v1/health's links (Jira) for reference links in the chat; read once, absent on failure.
let health = null;
let healthAsked = false;
function loadHealth() {
  if (healthAsked) return;
  healthAsked = true;
  api('GET', '/v1/health').then((h) => { health = h; if (chat) scheduleRender(); }, () => { healthAsked = false; });
}

async function showSession(id) {
  loadHealth();
  if (chat) drafts.set(chat.id, $('prompt').value);
  closeSession();
  $('prompt').value = drafts.get(id) || '';
  autoResize();
  const gen = nav;
  chat = {
    id, detail: null, events: [], log: '', lastSeq: 0, mode: 'loading', failMsg: '', lastSend: null, answeredPrompts: new Set(), pressKeys: new Map(),
    queue: null, queueClient: createQueueClient(api, id, { newKey }), busyOffer: false, lostEdits: [],
  };
  queueNotice = '';
  queueView.reset();
  openSteps = new Set();
  openThoughts = new Set();
  openStepDetails = new Set();
  stopError = '';
  notice = '';
  stick = true;
  showView('session');
  render();
  nowPoll.tick();
  await load(id, gen, true);
}

// load reads the session (readChat: snapshot, history, a check for a turn
// that started meanwhile), then follows the stream from the snapshot's cursor.
async function load(id, gen, opened) {
  let page, detail;
  const queueRev = (chat && chat.queueRev) || 0;
  try {
    const got = await readChat(api, id, { opened, stale: () => gen !== nav });
    if (!got) return;
    ({ page, detail } = got);
  } catch (e) {
    if (gen !== nav) return;
    if (e.status === 404) {
      chat.mode = 'failed';
      chat.failMsg = 'No session with that id.';
      render();
      return;
    }
    retry(id, gen, 'Could not load the session: ' + e.message);
    return;
  }
  if (gen !== nav) return;
  setDetail(chat, detail, queueRev);
  retryDelay = 2000;
  if (infoOpen) drawInfo();
  document.title = displayTitle(detail.title, id) + ' · aimux';
  // A session open in a terminal this server can type into is read again on a timer.
  if (isTerminal(detail)) {
    chat.events = [];
    chat.mode = 'terminal';
    prunePrompts(chat);
    render();
    termPoll.schedule();
    return;
  }
  // A session open in a terminal, or one that never started, is history only.
  if (detail.state === 'open_in_terminal' || detail.state === 'unavailable') {
    chat.events = [];
    chat.mode = 'readOnly';
    render();
    return;
  }
  chat.events = page.events || [];
  chat.log = page.log || '';
  chat.lastSeq = chat.events.reduce((m, e) => Math.max(m, e.seq), 0);
  if (chat.mode !== 'reconnecting') chat.mode = 'live';
  render();
  stream(id, gen, chat.lastSeq, chat.log);
}

// setDetail takes a newly read detail: its queue (absent when empty) unless a
// newer one came while it was read (adoptDetail), and the end of an offer to
// queue after a refused send (the detail says the state now).
function setDetail(c, detail, queueRev) {
  adoptDetail(c, detail, queueRev);
  c.busyOffer = false;
}

// isTerminal: open in a terminal on the Mac, from a server that says whether it can type into it.
function isTerminal(detail) {
  return detail.state === 'open_in_terminal' && !!detail.terminal;
}

const detailURL = (id) => '/v1/sessions/' + encodeURIComponent(id) + '?turns=30';

// readTerminal reads a terminal session once; one that left the terminal is
// loaded again as a live chat. A failed read waits for the next one.
async function readTerminal() {
  const c = chat;
  if (!c || c.mode !== 'terminal') return;
  let detail;
  const queueRev = c.queueRev || 0;
  try { detail = await api('GET', detailURL(c.id)); } catch (_) { return; }
  if (chat !== c || c.mode !== 'terminal') return;
  if (!isTerminal(detail)) { reload(); return; }
  setDetail(c, detail, queueRev);
  prunePrompts(c);
  if (infoOpen) drawInfo();
}

// prunePrompts forgets answers for prompts the screen no longer shows.
function prunePrompts(c) {
  const showing = c.detail.terminal.prompt ? c.detail.terminal.prompt.promptId : null;
  for (const id of c.answeredPrompts) if (id !== showing) c.answeredPrompts.delete(id);
  for (const slot of c.pressKeys.keys()) if (showing === null || !slot.startsWith(showing + '/')) c.pressKeys.delete(slot);
}

// The terminal's prompt card, unless it was answered here and not read again since.
function shownPrompt(c) {
  const prompt = c.mode === 'terminal' && c.detail ? c.detail.terminal.prompt : null;
  return prompt && !c.answeredPrompts.has(prompt.promptId) ? prompt : null;
}

// reload reads the open session again in place: the stream dropped and came
// back (the server may have restarted), or the server's log changed.
function reload() {
  if (!chat) return;
  if (events) { events.close(); events = null; }
  following = false;
  clearTimeout(retryTimer);
  chat.mode = chat.detail ? 'reconnecting' : 'loading';
  render();
  load(chat.id, ++nav);
}

function stream(id, gen, after, log) {
  if (gen !== nav) return;
  // The log id scopes the cursor: if the server restarted since the
  // snapshot, it replays its new log instead of hiding it behind our cursor.
  events = new EventSource('/v1/sessions/' + encodeURIComponent(id) + '/events?after=' + after + '&log=' + encodeURIComponent(log || '') + '&queue=1');
  let dropped = false;
  events.onopen = () => {
    if (gen !== nav) return;
    if (dropped) { reload(); return; } // the server may have restarted: read it again
    following = true;
    retryDelay = 2000;
    chat.mode = 'live';
    render();
  };
  events.onerror = () => {
    if (gen !== nav) return;
    dropped = true;
    following = false;
    checkLogin(gen);
    if (events && events.readyState === EventSource.CLOSED) {
      // The browser gave up (the server refused or is gone): start over.
      events = null;
      retry(id, gen, 'Lost the connection to your Mac.');
    } else {
      chat.mode = 'reconnecting';
      render();
    }
  };
  for (const kind of ['turn_started', 'update', 'approval', 'approval_resolved', 'turn_done', 'turn_error', 'queue']) {
    events.addEventListener(kind, (msg) => {
      if (gen !== nav) return;
      // Frames are "<log>.<seq>": a frame from another log is a restarted server.
      const frameLog = (msg.lastEventId || '').replace(/\.\d+$/, '');
      if (frameLog && frameLog !== chat.log) { reload(); return; }
      try { receive(JSON.parse(msg.data)); } catch (err) { console.error(err); }
    });
  }
}

// checkLogin reads health once after the stream failed: a login that ended
// shows the lock (the stream stops; the reload waits for the unlock), a
// lost network leaves the browser reconnecting.
let checking = false;
async function checkLogin(gen) {
  if (checking) return;
  checking = true;
  try {
    if ((await login.check()) !== 'none' || gen !== nav || !chat) return;
    if (events) { events.close(); events = null; }
    retry(chat.id, gen, 'Locked.');
  } finally {
    checking = false;
  }
}

// retry loads the session again with backoff while the view still shows it.
function retry(id, gen, why) {
  following = false;
  if (chat.detail) chat.mode = 'reconnecting';
  else { chat.mode = 'failed'; chat.failMsg = why; }
  render();
  clearTimeout(retryTimer);
  retryTimer = setTimeout(() => {
    if (gen !== nav) return;
    retryDelay = Math.min(retryDelay * 2, 30000);
    load(id, gen);
  }, retryDelay);
}

// receive applies one streamed event; buildTimeline keeps each seq once.
function receive(e) {
  if (e.kind === 'queue') { // a snapshot of the queue, of no turn: not a timeline event
    chat.lastSeq = Math.max(chat.lastSeq, e.seq);
    if (e.data) setQueue(chat, e.data);
    scheduleRender();
    return;
  }
  if (chat.events.some((x) => x.seq === e.seq)) return;
  chat.events.push(e);
  chat.lastSeq = Math.max(chat.lastSeq, e.seq);
  if (e.kind === 'approval' && Voice.settings().speak) {
    dictationGen++;
    Voice.speak('The agent asks for permission: ' + ((e.data && e.data.title) || 'a tool call') + '. Tap one of the options on screen.');
  }
  if (e.kind === 'approval_resolved') clearBareAnswer();
  if (e.kind === 'turn_done' || e.kind === 'turn_error') {
    chat.busyOffer = false;
    render();
    if (e.kind === 'turn_error' && Voice.settings().speak) {
      dictationGen++; // as for any read-aloud: a dictation in progress must not auto-send
      Voice.speak('The turn failed.');
    } else if (e.kind === 'turn_done' && (e.data || {}).stopReason !== 'cancelled') {
      speakTurn();
    }
    refreshDetail();
    return;
  }
  scheduleRender();
}

// refreshDetail reads the history again once a turn ends, so the turn's
// record (and the session's stats) come from the session file.
async function refreshDetail() {
  const c = chat;
  const queueRev = c.queueRev || 0;
  try {
    const detail = await api('GET', '/v1/sessions/' + encodeURIComponent(c.id) + '?turns=30');
    if (chat !== c) return;
    setDetail(c, detail, queueRev);
    render();
    if (infoOpen) drawInfo(); // new stats
  } catch (_) { /* the next load reads it */ }
}

let renderQueued = false;
function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => { renderQueued = false; render(); });
}

// render draws the whole session view from its state.
function render() {
  if (!chat) return;
  const c = chat;
  const turns = (c.detail && c.detail.turns) || [];
  result = buildTimeline({ turns, events: c.events, now: Date.now() });
  pendingApproval = null;
  for (const it of result.items) if (it.kind === 'approval' && it.outcome === null && !answered.has(it.approvalId)) pendingApproval = it.approvalId;
  const prompt = shownPrompt(c);
  if (prompt) pendingApproval = prompt.promptId;
  const terminal = c.mode === 'terminal' ? c.detail.terminal : null;
  const pending = pendings.get(c.id);
  if (pending && pendingShown(pending, c.events, turns)) pendings.delete(c.id);
  const readOnly = c.mode === 'readOnly';

  const listed = tabs.model.sessions.find((s) => s.id === c.id);
  const title = c.detail ? displayTitle(c.detail.title, c.id) : listed ? displayTitle(listed.title, listed.id) : 'Loading…';
  const status = chatStatus({ detail: c.detail, readOnly, result, pendingApproval });
  $('chat-title').replaceChildren(...headerContent(title, status, c.detail ? c.detail.project : ''));
  $('reconnecting').hidden = c.mode !== 'reconnecting';

  const box = $('messages');
  const refs = referenceContext(c.detail, health);
  const nodes = timelineNodes(result.items, {
    refs, working: result.working, openSteps, openThoughts, openStepDetails, answered, answering, answerErrors, chosen,
    onToggleSteps: (i) => { flip(openSteps, i); render(); },
    onToggleThought: (i) => { flip(openThoughts, i); render(); },
    onToggleStepDetail: (k) => { flip(openStepDetails, k); render(); },
    onAnswer: answer,
  });
  const p = pendings.get(c.id);
  if (p && !readOnly) {
    nodes.push(p.queueOffer
      ? pendingNode(p, () => queueText(p.text, (c2) => { if (pendings.get(c2.id) === p) pendings.delete(c2.id); }), { retryLabel: 'Add to queue', now: Date.now(), refs })
      : pendingNode(p, () => sendText(c.id, p.text), { terminal: !!terminal, canRetry: !terminal || terminal.canType, now: Date.now(), refs }));
  }
  if (prompt) {
    nodes.push(promptCard(prompt, {
      answering, answerErrors,
      onAnswer: (_, o) => answerPrompt(prompt, prompt.options.find((x) => x.key === o.optionId)),
    }));
  }
  // While Claude works on the Mac: a live line with how long and the current step.
  const working = !prompt ? terminalWorkingLine(c.detail) : '';
  if (working) {
    const line = el('div', 'working-line');
    line.append(dot('working'), el('span', '', working));
    nodes.push(line);
  }
  if (c.mode === 'loading' && !nodes.length) nodes.push(el('div', 'chat-empty', 'Loading…'));
  if (c.mode === 'failed') {
    const f = el('div', 'chat-empty');
    f.append(el('p', '', c.failMsg));
    nodes.push(f);
  }
  // A step row keeps its focus across renders (a toggle, a streamed update): found again by its key.
  const active = document.activeElement;
  const focusedStep = active && box.contains(active) ? active.getAttribute('data-step-key') : null;
  box.replaceChildren(...nodes);
  if (focusedStep) {
    const again = box.querySelector('[data-step-key="' + CSS.escape(focusedStep) + '"]');
    if (again) again.focus({ preventScroll: true });
  }
  scrollDown();

  // Bottom: the Next bar, a note, then the composer or the read-only line.
  const next = c.mode === 'failed' || pendingApproval ? null : nextSession(tabs.model.view, c.id);
  $('nextbar').replaceChildren(...(next ? [nextBar(next, displayTitle(next.session.title, next.session.id),
    () => { location.hash = '#/s/' + encodeURIComponent(next.session.id); })] : []));
  const note = notice || queueNotice || (stopError ? 'Could not stop · ' + stopError : '');
  $('notice').textContent = note;
  $('notice').hidden = !note;
  const unavailable = c.detail && c.detail.state === 'unavailable';
  // A terminal that cannot be typed into says why, unless its prompt's card says what to do.
  // The prompt shown is the card's (an answered one is not), so the line reads the rest of the detail.
  const reason = terminal && !prompt
    ? terminalReasonLine({ ...c.detail, terminal: { ...terminal, prompt: null }, queue: c.queue }, { queue: queueOn() })
    : '';
  $('readonly').replaceChildren(...(readOnly ? [readOnlyLine(unavailable
    ? 'This session never started: its first message failed before Claude saved it. Start a new session from Now.'
    : "Read-only here while it's open in a terminal on your Mac")] : reason ? [readOnlyLine(reason)] : []));
  // With the queue, a terminal that cannot take typing still takes messages to queue.
  $('composer').hidden = readOnly || c.mode === 'failed' || (!!terminal && !terminal.canType && !queueOn());
  $('mic').hidden = !Voice.canListen;
  drawAction();
  drawQueue();
  setRunning(result.working);
}

// queueOn: the server keeps a chat queue (its health says so).
function queueOn() {
  return !!health && (health.capabilities || []).includes('queue');
}

// action is the composer's main button: Send, or Add to queue while the session works, its
// terminal cannot take typing, its queue is sending, or a send was just refused busy (queue.mjs,
// the shared fixtures).
function action() {
  if (!chat || !queueOn()) return 'send';
  const live = chat.mode === 'terminal' ? chat.detail : { state: result.working ? 'busy' : 'idle' };
  return composerAction({ ...live, queue: chat.queue }, { refusedBusy: chat.busyOffer }).action;
}

let actionDrawn = '';
function drawAction() {
  const a = action();
  $('queue-add').hidden = true; // one send arrow, like any chat
  if (a === actionDrawn) return;
  actionDrawn = a;
  // The same arrow; while Claude works it queues the message, which goes once Claude finishes.
  const b = $('send');
  b.setAttribute('aria-label', a === 'queue' ? 'Send when Claude finishes' : 'Send');
  b.replaceChildren(icon('up'));
}

// The queue under the composer (queueView.mjs); its requests go through this chat's client.
const queueView = createQueueView({
  box: $('queue'),
  errorNode: $('queue-error'),
  client: () => chat.queueClient,
  isTouch: () => 'ontouchstart' in window,
  onQueue: (q) => { if (chat) setQueue(chat, q); },
  onGone: () => $('prompt').focus(),
  // An edit whose message was sent first: its text goes to an empty box, else it is kept in an
  // offer (Add to queue, Dismiss) until one of them is used; each lost edit has its own. Focus
  // that was in the editor follows the text.
  onEditLost: (text, hadFocus) => {
    if (!chat) return;
    if (!$('prompt').value.trim()) {
      $('prompt').value = text;
      autoResize();
      updateSend();
      if (hadFocus) $('prompt').focus();
    } else {
      chat.lostEdits = [...chat.lostEdits, { text }];
      if (hadFocus) offerFocus = chat.lostEdits[chat.lostEdits.length - 1];
    }
    render();
  },
});

function drawQueue() {
  queueView.update(chat && queueOn() && chat.mode !== 'failed' ? chat.queue : null);
  drawEditOffers();
}

// The lost edits' offers, drawn again only when the list changes (so their buttons keep focus).
let offersDrawn = null;
let offerFocus = null; // the offer whose Add to queue takes focus after the next draw
function drawEditOffers() {
  const list = chat ? chat.lostEdits : [];
  if (list === offersDrawn && !offerFocus) return;
  const box = $('edit-offer');
  const hadFocus = box.contains(document.activeElement);
  offersDrawn = list;
  // Ending an offer: focus goes to the next one still kept, else the message box.
  const end = (entry) => {
    const rest = chat.lostEdits.filter((x) => x !== entry);
    chat.lostEdits = rest;
    if (rest.length) offerFocus = rest[Math.min(list.indexOf(entry), rest.length - 1)];
    render();
    if (!rest.length) $('prompt').focus();
  };
  box.replaceChildren(...(list.length ? [editOffersNode(list.map((x) => x.text), {
    onQueue: (i) => queueText(list[i].text, (c) => { if (c === chat) end(list[i]); }),
    onDismiss: (i) => end(list[i]),
  })] : []));
  const want = offerFocus ? list.indexOf(offerFocus) : -1;
  offerFocus = null;
  if (want >= 0) box.querySelectorAll('.edit-offer-queue')[want].focus();
  else if (hadFocus && !list.length) $('prompt').focus();
}

function setRunning(on) {
  running = on;
  $('stop').hidden = !on;
  $('send').hidden = on && !queueOn(); // with the queue it offers Add to queue meanwhile
  updateSend();
  if (on) requestWakeLock(); else releaseWakeLock();
}

// canSend: replies will show, from the stream or, in a terminal session, from reading it again.
function canSend() {
  return !!chat && (following || chat.mode === 'terminal');
}

function updateSend() {
  const empty = !$('prompt').value.trim();
  $('queue-add').disabled = !chat || !chat.detail || empty; // a tap while an add runs is ignored, not disabled
  if (action() === 'queue') { $('send').disabled = $('queue-add').disabled; return; }
  $('send').disabled = !canSend() || sending.has(chat.id) || empty;
}

// Session info: title and description (Edit, Update summary, Pin), then cost, tokens, model and
// timing; voice settings under them.
function openInfo() {
  if (!chat) return;
  infoOpen = true;
  infoSheet.open(chat.id);
  drawInfo(true);
  // Today's naming count and the describe capability, read now (they change during the day).
  tabs.model.readHealth().then(() => { if (infoOpen) drawInfo(); });
}

function closeInfo() {
  infoOpen = false;
  infoSheet.close();
  $('infosheet').replaceChildren();
  $('chat-info').focus();
}

// The session as the sheet shows it: the detail, else the listed session.
function infoDetail() {
  const listed = tabs.model.sessions.find((s) => s.id === chat.id);
  return chat.detail || (listed && { ...listed, lastActive: new Date(listed.lastActive).toISOString() });
}

// drawInfo draws the sheet again; the focused control (by data-focus), a field's caret and the
// scroll position survive, so a redraw while editing never loses the text or the place.
function drawInfo(focus, focusKey) {
  const c = chat;
  const st = infoSheet.current;
  if (!c || !st) return;
  const detail = infoDetail();
  const old = $('infosheet');
  const active = old.contains(document.activeElement) ? document.activeElement : null;
  const want = focusKey || active?.dataset?.focus;
  const caret = active && 'selectionStart' in active ? [active.selectionStart, active.selectionEnd] : null;
  const scroll = old.querySelector('.sheet')?.scrollTop ?? 0;
  const meta = { title: displayTitle(detail ? detail.title : '', c.id), description: detail?.description, pinned: !!detail?.pinned };
  const top = renderMetaCard({
    meta, state: st, describing: tabs.model.describing.has(c.id), canEdit: tabs.model.canDescribe, naming: tabs.model.health?.naming,
    on: {
      edit: (field) => infoSheet.edit(field, field === 'title' ? detail?.title : detail?.description),
      draft: (text) => infoSheet.draft(text),
      cancel: () => infoSheet.cancel(),
      save: () => infoSheet.save(),
      describe: () => { infoSheet.describe().then(() => tabs.model.readHealth()).then(() => { if (infoOpen) drawInfo(); }); },
      pin: () => infoSheet.pin(!detail?.pinned),
    },
  });
  const { node, focus: focusDone } = renderInfoSheet({
    title: meta.title, detail, onClose: closeInfo, top, extra: voiceSettings(),
  });
  old.replaceChildren(node);
  const sheet = old.querySelector('.sheet');
  if (sheet) sheet.scrollTop = scroll;
  if (focus) { focusDone(); return; }
  const target = want && old.querySelector(`[data-focus="${CSS.escape(want)}"]`);
  if (!target) return;
  target.focus();
  if (caret && active?.dataset?.focus === want && 'setSelectionRange' in target) target.setSelectionRange(...caret);
  else if (want.startsWith('input:')) target.setSelectionRange(target.value.length, target.value.length);
}

// takeMeta copies a meta or describe answer into the open chat's detail (the list took it already).
function takeMeta(id, result) {
  if (!chat || chat.id !== id || !chat.detail) return;
  chat.detail.title = result.title;
  if (typeof result.description === 'string') chat.detail.description = result.description; else delete chat.detail.description;
  if (typeof result.pinned === 'boolean') chat.detail.pinned = result.pinned;
  scheduleRender(); // the header shows the title
}

function voiceSettings() {
  if (!Voice.canListen && !Voice.canSpeak) return [];
  const card = el('div', 'card info-card');
  const v = Voice.settings();
  const options = [['speak', 'Read replies aloud'], ['autoSend', 'Send when I stop talking'], ['handsFree', 'Hands-free conversation']];
  for (const [name, label] of options) {
    if (!Voice.canListen && name !== 'speak') continue;
    const row = el('label', 'info-row');
    const box = el('input');
    box.type = 'checkbox';
    box.checked = !!v[name];
    box.addEventListener('change', () => {
      Voice.set(name, box.checked);
      if (!box.checked && name === 'speak') Voice.stopSpeaking();
      drawInfo();
    });
    row.append(el('span', 'info-label', label), box);
    card.append(row);
  }
  return [el('div', 'info-section', 'Voice'), card, el('p', 'info-hint', 'Approvals are always a tap; they are read out, never answered by voice.')];
}

// Approvals

const BARE_ANSWER = /^(yes|no|yeah|nope|ok|okay|allow|deny|sure)[.!]?$/i;

async function answer(item, option) {
  const id = item.approvalId;
  if (answering.has(id)) return;
  answering.add(id);
  answerErrors.delete(id);
  chosen.set(id, option);
  render();
  try {
    await api('POST', '/v1/approvals/' + encodeURIComponent(id), { option: option.optionId });
    answered.add(id);
  } catch (e) {
    // no_such_approval: already answered elsewhere, so the card is stale, not failed.
    if (e.status === 404 && e.code === 'no_such_approval') answered.add(id);
    else answerErrors.set(id, 'Could not answer: ' + e.message);
  } finally {
    answering.delete(id);
    render();
  }
  if (!answered.has(id)) return;
  clearBareAnswer();
  // The answered session may leave Needs you: update the Next bar now, not at the next poll.
  try { await tabs.model.refresh(); } catch (_) { /* the poll tries again */ }
  render();
}

// A bare yes or no typed or dictated for a request is spent with it; left in
// the box, one tap would send it to the agent as a message.
function clearBareAnswer() {
  if (BARE_ANSWER.test($('prompt').value.trim())) {
    $('prompt').value = '';
    autoResize();
    updateSend();
  }
  notice = '';
}

// Sending and stopping

async function send(ev) {
  ev.preventDefault();
  if (action() === 'queue') { await addToQueue(); return; }
  const text = $('prompt').value.trim();
  if (!text || !chat || chat.mode === 'readOnly' || sending.has(chat.id) || !canSend()) return;
  if (chat.mode === 'terminal' && !chat.detail.terminal.canType) return;
  if (pendingApproval && BARE_ANSWER.test(text)) {
    // A bare yes or no while a permission request waits is meant for the
    // request; answering it takes a tap on one of its options.
    notice = 'Answer the permission request with one of its buttons.';
    render();
    return;
  }
  if (running) return;
  // Sending ends any dictation: a late result must not refill the box or
  // send the same words again.
  dictationGen++;
  Voice.stopListening();
  Voice.stopSpeaking();
  notice = '';
  stick = true;
  $('prompt').value = '';
  autoResize();
  drafts.delete(chat.id);
  await sendText(chat.id, text);
}

// sendText shows the message at once as pending; sending the same text again
// after a failure is a retry and keeps its key. In a terminal session it is
// typed into the terminal and never retried here: a timeout may have typed
// part of it already, so only the person retries (same key).
async function sendText(session, text) {
  if (sending.has(session)) return;
  const c = chat && chat.id === session ? chat : null;
  if (c && c.mode === 'terminal') { await typeIntoTerminal(c, text); return; }
  const prior = pendings.get(session);
  const key = prior && prior.text === text && prior.failure ? prior.key : newKey();
  const p = { text, key, turn: null, sentAt: Date.now(), failure: null };
  pendings.set(session, p);
  sending.add(session);
  if (chat && chat.id === session) render();
  try {
    const r = await api('POST', '/v1/sessions/' + encodeURIComponent(session) + '/messages', { text, key });
    p.turn = r.turn || null;
  } catch (e) {
    if (refusedBusy(session, p, e)) return;
    p.failure = sendError(e);
    if (e.code === 'open_in_terminal' && chat && chat.id === session) reload();
  } finally {
    sending.delete(session);
    if (chat && chat.id === session) render();
  }
}

async function typeIntoTerminal(c, text) {
  const prior = pendings.get(c.id);
  if (prior && prior.failure && !c.detail.terminal.canType) return; // Retry waits for the terminal
  const key = prior && prior.text === text && prior.failure ? prior.key : newKey();
  const p = { text, key, turn: null, sentAt: Date.now(), failure: null };
  pendings.set(c.id, p);
  sending.add(c.id);
  render();
  try {
    await api('POST', '/v1/sessions/' + encodeURIComponent(c.id) + '/terminal/messages', { text, key });
    c.lastSend = Date.now();
    if (chat === c) termPoll.schedule(); // reads every 2 s from now on, for 2 minutes
  } catch (e) {
    if (refusedBusy(c.id, p, e)) return;
    p.failure = terminalErrorText(e);
    // The box is not showing, or a prompt came up before Enter: it shows as a card once read.
    if (e.code === 'not_at_input' || e.code === 'not_submitted') await readTerminal();
  } finally {
    sending.delete(c.id);
    if (chat === c) render();
  }
}

// answerPrompt presses one of the terminal prompt's options. A prompt that
// changed or went away collapses, not failed; the screen is read again either
// way, and the card collapses until that read.
async function answerPrompt(prompt, option) {
  const c = chat;
  const id = prompt.promptId;
  if (!c || !option || answering.has(id)) return;
  answering.add(id);
  answerErrors.delete(id);
  render();
  const slot = id + '/' + option.key;
  const key = c.pressKeys.get(slot) || newKey();
  c.pressKeys.set(slot, key);
  try {
    await api('POST', '/v1/sessions/' + encodeURIComponent(c.id) + '/terminal/keys', { promptId: id, option: option.key, key });
  } catch (e) {
    if (!(e.code === 'no_prompt' || e.code === 'prompt_changed')) {
      answerErrors.set(id, 'Could not answer: ' + terminalErrorText(e));
      answering.delete(id);
      if (chat === c) render();
      return;
    }
  }
  answering.delete(id);
  c.pressKeys.delete(slot);
  c.answeredPrompts.add(id);
  if (chat !== c) return;
  render();
  await readTerminal();
  // Collapsed only until the screen was read again: the same prompt still showing is asked again.
  c.answeredPrompts.delete(id);
  if (chat !== c) return;
  render();
  try { await tabs.model.refresh(); } catch (_) { /* the poll tries again */ }
  render();
}

// refusedBusy: a send refused 409 busy, with the queue: the message goes into the queue by itself
// and goes once Claude finishes. It stays shown, with its own Add to queue, only if the queue
// refuses it too, so it is never dropped.
function refusedBusy(session, p, e) {
  if (!sendRefusal(e) || !queueOn()) return false;
  const here = chat && chat.id === session;
  if (here) chat.busyOffer = true;
  p.failure = 'the session is busy';
  p.queueOffer = true;
  if (here) queueText(p.text, (c2) => { if (pendings.get(c2.id) === p) pendings.delete(c2.id); });
  else queueLeftChat(session, p);
  return true;
}

// queueLeftChat queues a message whose chat was left before its send was refused busy, through that
// session's own queue; refused there too, it stays that chat's failed message with Add to queue.
async function queueLeftChat(session, p) {
  const r = await createQueueClient(api, session, { newKey }).add(p.text);
  if (r.ok && pendings.get(session) === p) pendings.delete(session);
  if (r.ok && chat && chat.id === session) render();
}

// The chat queue: add from the composer (or from a refused message's offer); edit, move, delete
// and resume in its section (queueView.mjs).

function addToQueue() {
  return queueText($('prompt').value.trim(), null);
}

// queueText queues text. From the box (onQueued null) the box empties once it is queued; from an
// offer (a refused message, a lost edit) onQueued(chat) ends the offer and the box is left as is.
async function queueText(text, onQueued) {
  const c = chat;
  if (!c || !c.detail || !text || adding) return;
  if (!onQueued && pendingApproval && BARE_ANSWER.test(text)) {
    notice = 'Answer the permission request with one of its buttons.';
    render();
    return;
  }
  dictationGen++; // as for a send: a late dictation result must not refill the box
  Voice.stopListening();
  adding = true;
  notice = '';
  queueNotice = '';
  render();
  const r = await c.queueClient.add(text);
  adding = false;
  if (chat !== c) return;
  if (r.ok) {
    if (onQueued) onQueued(c);
    else {
      if ($('prompt').value.trim() === text) { $('prompt').value = ''; autoResize(); }
      drafts.delete(c.id);
    }
    if (r.queue) setQueue(c, r.queue);
    if (c.mode === 'terminal') { c.lastSend = Date.now(); termPoll.schedule(); } // read often while it may be sent
  } else {
    queueNotice = r.message; // the text stays where it was
  }
  render();
}

function sendError(e) {
  switch (e.code) {
    case 'busy': return 'a turn is already running';
    case 'in_use': return 'another aimux process is continuing this session';
    case 'open_in_terminal': return 'it is open in a terminal on your Mac';
    case 'ownership_unknown': return 'aimux cannot tell whether it is open in a terminal';
    default: return e.message;
  }
}

async function stop() {
  if (!chat) return;
  stopError = '';
  try {
    await api('POST', '/v1/sessions/' + encodeURIComponent(chat.id) + '/cancel');
  } catch (e) {
    // not_running: the server has no turn (it may have restarted), so this
    // view is out of date.
    if (e.code === 'not_running') reload();
    else { stopError = e.message; render(); }
  }
}

// Now stays fresh while a chat covers it, for the Next bar: on open, then every
// 15 s while the page is visible.
const nowPoll = poller({
  load: () => tabs.model.refresh(),
  render: () => render(),
  isActive: () => !!chat,
  isVisible: () => document.visibilityState === 'visible',
  every: 15000,
});

// A terminal session is read again every 2 s while Claude works there or for 2
// minutes after a message typed from here, else every 15 s; only while its
// chat is open and the page visible, one read at a time.
const termPoll = poller({
  load: readTerminal,
  render: () => render(),
  isActive: () => !!chat && chat.mode === 'terminal',
  isVisible: () => document.visibilityState === 'visible',
  every: () => terminalPollInterval({ busy: chat.detail.terminal.status === 'busy', lastSend: chat.lastSend, now: Date.now() }),
});

// stick records whether the reader is at the bottom, measured when they
// scroll, not after new content lands (a big card would otherwise look like
// the reader scrolled away).
let stick = true;
$('messages').addEventListener('scroll', () => {
  const box = $('messages');
  stick = box.scrollHeight - box.scrollTop - box.clientHeight < 120;
  $('jump').hidden = stick;
});
$('jump').addEventListener('click', () => { stick = true; scrollDown(); $('jump').hidden = true; });

function scrollDown() {
  const box = $('messages');
  if (stick) box.scrollTop = box.scrollHeight;
}

function autoResize() {
  const p = $('prompt');
  p.style.height = 'auto';
  p.style.height = Math.min(p.scrollHeight, 140) + 'px';
}

// Keep the screen on while a turn streams, so the phone does not sleep
// mid-reply.
async function requestWakeLock() {
  if (!('wakeLock' in navigator) || wakeLock) return;
  try {
    const lock = await navigator.wakeLock.request('screen');
    if (!running) { // the turn ended while the request was pending
      lock.release();
      return;
    }
    wakeLock = lock;
    lock.addEventListener('release', () => { if (wakeLock === lock) wakeLock = null; });
  } catch (_) { /* not allowed now (page hidden, battery saver) */ }
}

function releaseWakeLock() {
  if (wakeLock) { wakeLock.release(); wakeLock = null; }
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState !== 'visible') { nowPoll.stop(); termPoll.stop(); return; }
  if (running) requestWakeLock();
  if (chat) nowPoll.tick();
  termPoll.tick();
});

// Voice

// speakTurn reads the finished reply aloud, if the user asked for that, and
// in hands-free mode starts listening for the next message afterwards.
function speakTurn() {
  if (!Voice.settings().speak || !chat) return;
  const items = result.items;
  let from = items.length - 1;
  while (from >= 0 && items[from].kind !== 'user') from--;
  const turn = items.slice(from + 1);
  const steps = turn.filter((i) => i.kind === 'steps').flatMap((i) => i.steps);
  const failed = steps.filter((s) => s.status === 'failed').length;
  const summary = steps.length
    ? 'Ran ' + (steps.length === 1 ? 'one tool' : steps.length + ' tools') + (failed ? ', ' + failed + ' failed.' : '.')
    : '';
  const text = turn.filter((i) => i.kind === 'reply').map((i) => i.text).join('\n\n');
  const session = chat.id;
  dictationGen++; // a dictation in progress must not auto-send what it overhears
  Voice.speak(Voice.spoken(text, summary), () => listenAgain(session, Date.now()));
}

// listenAgain continues a hands-free conversation once the reply has been
// spoken, waiting (briefly) for the message box if the view is reloading.
function listenAgain(session, since) {
  const onScreen = location.hash === '#/s/' + encodeURIComponent(session);
  if (!Voice.settings().handsFree || !onScreen) return;
  if (!chat || running || !following) {
    if (Date.now() - since < 10000) setTimeout(() => listenAgain(session, since), 300);
    return;
  }
  dictate(true);
}

// dictate fills the message box from speech; with auto-send (or hands-free)
// it sends when the user stops talking.
// dictationGen invalidates a dictation when the user navigates away: its
// late result must not fill, or send, a message on another screen.
let dictationGen = 0;

function dictate(fromLoop) {
  const box = $('newsheet').hidden ? $('prompt') : $('newprompt');
  const mic = $('newsheet').hidden ? $('mic') : $('newmic');
  if (Voice.listening()) { Voice.stopListening(); return; }
  if (box.disabled) return;
  const base = box.value.trim() ? box.value.trim() + ' ' : '';
  const gen = ++dictationGen;
  const ok = Voice.listen({
    onText: (text) => {
      if (gen !== dictationGen) return;
      box.value = base + text;
      if (box === $('prompt')) { autoResize(); updateSend(); }
    },
    onEnd: (text, failed) => {
      mic.classList.remove('listening');
      if (gen !== dictationGen) return;
      setStatus('');
      // After a recognition error the text may be cut short: leave it in the
      // box to check rather than sending it.
      if (text && Voice.settings().autoSend && !failed) {
        (box === $('prompt') ? $('composer') : $('newform')).requestSubmit();
      }
    },
    onError: (err) => {
      mic.classList.remove('listening');
      if (err === 'not-allowed' || err === 'service-not-allowed') {
        setStatus(fromLoop ? 'tap the mic to talk' : 'microphone not allowed', 'error');
        if (box === $('prompt')) { notice = fromLoop ? 'Tap the mic to talk.' : 'The microphone is not allowed.'; render(); }
      }
    },
  });
  if (ok) {
    mic.classList.add('listening');
    setStatus('listening', 'ok');
  }
}

function setupVoice() {
  if (Voice.canListen) {
    $('newmic').hidden = false;
    $('mic').addEventListener('click', () => dictate(false));
    $('newmic').addEventListener('click', () => dictate(false));
  }
  if (!Voice.canListen && !Voice.canSpeak) return;
  $('voice').hidden = false;
  const menu = $('voicemenu');
  const sync = () => {
    const v = Voice.settings();
    menu.querySelectorAll('[data-voice]').forEach((c) => { c.checked = !!v[c.dataset.voice]; });
  };
  menu.querySelectorAll('[data-voice]').forEach((c) => {
    if (!Voice.canListen && c.dataset.voice !== 'speak') c.closest('label').hidden = true;
    c.addEventListener('change', () => {
      Voice.set(c.dataset.voice, c.checked);
      if (!c.checked && c.dataset.voice === 'speak') Voice.stopSpeaking();
      sync();
    });
  });
  $('voice').addEventListener('click', (e) => { e.stopPropagation(); sync(); menu.hidden = !menu.hidden; });
  document.addEventListener('click', (e) => { if (!menu.hidden && !menu.contains(e.target)) menu.hidden = true; });
}

// Routing: #/s/<id> is a session, #/new the new-session sheet, #/all and
// #/search those tabs, anything else Now.

function route() {
  dictationGen++; // a dictation in progress belongs to the screen being left
  Voice.stopListening();
  const m = location.hash.match(/^#\/s\/(.+)$/);
  if (m) showSession(decodeURIComponent(m[1]));
  else if (location.hash === '#/new') showNew();
  else showTabs(TAB_ROUTES[location.hash] ? location.hash : '#/');
}

$('chat-back').append(icon('back'));
$('chat-info').append(icon('info'));
$('mic').append(icon('mic'));
$('queue-add').append(icon('enqueue'));
$('queue-add').addEventListener('click', addToQueue);
$('stop').append(icon('stop'));
$('back').addEventListener('click', () => { location.hash = lastTab; });
$('chat-back').addEventListener('click', () => { location.hash = lastTab; });
$('chat-title').addEventListener('click', openInfo);
$('chat-info').addEventListener('click', openInfo);
$('newform').addEventListener('submit', startNew);
setupVoice();
$('refresh').addEventListener('click', () => { if (location.hash === '#/new') showNew(); });
$('composer').addEventListener('submit', send);
$('stop').addEventListener('click', stop);
$('prompt').addEventListener('input', () => {
  autoResize();
  if (queueNotice) { queueNotice = ''; render(); } // it was about the text before this change
  updateSend();
});
$('prompt').addEventListener('keydown', (e) => {
  // Enter sends on a keyboard; on a phone Enter stays a newline.
  // Not while an input method is composing: there Enter picks the characters.
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229 && !('ontouchstart' in window)) {
    e.preventDefault();
    $('composer').requestSubmit();
  }
});
window.addEventListener('hashchange', route);

// Pairing and the lock

function lockButton() {
  const b = el('button', 'round');
  b.type = 'button';
  b.setAttribute('aria-label', 'Lock');
  b.title = 'Lock';
  b.append(icon('lock'));
  b.addEventListener('click', () => login.lock());
  return b;
}

let loginShown = '';
let focusBefore = null; // what had focus when the overlay came up

// The heading of the view under the overlay, for focus when nothing else had it.
function viewHeading() {
  if (!$('session').hidden) return $('chat-title');
  if (!$('newsheet').hidden) return $('title');
  const h = document.querySelector('#tabs .screen:not([hidden]) .screen-title');
  if (h) h.tabIndex = -1;
  return h;
}

function renderLogin() {
  const s = login.state;
  const screen = s === 'unpaired' ? 'pair' : s === 'locked' ? 'lock' : '';
  if (screen && !loginShown) focusBefore = $('app').contains(document.activeElement) ? document.activeElement : null;
  $('login').hidden = !screen;
  $('app').inert = !!screen;
  $('pairscreen').hidden = screen !== 'pair';
  $('lockscreen').hidden = screen !== 'lock';
  $('revoked').hidden = !login.revoked;
  $('pairunlock').hidden = login.revoked;
  $('pairbutton').disabled = login.busy;
  $('pairunlock').disabled = login.busy;
  $('unlock').disabled = login.busy;
  $('pairbutton').textContent = login.busy && screen === 'pair' ? 'Pairing…' : 'Pair this device';
  $('unlock').textContent = login.busy ? 'Unlocking…' : 'Unlock with Face ID';
  $('pairerror').textContent = screen === 'pair' ? login.error : '';
  $('unlockerror').textContent = screen === 'lock' ? login.error : '';
  if (screen && screen !== loginShown) $(screen === 'pair' ? 'pairheading' : 'lockheading').focus();
  if (!screen && loginShown) {
    // Back where the person was, else the heading of the view they return to.
    const back = focusBefore && focusBefore.isConnected && focusBefore.offsetParent !== null ? focusBefore : viewHeading();
    if (back) back.focus({ preventScroll: true });
    focusBefore = null;
  }
  loginShown = screen;
}

login.subscribe(renderLogin);
$('pairform').addEventListener('submit', (e) => {
  e.preventDefault();
  login.pair($('paircode').value).then(() => { if (login.state === 'live') $('paircode').value = ''; });
});
$('unlock').addEventListener('click', () => login.unlock());
$('pairunlock').addEventListener('click', () => login.unlock());

login.start().then(() => tabs.rerender()); // the Lock button shows with device login
route();
