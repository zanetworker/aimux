import { allSections } from './all.mjs';
import { apiDate } from './apiDate.mjs';
import { NowModel } from './nowModel.mjs';
import { announce } from './sessionInfo.mjs';
import { renderNow } from './nowView.mjs';
import { displayTitle, el, icon, projectName, sectionLabel, section, sessionRow, dotRow, projectLine, errorBanner, screenHeader } from './rows.mjs';

// The Now / All / search screens and the tab bar under them, like RootTabs in the iPhone app.
// One session list feeds all three; Now adds each candidate's activity.

const POLL_MS = 15000;
const SEARCH_DEBOUNCE_MS = 400;
const SORTS = [['newest', 'Newest'], ['mostActive', 'Most active'], ['project', 'Project']];

function storage() {
  try { return window.localStorage; } catch { return { getItem: () => null, setItem: () => {} }; }
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

// Search on this phone: sessions whose title, description, folder and last prompt hold every word
// of the query; those matched by title and description alone first, the list order kept inside.
export function localMatches(sessions, query) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return [];
  const has = (parts) => { const hay = parts.join('\n').toLowerCase(); return words.every((w) => hay.includes(w)); };
  const named = [], rest = [];
  for (const s of sessions) {
    const own = [s.title || '', s.description || ''];
    if (has(own)) named.push(s);
    else if (has([...own, s.project || '', s.lastPrompt || ''])) rest.push(s);
  }
  return [...named, ...rest];
}

// api: the page's /v1 helper (method, path, body, opts); opts.interaction marks what the person
// asked for. Rows open a session at #/s/<id>; the compose button opens #/new. headerButtons()
// gives extra buttons for Now's header (the Lock).
export function createTabs(api, { headerButtons = () => [] } = {}) {
  // userAsked: the load running now is one the person asked for (a tab opened, Retry), not a poll.
  let userAsked = false;
  // Only a GET the person caused is marked; a POST or PATCH counts as use on the server by itself.
  const model = new NowModel({ api: (method, path, body) => api(method, path, body, userAsked && method === 'GET' ? { interaction: true } : undefined), storage: storage() });
  const $ = (id) => document.getElementById(id);
  const screens = { now: $('now'), all: $('all'), search: $('search') };
  let tab = 'now';
  let active = false;
  let allFilter = { project: null, onMacOnly: false, sort: 'newest' };
  const ui = {
    collapsed: new Set(),
    openCards: new Set(),
    menuFor: null, // the session whose row menu is open
    focus: null, // data-focus of the control to focus after the next render
    rendering: false, // a render is replacing Now's rows (focus moves are not the person's)
    pointerDown: false,
    announce: (text) => announce($('now-status'), text),
    open(session, seen) {
      if (seen) model.markSeen(session.id);
      location.hash = '#/s/' + encodeURIComponent(session.id);
    },
    showAll(filter) {
      allFilter = { project: null, onMacOnly: false, sort: 'newest', ...filter };
      location.hash = '#/all';
    },
    rerender: () => render(),
    refresh: () => userTick(),
  };

  function composeButton() {
    const b = el('button', 'round');
    b.type = 'button';
    b.setAttribute('aria-label', 'New session');
    b.append(icon('compose'));
    b.addEventListener('click', () => { location.hash = '#/new'; });
    return b;
  }

  function nowButtons() {
    const box = el('span', 'header-buttons');
    box.append(...headerButtons(), composeButton());
    return box;
  }

  function render() {
    if (tab === 'now') {
      // A render replaces the rows: the focused row control (a menu button or item) gets focus back.
      const had = document.activeElement?.dataset?.focus;
      const want = ui.focus ?? (screens.now.contains(document.activeElement) ? had : null);
      ui.focus = null;
      ui.rendering = true;
      try {
        screens.now.replaceChildren(...renderNow(model, Object.assign(ui, { headerButton: nowButtons() })));
        if (want) screens.now.querySelector(`[data-focus="${CSS.escape(want)}"]`)?.focus();
      } finally {
        ui.rendering = false;
      }
    } else if (tab === 'all') {
      screens.all.replaceChildren(...renderAll());
    } else {
      renderSearchResults();
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

  function renderAll() {
    const out = [screenHeader('All Sessions', composeButton(), filterMenus())];
    if (allFilter.onMacOnly) {
      const row = el('div', 'row static');
      const label = el('span', 'text');
      label.append(el('span', 'title', 'Open on your Mac'));
      const show = el('button', 'link', 'Show all');
      show.type = 'button';
      show.addEventListener('click', () => { allFilter.onMacOnly = false; render(); });
      row.append(icon('lock'), label, show);
      out.push(section(null, row));
    }
    if (model.loadError) out.push(errorBanner('Could not reach your Mac', model.loadError.message, () => userTick()));
    const shown = allFilter.onMacOnly ? model.sessions.filter((s) => s.state === 'open_in_terminal') : model.sessions;
    const sections = allSections({
      sessions: shown, project: allFilter.project, sort: allFilter.sort, now: Date.now(),
      timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    });
    for (const s of sections) {
      out.push(section(sectionLabel(dayTitle(s.day), null, String(s.sessions.length)),
        ...s.sessions.map((session) => sessionRow(session, () => ui.open(session)))));
    }
    if (!model.sessions.length && !model.loadError) {
      out.push(el('p', 'empty', model.loaded ? 'No sessions yet. Sessions from Claude Code on your Mac show up here.' : 'Loading sessions…'));
    } else if (!sections.length && model.sessions.length) {
      out.push(el('p', 'empty', 'No sessions here.'));
    }
    return out;
  }

  // "Today", "Yesterday", "Mon, Oct 20"; the folder name when sorted by project.
  function dayTitle(day) {
    if (allFilter.sort === 'project') return projectName(day);
    if (day === 'today') return 'Today';
    if (day === 'yesterday') return 'Yesterday';
    const [y, m, d] = day.split('-').map(Number);
    const date = new Date(y, m - 1, d);
    if (isNaN(date)) return day;
    return y === new Date().getFullYear()
      ? date.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
      : date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
  }

  function filterMenus() {
    const box = el('div', 'filters');
    const project = menu('Project', [['', 'All projects'], ...projects().map((p) => [p.path, p.name + '  ' + p.count])],
      allFilter.project ?? '', (value) => { allFilter.project = value || null; render(); });
    const sort = menu('Sort', SORTS, allFilter.sort, (value) => { allFilter.sort = value; render(); });
    box.append(project, sort);
    return box;
  }

  // A native select drawn as a capsule: its value and a small chevron.
  function menu(label, options, value, onChange) {
    const wrap = el('label', 'menu-pill');
    const select = el('select');
    select.setAttribute('aria-label', label);
    for (const [v, text] of options) {
      const o = el('option', '', text);
      o.value = v;
      select.append(o);
    }
    select.value = value;
    select.addEventListener('change', () => onChange(select.value));
    wrap.append(select, icon('chevron', 'down'));
    return wrap;
  }

  // One entry per folder, most sessions first; ties keep the order of the newest session.
  function projects() {
    const counts = new Map();
    for (const s of model.sessions) counts.set(s.project, (counts.get(s.project) || 0) + 1);
    return [...counts].map(([path, count], i) => ({ path, count, i, name: projectName(path) }))
      .sort((a, b) => b.count - a.count || a.i - b.i);
  }

  // Search: title matches from the loaded list at once, the server's hits after a pause.

  const search = { query: '', hits: [], error: null, searching: false, supported: null, timer: null };
  const field = el('input', 'search-field');
  field.type = 'search';
  field.setAttribute('aria-label', 'Search sessions');
  field.autocomplete = 'off';
  field.enterKeyHint = 'search';
  const results = el('div', 'results');
  const fieldWrap = el('div', 'search-box');
  fieldWrap.append(icon('search'), field);
  screens.search.replaceChildren(screenHeader('Search'), fieldWrap, results);
  field.addEventListener('input', () => {
    clearTimeout(search.timer);
    search.query = field.value.trim();
    search.hits = [];
    search.error = null;
    search.searching = !!search.query;
    renderSearchResults();
    if (search.query) search.timer = setTimeout(() => runSearch(search.query), SEARCH_DEBOUNCE_MS);
  });

  async function runSearch(query) {
    if (search.supported === null) {
      try {
        const health = await api('GET', '/v1/health');
        search.supported = (health.capabilities || []).includes('search');
      } catch { /* ask again next time */ }
    }
    if (query !== search.query) return;
    if (search.supported === false) { search.searching = false; renderSearchResults(); return; }
    try {
      const data = await api('GET', '/v1/search?q=' + encodeURIComponent(query) + '&limit=30', undefined, { interaction: true });
      if (query !== search.query) return; // a newer query is on its way
      search.hits = (data.results || []).map((h) => ({ ...h, lastActive: apiDate(h.lastActive) }));
      search.error = null;
    } catch (e) {
      if (query !== search.query) return;
      search.hits = [];
      search.error = e;
    }
    search.searching = false;
    renderSearchResults();
  }

  function renderSearchResults() {
    field.placeholder = model.sessions.length ? 'Search ' + model.sessions.length + ' sessions' : 'Search sessions';
    if (!search.query) { results.replaceChildren(); return; }
    const local = localMatches(model.sessions, search.query);
    const out = [];
    if (local.length) {
      out.push(section(sectionLabel('Titles and descriptions'), ...local.map((s) => sessionRow(s, () => ui.open(s)))));
    }
    if (search.supported !== false) {
      const shown = new Set(local.map((s) => s.id));
      const hits = search.hits.filter((h) => !shown.has(h.id));
      const rows = hits.map(hitRow);
      if (!rows.length) {
        const note = search.searching ? 'Searching your Mac…'
          : search.error ? 'Search failed: ' + search.error.message : 'No other matches in conversations';
        rows.push(el('p', 'row static note', note));
      }
      out.push(section(sectionLabel('In conversations'), ...rows));
    }
    results.replaceChildren(...out);
  }

  function hitRow(hit) {
    const detail = el('span', 'detail stack');
    detail.append(projectLine(hit.project), snippet(hit.snippet));
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
    // Shows a tab ('now', 'all' or 'search'), and polls while it is shown.
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
      if (name === 'search') field.focus();
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
