// aimux web client: list sessions, follow one, continue it and answer its
// approvals. Served by `aimux remote serve` at /app and talking to /v1 on
// the same origin; tailscale serve supplies the identity, so there is no
// login here.
'use strict';

const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
};

const STATE_LABEL = { idle: 'idle', busy: 'running', open_in_terminal: 'in terminal', unavailable: 'did not start' };

let current = null;      // {id, detail} of the open session
let events = null;       // EventSource for the open session
let turn = null;         // the turn being rendered: {div, text, textEl, tools, thinking}
let running = false;     // a turn is in progress
let listTimer = null;
let wakeLock = null;

// API

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(path, opts);
  let data = {};
  try { data = await r.json(); } catch (_) { /* empty or not JSON */ }
  if (!r.ok) {
    const err = new Error(data.error || ('HTTP ' + r.status));
    err.code = data.code || '';
    err.status = r.status;
    throw err;
  }
  return data;
}

function setStatus(text, cls) {
  const s = $('status');
  s.textContent = text || '';
  s.className = cls || '';
}

// Markdown, minimal and safe: everything is escaped first, then a few
// constructs become markup. Links only for http(s) URLs.

function escapeHtml(s) {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function inline(s) {
  return s
    .replace(/`([^`\n]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
}

function renderMarkdown(src) {
  const parts = escapeHtml(src).split(/^```[^\n]*\n?/m);
  let html = '';
  parts.forEach((part, i) => {
    if (i % 2 === 1) { // inside a fence
      html += '<pre><code>' + part.replace(/\n$/, '') + '</code></pre>';
      return;
    }
    for (const block of part.split(/\n{2,}/)) {
      if (!block.trim()) continue;
      const lines = block.split('\n');
      if (lines.every((l) => /^\s*[-*] /.test(l))) {
        html += '<ul>' + lines.map((l) => '<li>' + inline(l.replace(/^\s*[-*] /, '')) + '</li>').join('') + '</ul>';
      } else {
        html += '<p>' + lines.map((l) => {
          const h = l.match(/^#{1,6} (.*)/);
          return h ? '<span class="h">' + inline(h[1]) + '</span>' : inline(l);
        }).join('<br>') + '</p>';
      }
    }
  });
  return html;
}

// Session list

function ago(iso) {
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (!isFinite(s)) return '';
  if (s < 60) return 'now';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86400) return Math.floor(s / 3600) + 'h';
  return Math.floor(s / 86400) + 'd';
}

function dayGroup(iso) {
  const d = new Date(iso);
  const today = new Date(); today.setHours(0, 0, 0, 0);
  const t = d.getTime(), day = 86400000;
  if (t >= today.getTime()) return 'Today';
  if (t >= today.getTime() - day) return 'Yesterday';
  if (t >= today.getTime() - 6 * day) return 'This week';
  return 'Earlier';
}

// mark highlights the search snippet's [matched] words, after escaping.
function mark(snippet) {
  return escapeHtml(snippet || '').replace(/\[([^\]\n]{1,80})\]/g, '<mark>$1</mark>');
}

let query = '';
let searchTimer = null;
let lastList = null; // shown at once when a search is cleared, then refreshed

function showView(name) {
  $('listview').hidden = name !== 'list';
  $('newsheet').hidden = name !== 'new';
  $('session').hidden = name !== 'session';
  $('back').hidden = name === 'list';
}

async function showList() {
  if (current) drafts.set(current.id, $('prompt').value);
  closeSession();
  showView('list');
  $('state').hidden = true;
  setActivity('');
  $('title').textContent = 'aimux';
  document.title = 'aimux';
  await loadList();
  clearInterval(listTimer);
  listTimer = setInterval(() => { if (!query) loadList(); }, 30000);
}

async function loadList() {
  if (query) return runSearch();
  setStatus('loading');
  let data;
  try {
    data = await api('GET', '/v1/sessions?limit=100');
  } catch (e) {
    setStatus('offline', 'error');
    $('list').replaceChildren(el('div', 'empty', 'Could not load sessions: ' + e.message));
    return;
  }
  if (query) return; // the user started searching meanwhile
  lastList = data.sessions;
  setStatus('');
  if (!data.sessions.length) {
    $('list').replaceChildren(el('div', 'empty', 'No Claude sessions yet. Start one with +, or on your Mac.'));
    return;
  }
  renderSessions(data.sessions, true);
}

async function runSearch() {
  const q = query;
  setStatus('searching');
  let data;
  try {
    data = await api('GET', '/v1/search?q=' + encodeURIComponent(q) + '&limit=30');
  } catch (e) {
    if (q !== query) return;
    setStatus('');
    $('list').replaceChildren(el('div', 'empty', 'Search failed: ' + e.message));
    return;
  }
  if (q !== query) return; // a newer query is on its way
  setStatus('');
  if (!data.results.length) {
    $('list').replaceChildren(el('div', 'empty', 'No sessions match \u201c' + q + '\u201d.'));
    return;
  }
  renderSessions(data.results, false);
}

// renderSessions draws compact rows: a state dot, the title and when, then
// one dim line saying what the session was last asked (or, for a search,
// where it matched).
function renderSessions(sessions, grouped) {
  const out = [];
  let group = '';
  for (const s of sessions) {
    if (grouped && dayGroup(s.lastActive) !== group) {
      group = dayGroup(s.lastActive);
      out.push(el('div', 'group', group));
    }
    const item = el('button', 'session-item');
    item.type = 'button';
    const row = el('div', 'row');
    const dot = el('span', 'dot ' + s.state);
    dot.title = STATE_LABEL[s.state] || s.state;
    const project = (s.project || '').split('/').filter(Boolean).pop() || '';
    row.append(dot, el('span', 'name', s.title || s.id.slice(0, 8)), el('span', 'proj', project), el('span', 'ago', ago(s.lastActive)));
    item.append(row);
    if (s.snippet) {
      const sn = el('div', 'meta snippet');
      sn.innerHTML = mark(s.snippet);
      item.append(sn);
    } else if (s.lastPrompt && s.lastPrompt !== s.title) {
      item.append(el('div', 'meta', s.lastPrompt));
    }
    item.setAttribute('aria-label', (s.title || 'session') + ', ' + (STATE_LABEL[s.state] || s.state));
    item.addEventListener('click', () => { location.hash = '#/s/' + encodeURIComponent(s.id); });
    out.push(item);
  }
  $('list').replaceChildren(...out);
}

// New session: pick one of the recent project folders, say what to do.

let newProject = '';

async function showNew() {
  if (current) drafts.set(current.id, $('prompt').value);
  closeSession();
  clearInterval(listTimer);
  showView('new');
  $('state').hidden = true;
  setActivity('');
  $('title').textContent = 'New session';
  $('newerror').hidden = true;
  const box = $('projects');
  // Nothing to start in until this visit's projects are listed.
  newProject = '';
  $('newstart').disabled = true;
  box.replaceChildren(el('div', 'empty', 'Loading projects\u2026'));
  const gen = nav;
  let data;
  try {
    data = await api('GET', '/v1/projects');
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

// Session view

function closeSession() {
  nav++;
  clearTimeout(retryTimer);
  retryDelay = 2000;
  following = false;
  liveTurns = new Set();
  if (events) { events.close(); events = null; }
  current = null;
  turn = null;
  setRunning(false);
}

// nav counts navigations; a response for an older one is dropped, so a slow
// session can never take over the view (or the composer) of a newer one.
let nav = 0;
let retryTimer = null;
let following = false; // the event stream is open: replies will show
let liveTurns = new Set(); // turns this view saw start on the live stream

const drafts = new Map(); // session id -> unsent text, so a draft never goes to another session

async function showSession(id) {
  clearInterval(listTimer);
  if (current) drafts.set(current.id, $('prompt').value);
  closeSession();
  $('prompt').value = drafts.get(id) || '';
  autoResize();
  const gen = nav;
  showView('session');
  setActivity('');
  $('messages').replaceChildren();
  $('pending').replaceChildren();
  $('banner').hidden = true;
  $('banner').textContent = '';
  $('title').textContent = 'Loading\u2026';
  $('state').hidden = true;
  document.title = 'aimux';
  setComposer(false);
  setStatus('loading');
  await load(id, gen);
}

// load fetches the event snapshot first, then the history, then follows the
// stream from the snapshot's cursor. A turn that ends after the history was
// read is caught on the stream; see handle.
async function load(id, gen) {
  let page, detail;
  try {
    const eventsURL = '/v1/sessions/' + encodeURIComponent(id) + '/events?wait=0';
    for (let attempt = 0; ; attempt++) {
      page = await api('GET', eventsURL);
      if (gen !== nav) return;
      detail = await api('GET', '/v1/sessions/' + encodeURIComponent(id) + '?turns=30');
      if (gen !== nav) return;
      // A turn that started after the snapshot may already be in the history
      // and would then also arrive on the stream: read both again.
      const seen = page.events.length ? page.events[page.events.length - 1].seq : 0;
      const since = await api('GET', eventsURL + '&after=' + seen + '&log=' + encodeURIComponent(page.log || ''));
      if (gen !== nav) return;
      // The server restarted meanwhile (its log changed), or a turn started:
      // the snapshot no longer matches the history, so read both again.
      const changed = since.log !== page.log || since.events.some((e) => e.kind === 'turn_started');
      if (!changed || attempt >= 2) break;
    }
  } catch (e) {
    if (gen !== nav) return;
    if (e.status === 404) {
      setStatus('');
      $('title').textContent = 'aimux';
      system('No session with that id.', true);
      return;
    }
    retry(id, gen, 'Could not load the session: ' + e.message);
    return;
  }
  if (gen !== nav) return;
  current = { id, detail };
  $('messages').replaceChildren();
  $('pending').replaceChildren();
  $('title').textContent = detail.title || id.slice(0, 8);
  document.title = (detail.title || 'session') + ' \u00b7 aimux';
  showState(detail.state);

  // A turn still running when the snapshot was taken is replayed from the
  // events it still has, even if its start has aged out of the log. The
  // history may already hold its first part (the session file is written as
  // it goes); those turns, from its start on, are left to the replay.
  const evs = page.events || [];
  const last = evs[evs.length - 1];
  // The last event's turn is running unless it has ended: its end can come
  // before late events of its own (an approval resolved as the turn ended).
  // The running turn is the latest one that has not ended. It is not
  // always the last event's: an earlier turn's approval can resolve after a
  // newer turn started.
  const ended = (turn) => evs.some((e) => e.turn === turn && (e.kind === 'turn_done' || e.kind === 'turn_error'));
  let activeTurn = '';
  for (let i = evs.length - 1; i >= 0; i--) {
    if (!ended(evs[i].turn)) { activeTurn = evs[i].turn; break; }
  }
  const active = activeTurn ? evs.filter((e) => e.turn === activeTurn) : [];
  let turns = detail.turns || [];
  const lastTurn = turns[turns.length - 1];
  if (active.length && active[0].kind === 'turn_started') {
    // The running turn, if the file has it yet, is the file's latest turn
    // with this prompt, recorded after the turn started.
    if (lastTurn && samePrompt(lastTurn.user, active[0].data.text) &&
        new Date(lastTurn.time).getTime() >= new Date(active[0].time).getTime() - 2000) {
      turns = turns.slice(0, -1);
    }
  }
  // If the running turn's start aged out of the log, its earlier part is
  // only in the history: keep that turn, and the retained events continue
  // it below. When the turn ends the view is redrawn from history (a turn
  // joined part way), so any overlap is brief.
  const ends = renderHistory(turns);
  // The history has no errors: a failed turn's error comes from the event
  // log, placed after that turn (the first history turn with its prompt
  // recorded after it started), or with its prompt at the end if the agent
  // never wrote it.
  // Its start may have aged out of the log (a long turn); then the error
  // belongs to the latest history turn recorded before it.
  const claimed = new Set();
  const ms = (x) => new Date(x).getTime();
  for (const failed of evs) {
    if (failed.kind !== 'turn_error') continue;
    const started = evs.find((x) => x.turn === failed.turn && x.kind === 'turn_started');
    const end = ms(failed.time) + 2000;
    let i = -1;
    if (started) {
      const from = ms(started.time) - 2000;
      i = turns.findIndex((t, j) => !claimed.has(j) && samePrompt(t.user, started.data.text) && ms(t.time) >= from && ms(t.time) <= end);
    } else {
      for (let j = turns.length - 1; j >= 0; j--) {
        if (!claimed.has(j) && ms(turns[j].time) <= end) { i = j; break; }
      }
    }
    const note = el('div', 'msg system error', failed.data.error || 'The turn failed.');
    if (i >= 0) {
      claimed.add(i);
      ends[i].after(note);
    } else if (started) {
      // Never written to the file: place it in time among the turns shown,
      // and leave it out if it is older than all of them.
      const t0 = ms(started.time);
      if (turns.length && t0 < ms(turns[0].time)) continue;
      const k = turns.findIndex((t) => ms(t.time) > t0);
      const prompt = el('div', 'msg user', started.data.text || '');
      if (k > 0) ends[k - 1].after(prompt, note);
      else $('messages').append(prompt, note);
    } else if (!turns.length || ms(failed.time) >= ms(turns[0].time)) {
      $('messages').append(note);
    }
  }
  for (const e of active) handle(e, false);
  stream(id, gen, last ? last.seq : 0, page.log);
}

function stream(id, gen, after, log) {
  if (gen !== nav) return;
  // The log id scopes the cursor: if the server restarted since the
  // snapshot, it replays its new log instead of hiding it behind our cursor.
  events = new EventSource('/v1/sessions/' + encodeURIComponent(id) + '/events?after=' + after + '&log=' + encodeURIComponent(log || ''));
  let dropped = false;
  events.onopen = () => {
    if (dropped) { refresh(); return; } // the server may have restarted: reload its state
    following = true;
    retryDelay = 2000;
    setStatus($('banner').textContent ? 'read only' : 'live', $('banner').textContent ? '' : 'ok');
    setComposer(!$('banner').textContent);
  };
  events.onerror = () => {
    dropped = true;
    following = false;
    setComposer(false);
    if (events && events.readyState === EventSource.CLOSED) {
      // The browser gave up (the server refused or is gone): start over.
      events = null;
      retry(id, gen, 'Lost the connection to your Mac.');
    } else {
      setStatus('reconnecting', 'error');
    }
  };
  for (const kind of ['turn_started', 'update', 'approval', 'approval_resolved', 'turn_done', 'turn_error']) {
    events.addEventListener(kind, (msg) => {
      if (gen !== nav) return;
      try { handle(JSON.parse(msg.data), true); } catch (err) { console.error(err); }
    });
  }
}

// samePrompt matches a history turn's prompt (whitespace-folded, perhaps
// cut short with an ellipsis) to the text a turn was started with.
function samePrompt(history, text) {
  const fold = (x) => (x || '').replace(/\s+/g, ' ').trim();
  const h = fold(history).replace(/\u2026$/, '');
  return h !== '' && fold(text).startsWith(h);
}

// retry reloads the session with backoff while the view still shows it;
// sending stays off meanwhile, since a reply could not be shown.
let retryDelay = 2000;
function retry(id, gen, why) {
  setStatus('offline, retrying', 'error');
  setComposer(false);
  if (!$('messages').childElementCount) system(why, true);
  clearTimeout(retryTimer);
  retryTimer = setTimeout(() => {
    if (gen !== nav) return;
    retryDelay = Math.min(retryDelay * 2, 30000);
    load(id, gen);
  }, retryDelay);
}

function showState(state) {
  if (current) current.detail.state = state;
  const s = $('state');
  s.hidden = false;
  s.className = 'dot ' + state;
  s.title = STATE_LABEL[state] || state;
  const inTerminal = state === 'open_in_terminal';
  const unavailable = state === 'unavailable';
  const readOnly = inTerminal || unavailable;
  $('mic').hidden = readOnly || !Voice.canListen;
  $('prompt').placeholder = readOnly ? 'Read only here' : 'Message this session';
  $('banner').hidden = !readOnly;
  $('banner').textContent = inTerminal
    ? 'This session is open in a terminal on your Mac, so it is read only here. Close it there (or let it finish) to continue from this device.'
    : unavailable
      ? 'This session never started: its first message failed before Claude saved it. Start a new session with +.'
      : '';
  setComposer(!readOnly);
}

function setComposer(enabled) {
  $('prompt').disabled = !enabled;
  $('send').disabled = !enabled || running || !following;
}

// refresh redraws the open session from the server: used when a turn this
// view joined part way through ends, so its full text comes from history.
function refresh() {
  if (current) showSession(current.id);
}

// renderHistory draws the turns and returns each one's last element, so
// notes about a turn (its error) can be placed right after it. Each turn is
// its prompt, one collapsed line for its tools, and its reply, cut to a few
// lines until tapped.
function renderHistory(turns) {
  const box = $('messages');
  const ends = [];
  if (turns.length) box.append(el('div', 'earlier', turns.length === 1 ? 'Latest turn' : 'Latest ' + turns.length + ' turns'));
  for (const t of turns) {
    if (t.user) box.append(el('div', 'msg user', t.user));
    if (t.reply || (t.tools && t.tools.length)) {
      const div = el('div', 'msg assistant');
      if (t.tools && t.tools.length) {
        const tools = toolsBlock();
        for (const tool of t.tools) addTool(tools, tool.ok ? 'completed' : 'failed', toolLabel(tool.name, tool.snippet));
        div.append(tools);
      }
      if (t.reply) {
        const text = el('div', 'text');
        text.innerHTML = renderMarkdown(t.reply);
        div.append(text);
        clamp(div, text);
      }
      box.append(div);
    }
    ends.push(box.lastElementChild);
  }
  scrollDown(true);
  return ends;
}

// toolLabel is a tool and its target: a bare path shortened to its file name.
function toolLabel(name, snippet) {
  let target = (snippet || '').split('\n')[0];
  if (/^\/\S+$/.test(target)) target = target.split('/').pop();
  if (target.length > 60) target = target.slice(0, 60) + '\u2026';
  return target ? name + ' ' + target : name;
}

// toolsBlock is a turn's tools, collapsed to one summary line.
function toolsBlock() {
  const d = el('details', 'tools');
  d.append(el('summary'), el('div', 'list'));
  return d;
}

function addTool(block, status, label) {
  const line = el('div', 'tool ' + status, label);
  block.lastChild.append(line);
  summarize(block);
  return line;
}

// summarize writes "N tools: last ✓ ✗" so a turn's work reads at a glance.
function summarize(block) {
  const lines = [...block.lastChild.children];
  const failed = lines.filter((l) => l.classList.contains('failed')).length;
  const last = lines[lines.length - 1];
  const head = lines.length === 1 ? '1 tool' : lines.length + ' tools';
  block.firstChild.textContent = head + (failed ? ' \u00b7 ' + failed + ' failed' : '') + (last ? ' \u00b7 ' + last.textContent : '');
  block.classList.toggle('has-failed', failed > 0);
}

// clamp cuts a long reply to a few lines, with a tap to read it all.
function clamp(div, text) {
  if ((text.textContent || '').length < 500 && text.querySelectorAll('p, li, pre').length < 6) return;
  text.classList.add('clamped');
  const more = el('button', 'more', 'more');
  more.type = 'button';
  more.addEventListener('click', () => {
    const open = text.classList.toggle('clamped');
    more.textContent = open ? 'more' : 'less';
  });
  div.append(more);
}

function setActivity(text) {
  const a = $('activity');
  a.textContent = text;
  a.hidden = !text;
}

function handle(e, live) {
  const d = e.data || {};
  switch (e.kind) {
    case 'turn_started':
      if (live) liveTurns.add(e.turn);
      if (current && unsettled.has(current.id) && unsettled.get(current.id).text === d.text) {
        // The send that looked failed did reach the server.
        unsettled.delete(current.id);
        if ($('prompt').value.trim() === d.text) { $('prompt').value = ''; autoResize(); }
      }
      $('messages').append(el('div', 'msg user', d.text || ''));
      startTurn();
      break;
    case 'update':
      update(d);
      break;
    case 'approval':
      approvalCard(d);
      if (live && Voice.settings().speak) {
        dictationGen++;
        Voice.speak('The agent asks for permission: ' + (d.title || 'a tool call') + '. Tap one of the options on screen.');
      }
      break;
    case 'approval_resolved':
      resolveCard(d.approvalId, d.outcome);
      break;
    case 'turn_done': {
      const done = turn;
      endTurn();
      // Spoken first: a turn joined part way (a new session's first reply)
      // has its full text from the replay plus the stream.
      if (live && d.stopReason !== 'cancelled') speakTurn(done);
      if (live && !liveTurns.has(e.turn)) { refresh(); return; }
      if (d.stopReason === 'cancelled') system('Stopped.');
      else if (d.stopReason && d.stopReason !== 'end_turn') system('Turn ended: ' + d.stopReason);
      break;
    }
    case 'turn_error':
      endTurn();
      if (live && !liveTurns.has(e.turn)) { refresh(); return; }
      system(d.error || 'The turn failed.', true);
      if (live && Voice.settings().speak) {
        dictationGen++; // as for any read-aloud: a dictation in progress must not auto-send
        Voice.speak('The turn failed.');
      }
      break;
  }
  scrollDown();
}

function startTurn() {
  const div = el('div', 'msg assistant streaming');
  const textEl = el('div', 'text');
  div.append(textEl);
  $('messages').append(div);
  turn = { div, text: '', textEl, tools: {}, toolsEl: null, thinking: null, planEl: null, pending: false };
  setRunning(true);
  setActivity('thinking');
}

function ensureTurn() {
  if (!turn) startTurn();
  return turn;
}

function endTurn() {
  if (turn) {
    turn.div.classList.remove('streaming');
    flushText(turn);
    if (turn.text) clamp(turn.div, turn.textEl);
    if (!turn.text && !turn.toolsEl && !turn.thinking && !turn.planEl) turn.div.remove();
  }
  turn = null;
  setRunning(false);
}

function flushText(t) {
  t.pending = false;
  t.textEl.innerHTML = renderMarkdown(t.text);
}

function update(u) {
  const t = ensureTurn();
  switch (u.sessionUpdate) {
    case 'agent_message_chunk':
      if (u.content && u.content.type === 'text') {
        if (!t.text) setActivity('writing');
        t.text += u.content.text;
        if (!t.pending) { // re-render at most once a frame
          t.pending = true;
          requestAnimationFrame(() => { flushText(t); scrollDown(); });
        }
      }
      break;
    case 'agent_thought_chunk':
      if (u.content && u.content.type === 'text') {
        if (!t.thinking) {
          const d = el('details', 'thinking');
          d.append(el('summary', '', 'thinking'), el('div'));
          t.div.insertBefore(d, t.textEl);
          t.thinking = d;
        }
        t.thinking.lastChild.textContent += u.content.text;
      }
      break;
    case 'tool_call': {
      if (!t.toolsEl) { t.toolsEl = toolsBlock(); t.div.insertBefore(t.toolsEl, t.textEl); }
      const title = u.title || u.kind || 'tool';
      t.tools[u.toolCallId] = addTool(t.toolsEl, u.status || 'pending', title);
      setActivity(title);
      break;
    }
    case 'tool_call_update': {
      const line = t.tools[u.toolCallId];
      if (!line) break;
      if (u.status) line.className = 'tool ' + u.status;
      if (u.title) line.textContent = u.title;
      summarize(t.toolsEl);
      if (u.status === 'in_progress' || u.title) setActivity(line.textContent);
      break;
    }
    case 'plan': {
      const ul = el('ul', 'plan');
      for (const p of u.entries || []) ul.append(el('li', p.status || '', p.content || ''));
      if (t.planEl) t.planEl.replaceWith(ul); else t.div.insertBefore(ul, t.textEl);
      t.planEl = ul;
      break;
    }
  }
}

// Approvals

const OPTION_CLASS = { allow_once: 'allow', allow_always: 'always' };
const BARE_ANSWER = /^(yes|no|yeah|nope|ok|okay|allow|deny|sure)[.!]?$/i;

// toolPreview shows what the agent wants to run or write, from the request's
// raw tool input, shortened for a phone.
function toolPreview(call) {
  const input = call && call.rawInput;
  if (!input || typeof input !== 'object') return '';
  const text = input.command || input.content || input.new_string || input.url || input.pattern || '';
  const where = input.file_path || input.path || '';
  const out = [where, typeof text === 'string' ? text : JSON.stringify(text)].filter(Boolean).join('\n');
  return out.length > 400 ? out.slice(0, 400) + '\u2026' : out;
}

function approvalCard(d) {
  const card = el('div', 'approval');
  card.dataset.approval = d.approvalId;
  card.append(el('div', 'ask', 'The agent asks for permission'), el('div', 'what', d.title || 'a tool call'));
  const preview = toolPreview(d.toolCall);
  if (preview) card.append(el('pre', 'preview', preview));
  const opts = el('div', 'options');
  for (const o of d.options || []) {
    const b = el('button', OPTION_CLASS[o.kind] || '', o.name || o.optionId);
    b.type = 'button';
    b.addEventListener('click', () => answer(card, d.approvalId, o.optionId));
    opts.append(b);
  }
  card.append(opts, el('div', 'outcome'));
  card.dataset.title = d.title || 'a tool call';
  $('pending').append(card); // pinned above the composer until answered
  setActivity('waiting for you');
  if (navigator.vibrate) navigator.vibrate(80);
  return card;
}

async function answer(card, id, option) {
  card.querySelectorAll('button').forEach((b) => { b.disabled = true; });
  try {
    await api('POST', '/v1/approvals/' + encodeURIComponent(id), { option });
  } catch (e) {
    card.querySelector('.outcome').textContent = e.code === 'no_such_approval'
      ? 'This request is no longer waiting for an answer.'
      : 'Could not answer: ' + e.message;
    if (e.code !== 'no_such_approval') card.querySelectorAll('button').forEach((b) => { b.disabled = false; });
  }
}

// resolveCard takes an answered request out of the pinned area and leaves a
// one-line record of it in the conversation.
function resolveCard(id, outcome) {
  const card = document.querySelector('[data-approval="' + CSS.escape(id) + '"]');
  if (!card) return;
  const word = { allowed: 'Allowed', denied: 'Denied', expired: 'Expired (denied, no answer)' }[outcome] || outcome;
  const note = el('div', 'decision ' + outcome, word + ': ' + card.dataset.title);
  card.remove();
  // A bare yes or no typed or dictated for the request is spent with it;
  // left in the box, one tap would send it to the agent as a message.
  if (BARE_ANSWER.test($('prompt').value.trim())) { $('prompt').value = ''; autoResize(); }
  (turn ? turn.div : $('messages')).append(note);
  if (running) setActivity('thinking');
}

// Sending and stopping

const sending = new Set(); // sessions with a message POST in flight: Enter must not send it again
// unsettled is a failed send: its outcome may be unknown (the response was
// lost), so a retry of the same text reuses its key and the server runs it
// at most once. Reusing a key the server never saw is harmless.
const unsettled = new Map(); // session id -> {text, key}

async function send(ev) {
  ev.preventDefault();
  const text = $('prompt').value.trim();
  if (!text || !current || sending.has(current.id) || !following) return;
  if ($('pending').childElementCount && BARE_ANSWER.test(text)) {
    // A bare yes or no while a permission request waits is meant for the
    // request; answering it takes a tap on one of its options.
    system('Answer the permission request with one of its buttons.', true);
    return;
  }
  if (running) return;
  // Sending ends any dictation: a late result must not refill the box or
  // send the same words again.
  dictationGen++;
  Voice.stopListening();
  Voice.stopSpeaking();
  stick = true;
  scrollDown(true);
  const gen = nav;
  const session = current.id;
  const prior = unsettled.get(session);
  const key = prior && prior.text === text ? prior.key : newKey();
  sending.add(session);
  $('send').disabled = true;
  try {
    await api('POST', '/v1/sessions/' + encodeURIComponent(session) + '/messages', { text, key });
    unsettled.delete(session);
    // Sent text is no draft, wherever it is now (saved, or back on screen
    // after the user left and returned); anything typed since is kept.
    if ((drafts.get(session) || '').trim() === text) drafts.delete(session);
    if (current && current.id === session && $('prompt').value.trim() === text) {
      $('prompt').value = '';
      autoResize();
    }
  } catch (e) {
    unsettled.set(session, { text, key });
    if (gen !== nav) return;
    if (e.code === 'open_in_terminal') showState('open_in_terminal');
    system(sendError(e), true);
  } finally {
    sending.delete(session);
    if (gen === nav) setComposer(!$('banner').textContent);
  }
}

function sendError(e) {
  switch (e.code) {
    case 'busy': return 'A turn is already running in this session. Wait for it to finish.';
    case 'in_use': return 'Another aimux process (aimux chat?) is continuing this session.';
    case 'open_in_terminal': return 'This session is open in a terminal on your Mac.';
    case 'ownership_unknown': return 'aimux cannot tell whether this session is open in a terminal, so it will not send. ' + e.message;
    default: return 'Could not send: ' + e.message;
  }
}

async function stop() {
  if (!current) return;
  try {
    await api('POST', '/v1/sessions/' + encodeURIComponent(current.id) + '/cancel');
  } catch (e) {
    // not_running: the server has no turn (it may have restarted), so this
    // view is out of date.
    if (e.code === 'not_running') refresh();
    else system('Could not stop: ' + e.message, true);
  }
}

function newKey() {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}

function setRunning(on) {
  running = on;
  if (current) {
    const base = current.detail.state === 'open_in_terminal' ? 'open_in_terminal' : (on ? 'busy' : 'idle');
    const s = $('state');
    s.hidden = false;
    s.className = 'dot ' + base;
    s.title = STATE_LABEL[base];
  }
  if (!on) setActivity('');
  $('stop').hidden = !on;
  $('send').hidden = on;
  setComposer(!!current && !$('banner').textContent);
  if (on) requestWakeLock(); else releaseWakeLock();
}

// Helpers

function system(text, isError) {
  $('messages').append(el('div', 'msg system' + (isError ? ' error' : ''), text));
  scrollDown(true);
}

// stick records whether the reader is at the bottom, measured when they
// scroll, not after new content lands (a big card would otherwise look like
// the reader scrolled away).
let stick = true;
$('messages').addEventListener('scroll', () => {
  const box = $('messages');
  stick = box.scrollHeight - box.scrollTop - box.clientHeight < 120;
  $('jump').hidden = stick;
});
$('jump').addEventListener('click', () => { stick = true; scrollDown(true); $('jump').hidden = true; });

function scrollDown(force) {
  const box = $('messages');
  if (force || stick) box.scrollTop = box.scrollHeight;
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
  if (document.visibilityState === 'visible' && running) requestWakeLock();
});

// Voice

// speakTurn reads a finished reply aloud, if the user asked for that, and
// in hands-free mode starts listening for the next message afterwards.
function speakTurn(t) {
  const v = Voice.settings();
  if (!v.speak || !t) return;
  const lines = t.toolsEl ? [...t.toolsEl.lastChild.children] : [];
  const failed = lines.filter((l) => l.classList.contains('failed')).length;
  const summary = lines.length
    ? 'Ran ' + (lines.length === 1 ? 'one tool' : lines.length + ' tools') + (failed ? ', ' + failed + ' failed.' : '.')
    : '';
  const session = current && current.id;
  dictationGen++; // a dictation in progress must not auto-send what it overhears
  Voice.speak(Voice.spoken(t.text, summary), () => listenAgain(session, Date.now()));
}

// listenAgain continues a hands-free conversation once the reply has been
// spoken, waiting (briefly) for the message box if the view is reloading.
function listenAgain(session, since) {
  // The address says which session is on screen; current is briefly empty
  // while that same session reloads (a turn joined part way, just ended).
  const onScreen = location.hash === '#/s/' + encodeURIComponent(session);
  if (!Voice.settings().handsFree || !onScreen) return;
  if (!current || running || $('prompt').disabled || !following) {
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
      if (box === $('prompt')) autoResize();
    },
    onEnd: (text, failed) => {
      mic.classList.remove('listening');
      if (gen !== dictationGen) return;
      setStatus(following ? 'live' : '', following ? 'ok' : '');
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
    $('mic').hidden = false;
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

// Routing: #/s/<id> is a session, #/new the new-session sheet, anything
// else the list.

function route() {
  dictationGen++; // a dictation in progress belongs to the screen being left
  Voice.stopListening();
  const m = location.hash.match(/^#\/s\/(.+)$/);
  if (m) showSession(decodeURIComponent(m[1]));
  else if (location.hash === '#/new') showNew();
  else showList();
}

$('back').addEventListener('click', () => { location.hash = ''; });
$('newbtn').addEventListener('click', () => { location.hash = '#/new'; });
$('newform').addEventListener('submit', startNew);
$('search').addEventListener('input', () => {
  clearTimeout(searchTimer);
  const q = $('search').value.trim();
  if (!q && lastList) { // back to the list: show it now, refresh behind it
    query = '';
    renderSessions(lastList, true);
  }
  searchTimer = setTimeout(() => {
    query = q;
    loadList();
  }, q ? 300 : 0);
});
setupVoice();
$('refresh').addEventListener('click', () => {
  const m = location.hash.match(/^#\/s\/(.+)$/);
  if (m) showSession(decodeURIComponent(m[1]));
  else if (location.hash === '#/new') showNew();
  else loadList();
});
$('composer').addEventListener('submit', send);
$('stop').addEventListener('click', stop);
$('prompt').addEventListener('input', autoResize);
$('prompt').addEventListener('keydown', (e) => {
  // Enter sends on a keyboard; on a phone Enter stays a newline.
  // Not while an input method is composing: there Enter picks the characters.
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229 && !('ontouchstart' in window)) {
    e.preventDefault();
    $('composer').requestSubmit();
  }
});
window.addEventListener('hashchange', route);
route();
