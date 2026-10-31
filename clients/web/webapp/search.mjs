import { apiDate } from './apiDate.mjs';

// The one search of Now and All, and the tests Now, All and search share, by the rules in
// api/testdata/clients/README.md. Times are milliseconds since the epoch.

// Plain code point order, not locale collation or UTF-16 code units.
export function compareCodePoints(a, b) {
  const x = [...a], y = [...b];
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const d = x[i].codePointAt(0) - y[i].codePointAt(0);
    if (d) return d < 0 ? -1 : 1;
  }
  return x.length - y.length < 0 ? -1 : x.length > y.length ? 1 : 0;
}

// The last component of a project path, trailing slashes trimmed first.
export function projectName(path) {
  return (path || '').split('/').filter(Boolean).pop() || path || '';
}

// The query's words: lower-cased and split on whitespace; none means no query.
export function queryWords(query) {
  return (query || '').toLowerCase().split(/\s+/).filter(Boolean);
}

// Every word is a substring of the lower-cased title, description, folder label, tab name, window
// name or last prompt; no words matches everything.
export function matches(session, words) {
  const fields = [session.title, session.description, session.folder?.label, session.mac?.tab?.name,
    session.mac?.window?.name, session.lastPrompt].map((f) => (typeof f === 'string' ? f.toLowerCase() : ''));
  return words.every((w) => fields.some((f) => f.includes(w)));
}

// Working: busy, working activity, or the server's `working` flag (a terminal Claude the registry calls busy).
export function isWorking(session, activity) {
  return session.state === 'busy' || session.working === true || activity?.kind === 'working';
}

// Open: busy, open in a terminal, in an iTerm2 pane (whatever its state), or working activity.
export function isOpen(session, activity) {
  return session.state === 'busy' || session.state === 'open_in_terminal' || session.mac != null || activity?.kind === 'working';
}

// The clock every order and window is measured against: required, epoch milliseconds.
function requireNow(now) {
  if (typeof now !== 'number' || !Number.isFinite(now)) throw new TypeError('now must be a finite time in milliseconds, got ' + String(now));
}

// Newest first by lastActive (a time after now counts as now), equal times by id.
export function newestFirst(now) {
  requireNow(now);
  const t = (s) => Math.min(s.lastActive, now);
  return (a, b) => t(b) - t(a) || compareCodePoints(a.id, b.id);
}

// Mac order: window index, window id, tab index, pane, then id; numbers as numbers.
export function macOrder(a, b) {
  const l = a.mac, r = b.mac;
  if (!l || !r) return compareCodePoints(a.id, b.id);
  return l.window.index - r.window.index || compareCodePoints(l.window.id, r.window.id)
    || l.tab.index - r.tab.index || l.pane - r.pane || compareCodePoints(a.id, b.id);
}

// The matching open sessions (in Mac order, then those with no pane newest first), then every other
// match newest first. A query with no words finds nothing.
export function searchSessions(sessions, query, activity, now) {
  requireNow(now);
  const words = queryWords(query);
  if (!words.length) return { open: [], other: [] };
  const found = sessions.filter((s) => matches(s, words));
  const open = found.filter((s) => isOpen(s, activity?.[s.id]));
  const placed = open.filter((s) => s.mac).sort(macOrder);
  const unplaced = open.filter((s) => !s.mac).sort(newestFirst(now));
  const openIds = new Set(open.map((s) => s.id));
  return { open: [...placed, ...unplaced], other: found.filter((s) => !openIds.has(s.id)).sort(newestFirst(now)) };
}

// The server's conversation search (GET /v1/search), 400 ms after the last keystroke; an answer for
// an older query never lands. `state` is what the "In conversations" section shows.
export function conversationSearch({ api, onChange, setTimer = setTimeout, clearTimer = clearTimeout, delay = 400 }) {
  const state = { query: '', hits: [], error: null, searching: false, supported: null };
  let timer = null;

  async function run(query) {
    if (state.supported === null) {
      try {
        const health = await api('GET', '/v1/health');
        state.supported = (health.capabilities || []).includes('search');
      } catch { /* ask again next time */ }
    }
    if (query !== state.query) return;
    if (state.supported === false) {
      state.searching = false;
      onChange();
      return;
    }
    try {
      const data = await api('GET', '/v1/search?q=' + encodeURIComponent(query) + '&limit=30', undefined, { interaction: true });
      if (query !== state.query) return;
      state.hits = (data.results || []).map((h) => ({ ...h, lastActive: apiDate(h.lastActive) }));
      state.error = null;
    } catch (e) {
      if (query !== state.query) return;
      state.hits = [];
      state.error = e;
    }
    state.searching = false;
    onChange();
  }

  return {
    state,
    typed(text) {
      clearTimer(timer);
      timer = null;
      state.query = queryWords(text).length ? text.trim() : '';
      state.hits = [];
      state.error = null;
      state.searching = !!state.query;
      if (state.query) {
        const query = state.query;
        timer = setTimer(() => run(query), delay);
      }
    },
  };
}
