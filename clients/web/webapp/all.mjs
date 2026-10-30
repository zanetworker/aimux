// The All screen's sections, by the shared rules in api/testdata/clients/README.md.
// Times are milliseconds since the epoch; sessions carry `lastActive` in that form.

const byString = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
const STATE_RANK = new Map([['busy', 0], ['open_in_terminal', 1]]);

export function allSections({ sessions, project, sort, now, timeZone }) {
  const lastActive = (s) => Math.min(s.lastActive, now);
  // Equal times fall back to the session id, ascending, so the order is deterministic.
  const newestFirst = (a, b) => lastActive(b) - lastActive(a) || byString(a.id, b.id);
  const shown = sessions.filter((s) => project == null || s.project === project);

  if (sort === 'project') return projectSections(shown, lastActive, newestFirst);

  const fmt = new Intl.DateTimeFormat('en-CA', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' });
  const dayKey = (t) => {
    const p = Object.fromEntries(fmt.formatToParts(t).map((x) => [x.type, x.value]));
    return `${p.year}-${p.month}-${p.day}`;
  };
  const today = dayKey(now);
  // The previous calendar day, computed on the date itself so DST never matters.
  const [y, m, d] = today.split('-').map(Number);
  const yesterday = new Date(Date.UTC(y, m - 1, d - 1)).toISOString().slice(0, 10);
  const label = (key) => (key === today ? 'today' : key === yesterday ? 'yesterday' : key);

  const rank = (s) => STATE_RANK.get(s.state) ?? 2;
  const order = sort === 'mostActive' ? (a, b) => rank(a) - rank(b) || newestFirst(a, b) : newestFirst;

  const byDay = new Map();
  for (const s of shown) {
    const key = dayKey(lastActive(s));
    if (!byDay.has(key)) byDay.set(key, []);
    byDay.get(key).push(s);
  }
  return [...byDay.keys()].sort().reverse().map((key) => ({
    day: label(key),
    sessions: byDay.get(key).sort(order),
  }));
}

function projectSections(sessions, lastActive, newestFirst) {
  const byProject = new Map();
  for (const s of sessions) {
    if (!byProject.has(s.project)) byProject.set(s.project, []);
    byProject.get(s.project).push(s);
  }
  return [...byProject]
    .map(([path, rows]) => ({ day: path, sessions: rows.sort(newestFirst) }))
    .sort((a, b) => lastActive(b.sessions[0]) - lastActive(a.sessions[0]) || byString(a.day, b.day));
}
