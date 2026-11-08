import { newestFirst } from './search.mjs';

// All's Recent: sessions by calendar day in `timeZone`, newest day first, newest first inside, by
// the shared rules in api/testdata/clients/README.md. Times are milliseconds since the epoch.
export function allSections({ sessions, project, now, timeZone }) {
  const shown = sessions.filter((s) => project == null || s.project === project);
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

  const byDay = new Map();
  for (const s of shown) {
    const key = dayKey(Math.min(s.lastActive, now));
    if (!byDay.has(key)) byDay.set(key, []);
    byDay.get(key).push(s);
  }
  return [...byDay.keys()].sort().reverse().map((key) => ({ day: label(key), sessions: byDay.get(key).sort(newestFirst(now)) }));
}
