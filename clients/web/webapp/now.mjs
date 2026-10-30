import { apiDate } from './apiDate.mjs';

// The Now screen's ranking, by the shared rules in api/testdata/clients/README.md.
// Times are milliseconds since the epoch; sessions carry `lastActive` in that form.

const RECENT_MS = 900 * 1000;
const FLAT_WORKING_LIMIT = 5;
const ON_MAC_LIMIT = 8;
const ON_MAC_COLLAPSE_ABOVE = 5;
const LAST_ON_MAC_MS = 12 * 3600 * 1000;

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

const byString = (a, b) => (a < b ? -1 : a > b ? 1 : 0);

// lastOnMac: {id, at} with `at` in epoch milliseconds, or null when the server does not know.
export function rankNow({ sessions, activity, seen, now, lastOnMac = null }) {
  const lastActive = (s) => Math.min(s.lastActive, now);
  const isRecent = (t) => now - t <= RECENT_MS;
  // Equal times fall back to the session id, ascending, so the order is deterministic.
  const newestFirst = (a, b) => lastActive(b) - lastActive(a) || byString(a.id, b.id);
  const newestEvent = (a, b) => b.time - a.time || byString(a.item.session.id, b.item.session.id);

  const approvals = [];
  const failures = [];
  const finishes = [];
  const working = [];
  const terminals = [];
  const byId = new Map();
  const status = new Map(); // a placed row keeps the session's own dot

  for (const session of sessions) {
    const current = (Object.hasOwn(activity, session.id) ? activity[session.id] : null) ?? { kind: 'none' };
    const item = { session, activity: current };
    byId.set(session.id, item);
    status.set(session.id, 'idle');
    if (current.kind === 'needsApproval') {
      approvals.push(item);
      continue;
    }
    if (current.kind === 'failed' && isRecent(current.at)) {
      failures.push({ item, time: current.at });
      continue;
    }
    if (current.kind === 'finished' && isRecent(current.at) && current.at > (seen[session.id] ?? -Infinity)) {
      finishes.push({ item, time: current.at });
      continue;
    }

    let isWorking = session.state === 'busy' || current.kind === 'working';
    if (session.state === 'open_in_terminal') {
      if (isRecent(lastActive(session))) isWorking = true;
      else if (!isWorking) terminals.push(item);
    }
    if (isWorking) {
      working.push(item);
      status.set(session.id, 'working');
    }
  }

  approvals.sort((a, b) => newestFirst(a.session, b.session));
  failures.sort(newestEvent);
  const needsYou = [...approvals, ...failures.map((f) => f.item)];
  // Counted before Last on your Mac and Pinned take their sessions: the header is an overview of state.
  const summary = { needYou: needsYou.length, working: working.length, onMac: terminals.length };

  // Urgency wins: a session in Needs you is never moved into Last on your Mac or Pinned.
  const urgent = new Set(needsYou.map((i) => i.session.id));
  const placed = (item) => ({ session: item.session, activity: item.activity, status: status.get(item.session.id) });
  let last = null;
  if (lastOnMac && byId.has(lastOnMac.id) && !urgent.has(lastOnMac.id)
    && now - Math.min(lastOnMac.at, now) <= LAST_ON_MAC_MS) {
    last = placed(byId.get(lastOnMac.id));
  }
  const pinned = sessions
    .filter((s) => s.pinned === true && s.id !== last?.session.id && !urgent.has(s.id))
    .sort(newestFirst)
    .map((s) => placed(byId.get(s.id)));
  const taken = new Set([last?.session.id, ...pinned.map((i) => i.session.id)]);
  const left = (item) => !taken.has(item.session.id);

  const finished = finishes.filter((f) => left(f.item)).sort(newestEvent).map((f) => f.item);
  const stillWorking = working.filter(left).sort((a, b) => newestFirst(a.session, b.session));
  const stillOnMac = terminals.filter(left).map((i) => i.session).sort(newestFirst);
  const showOnMac = needsYou.length + finished.length + stillWorking.length <= ON_MAC_COLLAPSE_ABOVE;
  const onMac = showOnMac ? stillOnMac.slice(0, ON_MAC_LIMIT) : [];

  return {
    lastOnMac: last,
    pinned,
    needsYou,
    finished,
    working: workingSection(stillWorking, lastActive),
    onMac,
    onMacFolders: byProject(onMac.map((session) => ({ session, activity: byId.get(session.id).activity }))),
    onMacTotal: stillOnMac.length,
    summary,
  };
}

// The row's second line: the trimmed description, or null when it has no text.
export function descriptionLine(session) {
  const d = typeof session?.description === 'string' ? session.description.trim() : '';
  return d || null;
}

// byProject groups items by project path into folder rows, in the order first met. Items come
// newest first, so each folder's first item is its newest and folders follow their newest session.
function byProject(items) {
  const groups = new Map();
  for (const item of items) {
    if (!groups.has(item.session.project)) groups.set(item.session.project, []);
    groups.get(item.session.project).push(item);
  }
  // A folder row starts expanded; collapsing it is the view's state.
  return [...groups].map(([project, grouped]) => ({
    kind: 'folder',
    expanded: true,
    project,
    name: project.split('/').filter(Boolean).pop() ?? project,
    count: grouped.length,
    newest: grouped[0],
    items: grouped,
  }));
}

function workingSection(working, lastActive) {
  if (working.length <= FLAT_WORKING_LIMIT) return { list: working.map((item) => ({ kind: 'session', ...item })) };
  const groups = byProject(working);
  groups.sort((a, b) => b.count - a.count
    || lastActive(b.newest.session) - lastActive(a.newest.session)
    || byString(a.project, b.project));
  const kept = groups.slice(0, FLAT_WORKING_LIMIT);
  const rest = groups.slice(FLAT_WORKING_LIMIT);
  const result = { groups: kept };
  if (rest.length) result.more = { projects: rest.length, sessions: rest.reduce((n, g) => n + g.count, 0) };
  return result;
}

// The chat's Next bar target: the first other session in Needs you, else in Working; null when none.
export function nextSession(view, id) {
  const other = (item) => item.session.id !== id;
  const working = view.working.list ?? view.working.groups.flatMap((g) => g.items);
  return view.needsYou.find(other) ?? working.find(other) ?? null;
}
