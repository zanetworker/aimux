import { apiDate } from './apiDate.mjs';
import { compareCodePoints, isOpen, isWaitingForPermission, isWorking, macOrder, matches, newestFirst, projectName, queryWords } from './search.mjs';

// The Now screen's ranking, by the shared rules in api/testdata/clients/README.md.
// Times are milliseconds since the epoch; sessions carry `lastActive` in that form.

const RECENT_MS = 900 * 1000;
const HOLD_MS = 30 * 1000;

const isTurnEvent = (e) => e.kind === 'turn_started' || e.kind === 'turn_done' || e.kind === 'turn_error';

// A pending approval wins (the latest one); otherwise the last turn event decides.
export function deriveActivity(events) {
  const ordered = [...events].sort((a, b) => a.seq - b.seq);

  let pending = [];
  for (const e of ordered) {
    if (e.kind === 'approval') pending.push(e.data);
    else if (e.kind === 'approval_resolved') pending = pending.filter((a) => a.approvalId !== e.data.approvalId);
  }
  if (pending.length) return { kind: 'needsApproval', approval: pending[pending.length - 1] };

  const last = ordered.findLast(isTurnEvent);
  if (!last) return { kind: 'none' };
  if (last.kind === 'turn_error') return { kind: 'failed', error: last.data.error, at: apiDate(last.time) };
  if (last.kind === 'turn_done') return { kind: 'finished', at: apiDate(last.time) };
  const step = currentStep(ordered, last.turn);
  return step === undefined ? { kind: 'working' } : { kind: 'working', step };
}

// Newest event of the turn that is a thought ("Thinking") or a titled tool call.
function currentStep(ordered, turn) {
  for (let i = ordered.length - 1; i >= 0; i--) {
    const e = ordered[i];
    if (e.turn !== turn || e.kind !== 'update') continue;
    const { sessionUpdate, title } = e.data;
    if (sessionUpdate === 'agent_thought_chunk') return 'Thinking';
    if ((sessionUpdate === 'tool_call' || sessionUpdate === 'tool_call_update') && title != null) return title;
  }
  return undefined;
}

// Now: Needs you, Pinned, the Mac's iTerm2 windows (tabs, panes), Elsewhere and, with a query,
// Other sessions. A session appears once, in the first section that takes it.
export function rankNow({ sessions, activity, now, query = '', workingOnly = false }) {
  const current = (s) => (Object.hasOwn(activity, s.id) ? activity[s.id] : null) ?? { kind: 'none' };
  const item = (s) => ({ session: s, activity: current(s) });
  const working = (s) => isWorking(s, current(s));
  const open = (s) => isOpen(s, current(s));
  const newest = newestFirst(now);
  const words = queryWords(query);
  const wanted = (s) => matches(s, words) && (!workingOnly || working(s));

  // Pending approvals (by the session's lastActive) and terminal sessions waiting for permission (by
  // waiting.since), newest first; then failures of the last 900 s (newest failure first).
  const approvals = [];
  const failures = [];
  for (const s of sessions) {
    const a = current(s);
    if (a.kind === 'needsApproval') approvals.push({ s, at: Math.min(s.lastActive, now) });
    else if (isWaitingForPermission(s)) approvals.push({ s, at: Math.min(apiDate(s.waiting.since), now) });
    else if (a.kind === 'failed' && now - a.at <= RECENT_MS) failures.push({ s, at: a.at });
  }
  const byTime = (x, y) => y.at - x.at || compareCodePoints(x.s.id, y.s.id);
  approvals.sort(byTime);
  failures.sort(byTime);
  const needsYouAll = [...approvals, ...failures].map((x) => x.s);
  const urgent = new Set(needsYouAll.map((s) => s.id));
  const rest = sessions.filter((s) => !urgent.has(s.id));
  const pinnedAway = (s) => s.pinned === true && !s.mac;
  const unpinned = rest.filter((s) => !pinnedAway(s));

  const { windows, hiddenTabs } = macWindows(rest.filter((s) => s.mac), words, wanted, working);
  return {
    needsYou: needsYouAll.filter((s) => matches(s, words)).map(item),
    pinned: rest.filter(pinnedAway).filter(wanted).sort(newest).map(item),
    windows,
    hiddenTabs,
    elsewhere: byLabel(unpinned.filter((s) => !s.mac && open(s) && wanted(s)), now),
    other: words.length && !workingOnly ? unpinned.filter((s) => !open(s) && wanted(s)).sort(newest) : [],
    working: sessions.filter(working).length,
    open: sessions.filter(open).length,
  };
}

// Windows by index, tabs by index, panes by number, and the tabs the filters left empty. A window is
// keyed by its id; its name and order come from its first session in Mac order.
function macWindows(placed, words, wanted, working) {
  const existing = new Set();
  const front = new Set();
  const byTab = new Map();
  for (const s of placed) {
    const key = s.mac.window.id + '\u0000' + s.mac.tab.index;
    existing.add(key);
    if (s.mac.lastInFront === true) front.add(key);
    if (!wanted(s)) continue;
    if (!byTab.has(key)) byTab.set(key, []);
    byTab.get(key).push(s);
  }
  const byWindow = new Map();
  for (const [key, listed] of byTab) {
    const ordered = listed.sort(macOrder);
    const first = ordered[0];
    const tab = {
      index: first.mac.tab.index, name: first.mac.tab.name, panes: first.mac.panes,
      working: ordered.filter(working).length, open: words.length ? true : front.has(key), sessions: ordered,
    };
    const entry = byWindow.get(first.mac.window.id) ?? { first, tabs: [] };
    if (macOrder(first, entry.first) < 0) entry.first = first;
    entry.tabs.push(tab);
    byWindow.set(first.mac.window.id, entry);
  }
  const windows = [...byWindow.values()].sort((a, b) => macOrder(a.first, b.first)).map((e) => ({
    id: e.first.mac.window.id, name: e.first.mac.window.name, tabs: e.tabs.sort((a, b) => a.index - b.index),
  }));
  return { windows, hiddenTabs: existing.size - byTab.size };
}

// Elsewhere: grouped by folder label (or the project's last component), newest group first.
function byLabel(sessions, now) {
  const groups = new Map();
  for (const s of sessions) {
    const label = s.folder?.label || projectName(s.project);
    if (!groups.has(label)) groups.set(label, []);
    groups.get(label).push(s);
  }
  const newest = newestFirst(now);
  const t = (s) => Math.min(s.lastActive, now);
  return [...groups].map(([label, list]) => ({ label, sessions: list.sort(newest) }))
    .sort((a, b) => t(b.sessions[0]) - t(a.sessions[0]) || compareCodePoints(a.label, b.label));
}

// The row's second line: the trimmed description, or null when it has no text.
export function descriptionLine(session) {
  const d = typeof session?.description === 'string' ? session.description.trim() : '';
  return d || null;
}

// A list with no `mac` at all keeps the last places (by session id, for sessions still listed) until
// 30 s, inclusive, after the last list that had some; a list with places replaces them. `previous` is
// {places, since} or null. Call it with the time a list arrived to move the clock, and with the held
// state to apply it.
export function holdPlaces(previous, current, now) {
  const fresh = current.filter((s) => s.mac);
  if (fresh.length) return { sessions: current, held: { places: Object.fromEntries(fresh.map((s) => [s.id, s.mac])), since: now } };
  if (!previous || now - previous.since > HOLD_MS) return { sessions: current, held: null };
  return {
    sessions: current.map((s) => (Object.hasOwn(previous.places, s.id) ? { ...s, mac: previous.places[s.id] } : s)),
    held: previous,
  };
}

// The chat's Next bar: Needs you first (not the open session); otherwise the first working session in
// Now's order (Pinned, the windows tab by tab and pane by pane, then Elsewhere) that is not the open
// session and not one whose approval the owner just answered. Null when none.
export function nextSession(view, id, answered = new Set(), activity = {}) {
  const urgent = view.needsYou.find((i) => i.session.id !== id);
  if (urgent) return urgent;
  const listed = [
    ...view.pinned.map((i) => i.session),
    ...view.windows.flatMap((w) => w.tabs.flatMap((t) => t.sessions)),
    ...view.elsewhere.flatMap((g) => g.sessions),
  ];
  const a = (s) => activity[s.id] ?? { kind: 'none' };
  const next = listed.find((s) => s.id !== id && !answered.has(s.id) && isWorking(s, a(s)));
  return next ? { session: next, activity: a(next) } : null;
}
