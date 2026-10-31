import { allSections } from './all.mjs';
import { folderTree } from './folders.mjs';
import { NowModel } from './nowModel.mjs';
import { announce } from './sessionInfo.mjs';
import { renderNow } from './nowView.mjs';
import { conversationSearch, queryWords, searchSessions } from './search.mjs';
import {
  displayTitle, dotRow, el, errorBanner, folderKey, foldLine, icon, isOpenChoice, rowPlace, saveChoice,
  screenHeader, searchBox, section, sectionLabel, sessionLine,
} from './rows.mjs';

export { folderKey, isOpenChoice, saveChoice, tabKey } from './rows.mjs';

// The Now and All screens and the tab bar under them, like RootTabs in the iPhone app. One session
// list feeds both; Now adds each candidate's activity. Each screen has the one search box.

const POLL_MS = 15000;
const MODE_KEY = 'allMode';
const FOLDERS_KEY = 'openFolders';

function storage() {
  try { return window.localStorage; } catch { return { getItem: () => null, setItem: () => {} }; }
}

// All's Recent or Folders, remembered on the device; Recent the first time.
export function readAllMode(store) {
  try { return store.getItem(MODE_KEY) === 'folders' ? 'folders' : 'recent'; } catch { return 'recent'; }
}

export function saveAllMode(store, mode) {
  try { store.setItem(MODE_KEY, mode); } catch { /* private mode: for this visit only */ }
}

// Polls `every` ms (or every() ms, asked after each poll) while the tab is shown and the page
// visible, one poll at a time. A failed load or render is reported to onError and never stops the
// next poll. schedule() waits one pause before the next poll instead of polling now.
export function poller({ load, render, isActive, isVisible, every, setTimer = setTimeout, clearTimer = clearTimeout, onError = console.error }) {
  let timer = null;
  let ticking = false;
  async function tick() {
    clearTimer(timer);
    if (!isActive() || ticking || !isVisible()) return;
    ticking = true;
    try {
      try {
        await load();
      } catch (e) {
        onError(e);
      }
      if (isActive()) render();
    } catch (e) {
      onError(e);
    } finally {
      ticking = false;
    }
    later();
  }
  // later sets the one timer for the next poll; a poll that is running sets it when it ends.
  function later() {
    clearTimer(timer);
    timer = null;
    if (isActive() && isVisible()) timer = setTimer(tick, typeof every === 'function' ? every() : every);
  }
  return { tick, schedule: () => { if (!ticking) later(); }, stop: () => clearTimer(timer) };
}

// api: the page's /v1 helper (method, path, body, opts); opts.interaction marks what the person
// asked for. Rows open a session at #/s/<id>; the compose button opens #/new. headerButtons()
// gives extra buttons for Now's header (the Lock).
export function createTabs(api, { headerButtons = () => [] } = {}) {
  // userAsked: the load running now is one the person asked for (a tab opened, Retry), not a poll.
  let userAsked = false;
  const store = storage();
  // Only a GET the person caused is marked; a POST or PATCH counts as use on the server by itself.
  const model = new NowModel({ api: (method, path, body) => api(method, path, body, userAsked && method === 'GET' ? { interaction: true } : undefined), storage: store });
  const $ = (id) => document.getElementById(id);
  const screens = { now: $('now'), all: $('all') };
  let tab = 'now';
  let active = false;
  let allMode = readAllMode(store);
  let allQuery = '';
  const ui = {
    openCards: new Set(),
    menuFor: null, // the session whose row menu is open
    focus: null, // data-focus of the control to focus after the next render
    rendering: false, // a render is replacing Now's rows (focus moves are not the person's)
    pointerDown: false,
    storage: store,
    announce: (text) => announce($('now-status'), text),
    open(session) { location.hash = '#/s/' + encodeURIComponent(session.id); },
    rerender: () => render(),
    refresh: () => userTick(),
    conversations: (shown) => conversations(nowSearch.state, shown),
  };

  // Each screen's header is built once and kept, so the search box keeps its focus and text while
  // what is around it is drawn again.
  function shell(screen, title, onInput) {
    const trailing = el('span', 'header-buttons');
    const summary = el('div', 'summary-slot');
    const box = searchBox({ placeholder: 'Search all sessions', onInput });
    const extra = el('div', 'header-extra');
    const below = el('div', 'header-below');
    below.append(summary, box.node, extra);
    const body = el('div', 'screen-body');
    screen.replaceChildren(screenHeader(title, trailing, below), body);
    return { trailing, summary, extra, body };
  }

  const nowSearch = conversationSearch({ api, onChange: () => { if (active && tab === 'now') render(); } });
  const allSearch = conversationSearch({ api, onChange: () => { if (active && tab === 'all') render(); } });
  const nowShell = shell(screens.now, 'Now', (text) => {
    model.setQuery(text);
    nowSearch.typed(text);
    render();
  });
  const allShell = shell(screens.all, 'All', (text) => {
    allQuery = text;
    allSearch.typed(text);
    render();
  });

  function composeButton() {
    const b = el('button', 'round');
    b.type = 'button';
    b.setAttribute('aria-label', 'New session');
    b.append(icon('compose'));
    b.addEventListener('click', () => { location.hash = '#/new'; });
    return b;
  }

  function render() {
    // A render replaces the rows: the focused control (a row menu, a fold, the chip) gets focus back.
    const screen = screens[tab];
    const had = document.activeElement?.dataset?.focus;
    const want = ui.focus ?? (screen.contains(document.activeElement) ? had : null);
    ui.focus = null;
    ui.rendering = true;
    try {
      if (tab === 'now') {
        const out = renderNow(model, ui);
        nowShell.trailing.replaceChildren(...headerButtons(), composeButton());
        nowShell.summary.replaceChildren(out.summary);
        nowShell.extra.replaceChildren(...(out.chip ? [out.chip] : []));
        nowShell.body.replaceChildren(...out.body);
      } else {
        allShell.trailing.replaceChildren(composeButton());
        allShell.summary.replaceChildren(allSummary());
        allShell.extra.replaceChildren(...(queryWords(allQuery).length ? [] : [modeSwitch()]));
        allShell.body.replaceChildren(...renderAll());
      }
      if (want) screen.querySelector(`[data-focus="${CSS.escape(want)}"]`)?.focus();
    } finally {
      ui.rendering = false;
    }
  }

  // Polls every 15 s while the page is visible, one refresh at a time.
  const polling = poller({
    load: () => (tab === 'now' ? model.refresh() : model.refreshList()),
    render: () => render(),
    isActive: () => active,
    isVisible: () => document.visibilityState === 'visible',
    every: POLL_MS,
  });
  const tick = () => polling.tick();
  async function userTick() {
    userAsked = true;
    try { await polling.tick(); } finally { userAsked = false; }
  }

  // Whether a pointer is pressed, for the row menu closing when focus leaves it (nowView.mjs).
  document.addEventListener('pointerdown', () => { ui.pointerDown = true; }, true);
  for (const type of ['pointerup', 'pointercancel']) document.addEventListener(type, () => { ui.pointerDown = false; }, true);

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') tick();
    else polling.stop();
  });

  // All

  function allSummary() {
    const n = model.sessions.length;
    return el('p', 'summary', n === 1 ? '1 session' : n + ' sessions');
  }

  // Recent | Folders: two buttons drawn as one segmented control.
  function modeSwitch() {
    const box = el('div', 'segmented');
    box.setAttribute('role', 'group');
    box.setAttribute('aria-label', 'Show');
    for (const [mode, label] of [['recent', 'Recent'], ['folders', 'Folders']]) {
      const b = el('button', 'segment' + (allMode === mode ? ' selected' : ''), label);
      b.type = 'button';
      b.dataset.focus = 'mode:' + mode;
      b.setAttribute('aria-pressed', String(allMode === mode));
      b.addEventListener('click', () => {
        allMode = mode;
        saveAllMode(store, mode);
        render();
      });
      box.append(b);
    }
    return box;
  }

  // A row in All: no state line, so the time shows; the place with its tab when open on the Mac.
  function allRow(session, place = rowPlace(session, true)) {
    const row = sessionLine({ session, activity: model.activity[session.id], state: null, place, onClick: () => ui.open(session) });
    row.dataset.focus = 'all-row:' + session.id;
    return row;
  }

  function renderAll() {
    const out = [];
    if (model.loadError) out.push(errorBanner('Could not reach your Mac', model.loadError.message, () => userTick()));
    const now = Date.now();
    if (queryWords(allQuery).length) {
      const found = searchSessions(model.sessions, allQuery, model.activity, now);
      if (found.open.length) out.push(section(sectionLabel('Open'), ...found.open.map((s) => allRow(s))));
      if (found.other.length) out.push(section(sectionLabel('Other sessions'), ...found.other.map((s) => allRow(s))));
      out.push(...conversations(allSearch.state, new Set([...found.open, ...found.other].map((s) => s.id))));
      return out;
    }
    if (allMode === 'folders') {
      const repos = folderTree(model.sessions, now);
      if (repos.length) out.push(section(null, ...repos.flatMap(repoRows)));
    } else {
      const sections = allSections({ sessions: model.sessions, project: null, now, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone });
      for (const s of sections) out.push(section(sectionLabel(dayTitle(s.day)), ...s.sessions.map((x) => allRow(x))));
    }
    if (!model.sessions.length && !model.loadError) {
      out.push(el('p', 'empty', model.loaded ? 'No sessions yet. Sessions from Claude Code on your Mac show up here.' : 'Loading sessions…'));
    }
    return out;
  }

  // "38 · 2 working", "12".
  const counts = (count, working) => (working ? count + ' · ' + working + ' working' : String(count));

  // Inside a folder its name is already above: only the subfolder and the Mac place, if any.
  function folderPlace(session) {
    const parts = [];
    if (session.folder?.sub) parts.push(session.folder.sub);
    if (session.mac) parts.push('tab ' + session.mac.tab.index, 'pane ' + session.mac.pane);
    return parts.join(' · ');
  }

  function fold(key, name, count, working, startsOpen) {
    const open = isOpenChoice(store, FOLDERS_KEY, key, startsOpen);
    const line = foldLine({
      name, counts: counts(count, working), open, focusKey: 'fold:' + key,
      onToggle: () => { saveChoice(store, FOLDERS_KEY, key, !open); render(); },
    });
    return { open, line };
  }

  // A repo: its fold line, then the main checkout's sessions and its worktrees (each a fold of its
  // own) under a guide line.
  function repoRows(repo) {
    const { open, line } = fold(folderKey(repo.repoPath), repo.name, repo.count, repo.working, repo.open);
    if (!open) return [line];
    const items = el('div', 'folder-items');
    for (const s of repo.sessions) items.append(allRow(s, folderPlace(s)));
    for (const w of repo.worktrees) {
      const sub = fold(folderKey(repo.repoPath, w.name), w.name, w.count, w.working, w.open);
      items.append(sub.line);
      if (sub.open) {
        const inner = el('div', 'folder-items');
        for (const s of w.sessions) inner.append(allRow(s, folderPlace(s)));
        items.append(inner);
      }
    }
    return [line, items];
  }

  // "Today", "Yesterday", "Mon, Oct 20".
  function dayTitle(day) {
    if (day === 'today') return 'Today';
    if (day === 'yesterday') return 'Yesterday';
    const [y, m, d] = day.split('-').map(Number);
    const date = new Date(y, m - 1, d);
    if (isNaN(date)) return day;
    return y === new Date().getFullYear()
      ? date.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
      : date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
  }

  // In conversations: the server's hits for the query (after 400 ms), without the sessions the
  // screen already shows; nothing without a query or when the server cannot search.
  function conversations(state, shown) {
    if (!state.query || state.supported === false) return [];
    const rows = state.hits.filter((h) => !shown.has(h.id)).map(hitRow);
    if (!rows.length) {
      const note = state.searching ? 'Searching your Mac…'
        : state.error ? 'Search failed: ' + state.error.message : 'No other matches in conversations';
      rows.push(el('p', 'row static note', note));
    }
    return [section(sectionLabel('In conversations'), ...rows)];
  }

  function hitRow(hit) {
    // The same place line as a session row: the folder label, then tab and pane when open on the Mac.
    const detail = el('span', 'detail stack');
    detail.append(el('span', 'detail place', rowPlace(hit, true)), snippet(hit.snippet));
    return dotRow({
      status: hit.state === 'busy' ? 'working' : 'idle', title: displayTitle(hit.title, hit.id), detail, time: hit.lastActive,
      locked: hit.state === 'open_in_terminal', onClick: () => ui.open(hit),
    });
  }

  // The server's [bracketed] matches in bold.
  function snippet(text) {
    const s = el('span', 'snippet');
    s.dir = 'auto';
    const re = /\[([^\]\n]{1,80})\]/g;
    let last = 0, m;
    while ((m = re.exec(text || ''))) {
      s.append(text.slice(last, m.index), el('b', '', m[1]));
      last = re.lastIndex;
    }
    s.append((text || '').slice(last));
    return s;
  }

  // The tab bar

  for (const b of document.querySelectorAll('[data-tab]')) {
    b.addEventListener('click', () => { location.hash = b.dataset.tab === 'now' ? '#/' : '#/' + b.dataset.tab; });
  }

  return {
    // The Now state; the chat reads it for its Next bar and refreshes it while open.
    model,
    // Shows a tab ('now' or 'all'), and polls while it is shown.
    show(name) {
      const changed = !active || tab !== name;
      tab = name;
      active = true;
      for (const [key, node] of Object.entries(screens)) node.hidden = key !== name;
      for (const b of document.querySelectorAll('[data-tab]')) {
        b.classList.toggle('selected', b.dataset.tab === name);
        if (b.dataset.tab === name) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
      }
      render();
      if (changed) userTick();
    },
    hide() {
      active = false;
      polling.stop();
    },
    refresh: () => tick(),
    // Draws the shown tab again from what it has (no request).
    rerender: () => { if (active) render(); },
  };
}
