// Which rows the sessions table shows. The server decides what is hidden
// (hidden: the reveals that must all be on, history.Visible); this only
// says which switches are on and applies the table's own filters.

export interface FilterableSession {
  id: string;
  starred: boolean;
  project: string;
  hidden?: string[];
  shownTitle?: string;
  title?: string;
  firstPrompt?: string;
  lastPrompt?: string;
  gitBranch?: string;
  annotation?: string;
  tags?: string[];
}

export interface SessionFilter {
  starredOnly: boolean;
  showSubagents: boolean;
  showAutomated: boolean;
  showAnalyzers: boolean;
  dirFilter: string;
  filter: string; // the metadata text filter
  deepMatches: Map<string, string> | null; // content search hits by id, null when none ran
}

function matchesText(s: FilterableSession, q: string): boolean {
  return [s.shownTitle, s.title, s.firstPrompt, s.lastPrompt, s.gitBranch, s.project, s.annotation]
    .some(v => (v || '').toLowerCase().includes(q)) ||
    (s.tags || []).some(t => t.toLowerCase().includes(q));
}

// filterSessions keeps the rows that pass Starred-only, hidden and the path
// filter; when a text filter or content search is active, a row must also
// match one of them.
export function filterSessions<T extends FilterableSession>(sessions: T[], f: SessionFilter): T[] {
  const searching = f.filter !== '' || f.deepMatches !== null;
  const revealed = new Set<string>();
  if (f.showSubagents) revealed.add('subagents');
  if (f.showAutomated) revealed.add('automated');
  if (f.showAnalyzers) revealed.add('analyzers');
  if (searching) revealed.add('search');
  const dq = f.dirFilter.toLowerCase();
  const q = f.filter.toLowerCase();
  return sessions.filter(s => {
    if (f.starredOnly && !s.starred) return false;
    if (!(s.hidden || []).every(r => revealed.has(r))) return false;
    if (dq && !(s.project || '').toLowerCase().includes(dq)) return false;
    if (!searching) return true;
    return (q !== '' && matchesText(s, q)) || (f.deepMatches?.has(s.id) ?? false);
  });
}

export type Switches = Pick<SessionFilter, 'showSubagents' | 'showAutomated' | 'showAnalyzers'>;

// defaultSwitches: every switch off, the table's state when it opens.
export const defaultSwitches: Switches = { showSubagents: false, showAutomated: false, showAnalyzers: false };

// totalShowable is the Sessions tab count and the table's "of Y sessions":
// rows the switches that are on could show. Near-empty rows count, since
// any search shows them.
export function totalShowable(sessions: FilterableSession[], f: Switches): number {
  const on = new Set<string>(['search']);
  if (f.showSubagents) on.add('subagents');
  if (f.showAutomated) on.add('automated');
  if (f.showAnalyzers) on.add('analyzers');
  return sessions.filter(s => (s.hidden || []).every(r => on.has(r))).length;
}
