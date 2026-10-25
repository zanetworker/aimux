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

const STATE_LABEL = { idle: 'idle', busy: 'running', open_in_terminal: 'in terminal' };

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
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

function badge(state) {
  const b = el('span', 'badge ' + state, STATE_LABEL[state] || state);
  return b;
}

async function showList() {
  closeSession();
  $('session').hidden = true;
  $('list').hidden = false;
  $('back').hidden = true;
  $('state').hidden = true;
  $('title').textContent = 'aimux';
  document.title = 'aimux';
  await loadList();
  clearInterval(listTimer);
  listTimer = setInterval(loadList, 30000);
}

async function loadList() {
  setStatus('loading');
  let data;
  try {
    data = await api('GET', '/v1/sessions?limit=100');
  } catch (e) {
    setStatus('offline', 'error');
    const list = $('list');
    list.replaceChildren(el('div', 'empty', 'Could not load sessions: ' + e.message));
    return;
  }
  setStatus('');
  const list = $('list');
  if (!data.sessions.length) {
    list.replaceChildren(el('div', 'empty', 'No Claude sessions yet. Sessions you start on your Mac show up here.'));
    return;
  }
  list.replaceChildren(...data.sessions.map((s) => {
    const item = el('button', 'session-item');
    item.type = 'button';
    const row = el('div', 'row');
    row.append(el('span', 'name', s.title || s.id.slice(0, 8)), badge(s.state));
    const project = (s.project || '').split('/').filter(Boolean).pop() || '';
    item.append(row, el('div', 'meta', [project, ago(s.lastActive)].filter(Boolean).join(' · ')));
    item.addEventListener('click', () => { location.hash = '#/s/' + encodeURIComponent(s.id); });
    return item;
  }));
}

// Session view

function closeSession() {
  if (events) { events.close(); events = null; }
  current = null;
  turn = null;
  setRunning(false);
}

async function showSession(id) {
  clearInterval(listTimer);
  closeSession();
  $('list').hidden = true;
  $('session').hidden = false;
  $('back').hidden = false;
  $('messages').replaceChildren();
  $('banner').hidden = true;
  $('banner').textContent = '';
  $('title').textContent = 'Loading\u2026';
  $('state').hidden = true;
  document.title = 'aimux';
  setComposer(false);
  setStatus('loading');
  let detail;
  try {
    detail = await api('GET', '/v1/sessions/' + encodeURIComponent(id) + '?turns=30');
  } catch (e) {
    setStatus('');
    $('title').textContent = 'aimux';
    system(e.status === 404 ? 'No session with that id.' : 'Could not load the session: ' + e.message, true);
    setComposer(false);
    return;
  }
  current = { id, detail };
  $('title').textContent = detail.title || id.slice(0, 8);
  document.title = (detail.title || 'session') + ' · aimux';
  showState(detail.state);
  renderHistory(detail.turns || []);
  await follow(id);
}

function showState(state) {
  if (current) current.detail.state = state;
  const s = $('state');
  s.hidden = false;
  s.className = 'badge ' + state;
  s.textContent = STATE_LABEL[state] || state;
  const inTerminal = state === 'open_in_terminal';
  $('banner').hidden = !inTerminal;
  $('banner').textContent = inTerminal
    ? 'This session is open in a terminal on your Mac, so it is read only here. Close it there (or let it finish) to continue from this device.'
    : '';
  setComposer(!inTerminal);
}

function setComposer(enabled) {
  $('prompt').disabled = !enabled;
  $('send').disabled = !enabled || running;
}

function renderHistory(turns) {
  const box = $('messages');
  if (turns.length) box.append(el('div', 'earlier', 'Latest ' + turns.length + ' turns'));
  for (const t of turns) {
    if (t.user) box.append(el('div', 'msg user', t.user));
    if (t.reply || (t.tools && t.tools.length)) {
      const div = el('div', 'msg assistant');
      if (t.reply) {
        const text = el('div', 'text');
        text.innerHTML = renderMarkdown(t.reply);
        div.append(text);
      }
      if (t.tools && t.tools.length) {
        const tools = el('div', 'tools');
        for (const tool of t.tools) {
          let target = (tool.snippet || '').split('\n')[0];
          if (/^\/\S+$/.test(target)) target = target.split('/').pop(); // a bare path: its file name

          tools.append(el('div', 'tool ' + (tool.ok ? 'completed' : 'failed'), target ? tool.name + ' ' + target : tool.name));
        }
        div.append(tools);
      }
      box.append(div);
    }
  }
  scrollDown(true);
}

// follow opens the session's event stream. A turn already running (started
// from another device or tab) is replayed from its start; finished turns are
// in the history above.
async function follow(id) {
  let page;
  try {
    page = await api('GET', '/v1/sessions/' + encodeURIComponent(id) + '/events?wait=0');
  } catch (e) {
    setStatus('offline', 'error');
    return;
  }
  const evs = page.events || [];
  let after = evs.length ? evs[evs.length - 1].seq : 0;
  let start = -1;
  for (let i = evs.length - 1; i >= 0; i--) {
    if (evs[i].kind === 'turn_done' || evs[i].kind === 'turn_error') break;
    if (evs[i].kind === 'turn_started') { start = i; break; }
  }
  if (start >= 0) {
    for (const e of evs.slice(start)) handle(e);
  }
  if (!current || current.id !== id) return; // navigated away meanwhile
  events = new EventSource('/v1/sessions/' + encodeURIComponent(id) + '/events?after=' + after);
  events.onopen = () => setStatus('live', 'ok');
  events.onerror = () => {
    if (events && events.readyState === EventSource.CLOSED) setStatus('disconnected', 'error');
    else setStatus('reconnecting', 'error');
  };
  for (const kind of ['turn_started', 'update', 'approval', 'approval_resolved', 'turn_done', 'turn_error']) {
    events.addEventListener(kind, (msg) => {
      try { handle(JSON.parse(msg.data)); } catch (err) { console.error(err); }
    });
  }
}

function handle(e) {
  const d = e.data || {};
  switch (e.kind) {
    case 'turn_started':
      $('messages').append(el('div', 'msg user', d.text || ''));
      startTurn();
      break;
    case 'update':
      update(d);
      break;
    case 'approval':
      approvalCard(d);
      break;
    case 'approval_resolved':
      resolveCard(d.approvalId, d.outcome);
      break;
    case 'turn_done':
      endTurn();
      if (d.stopReason === 'cancelled') system('Stopped.');
      else if (d.stopReason && d.stopReason !== 'end_turn') system('Turn ended: ' + d.stopReason);
      break;
    case 'turn_error':
      endTurn();
      system(d.error || 'The turn failed.', true);
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
}

function ensureTurn() {
  if (!turn) startTurn();
  return turn;
}

function endTurn() {
  if (turn) {
    turn.div.classList.remove('streaming');
    flushText(turn);
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
      if (!t.toolsEl) { t.toolsEl = el('div', 'tools'); t.div.append(t.toolsEl); }
      const line = el('div', 'tool ' + (u.status || 'pending'), u.title || u.kind || 'tool');
      t.tools[u.toolCallId] = line;
      t.toolsEl.append(line);
      break;
    }
    case 'tool_call_update': {
      const line = t.tools[u.toolCallId];
      if (!line) break;
      if (u.status) line.className = 'tool ' + u.status;
      if (u.title) line.textContent = u.title;
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
  $('messages').append(card);
  if (navigator.vibrate) navigator.vibrate(80);
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

function resolveCard(id, outcome) {
  const card = document.querySelector('[data-approval="' + CSS.escape(id) + '"]');
  if (!card) return;
  card.classList.add('resolved');
  card.querySelectorAll('button').forEach((b) => { b.disabled = true; });
  card.querySelector('.outcome').textContent = { allowed: 'Allowed', denied: 'Denied', expired: 'Expired: denied after no answer' }[outcome] || outcome;
}

// Sending and stopping

async function send(ev) {
  ev.preventDefault();
  const text = $('prompt').value.trim();
  if (!text || !current || running) return;
  $('send').disabled = true;
  try {
    await api('POST', '/v1/sessions/' + encodeURIComponent(current.id) + '/messages', { text, key: newKey() });
    $('prompt').value = '';
    autoResize();
  } catch (e) {
    if (e.code === 'open_in_terminal') showState('open_in_terminal');
    system(sendError(e), true);
  } finally {
    setComposer(current && !$('prompt').disabled);
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
    if (e.code !== 'not_running') system('Could not stop: ' + e.message, true);
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
    s.className = 'badge ' + base;
    s.textContent = STATE_LABEL[base];
  }
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

function scrollDown(force) {
  const box = $('messages');
  const near = box.scrollHeight - box.scrollTop - box.clientHeight < 120;
  if (force || near) box.scrollTop = box.scrollHeight;
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
    wakeLock = await navigator.wakeLock.request('screen');
    wakeLock.addEventListener('release', () => { wakeLock = null; });
  } catch (_) { /* not allowed now (page hidden, battery saver) */ }
}

function releaseWakeLock() {
  if (wakeLock) { wakeLock.release(); wakeLock = null; }
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && running) requestWakeLock();
});

// Routing: #/s/<id> is a session, anything else the list.

function route() {
  const m = location.hash.match(/^#\/s\/(.+)$/);
  if (m) showSession(decodeURIComponent(m[1]));
  else showList();
}

$('back').addEventListener('click', () => { location.hash = ''; });
$('refresh').addEventListener('click', () => { if (current) showSession(current.id); else loadList(); });
$('composer').addEventListener('submit', send);
$('stop').addEventListener('click', stop);
$('prompt').addEventListener('input', autoResize);
$('prompt').addEventListener('keydown', (e) => {
  // Enter sends on a keyboard; on a phone Enter stays a newline.
  if (e.key === 'Enter' && !e.shiftKey && !('ontouchstart' in window)) {
    e.preventDefault();
    $('composer').requestSubmit();
  }
});
window.addEventListener('hashchange', route);
route();
