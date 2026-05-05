import { useState, useEffect, useCallback } from 'react';

export interface HistorySession {
  id: string;
  provider: string;
  project: string;
  filePath: string;
  startTime: string;
  lastActive: string;
  turnCount: number;
  tokensIn: number;
  tokensOut: number;
  costUSD: number;
  firstPrompt: string;
  title: string;
  resumable: boolean;
  annotation: string;
  tags: string[];
  isSubagent: boolean;
}

type SortField = 'lastActive' | 'cost' | 'turns' | 'title' | 'project';
type SortDir = 'asc' | 'desc';

interface Props {
  onSelectSession: (session: HistorySession) => void;
  selectedId: string | null;
  onSessionCount?: (count: number) => void;
}

function shortProject(path: string): string {
  if (!path || path === '/') return '(global)';
  const parts = path.replace(/\/+$/, '').split('/');
  for (let i = parts.length - 1; i >= 0; i--) {
    if (parts[i] && parts[i] !== '.' && parts[i].length > 1) return parts[i];
  }
  return '(global)';
}

function formatAge(dateStr: string): string {
  if (!dateStr) return '?';
  const d = Date.now() - new Date(dateStr).getTime();
  if (d < 60_000) return 'now';
  if (d < 3_600_000) return `${Math.floor(d / 60_000)}m ago`;
  if (d < 86_400_000) return `${Math.floor(d / 3_600_000)}h ago`;
  if (d < 30 * 86_400_000) return `${Math.floor(d / 86_400_000)}d ago`;
  return `${Math.floor(d / (30 * 86_400_000))}mo ago`;
}

function formatK(n: number): string {
  if (n < 1000) return String(n);
  return (n / 1000).toFixed(1) + 'k';
}

export function SessionsTable({ onSelectSession, selectedId, onSessionCount }: Props) {
  const [sessions, setSessions] = useState<HistorySession[]>([]);
  const [loading, setLoading] = useState(true);
  const [sortField, setSortField] = useState<SortField>('lastActive');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  const [filter, setFilter] = useState('');
  const [deepQuery, setDeepQuery] = useState('');
  const [deepMatches, setDeepMatches] = useState<Map<string, string> | null>(null);
  const [searching, setSearching] = useState(false);
  const [showSubagents, setShowSubagents] = useState(false);
  const [copiedId, setCopiedId] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const resp = await fetch('/api/history');
        if (!resp.ok) return;
        const data = await resp.json();
        if (!cancelled) {
          const s = data.sessions || [];
          setSessions(s);
          setLoading(false);
          onSessionCount?.(s.length);
        }
      } catch {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => { cancelled = true; };
  }, []);

  const handleSort = (field: SortField) => {
    if (sortField === field) {
      setSortDir(d => d === 'asc' ? 'desc' : 'asc');
    } else {
      setSortField(field);
      setSortDir(field === 'title' || field === 'project' ? 'asc' : 'desc');
    }
  };

  const handleDeepSearch = useCallback(async () => {
    const q = deepQuery.trim();
    if (!q) { setDeepMatches(null); return; }
    setSearching(true);
    try {
      const resp = await fetch(`/api/search?q=${encodeURIComponent(q)}`);
      if (!resp.ok) return;
      const data = await resp.json();
      const m = new Map<string, string>();
      for (const r of data.results || []) {
        m.set(r.sessionId, r.snippet);
      }
      setDeepMatches(m);
    } catch {
      setDeepMatches(null);
    } finally {
      setSearching(false);
    }
  }, [deepQuery]);

  const clearDeepSearch = () => { setDeepQuery(''); setDeepMatches(null); };

  // Filter
  let visible = sessions;
  if (!showSubagents) {
    visible = visible.filter(s => !s.isSubagent);
  }
  // Hide near-empty sessions unless searching
  const isSearching = filter !== '' || deepMatches !== null;
  if (!isSearching) {
    visible = visible.filter(s => s.costUSD > 0 || s.turnCount > 5);
  }
  if (filter) {
    const q = filter.toLowerCase();
    visible = visible.filter(s =>
      (s.title || '').toLowerCase().includes(q) ||
      (s.firstPrompt || '').toLowerCase().includes(q) ||
      (s.project || '').toLowerCase().includes(q) ||
      (s.annotation || '').toLowerCase().includes(q) ||
      (s.tags || []).some(t => t.toLowerCase().includes(q))
    );
  }
  if (deepMatches) {
    const metaIds = new Set(visible.map(s => s.id));
    visible = visible.filter(s => metaIds.has(s.id) && deepMatches.has(s.id));
    // Also add sessions matched by content but not metadata
    for (const s of sessions) {
      if (deepMatches.has(s.id) && !visible.some(v => v.id === s.id)) {
        visible.push(s);
      }
    }
  }

  // Sort
  const sorted = [...visible].sort((a, b) => {
    let cmp = 0;
    switch (sortField) {
      case 'lastActive': {
        const at = a.lastActive ? new Date(a.lastActive).getTime() : 0;
        const bt = b.lastActive ? new Date(b.lastActive).getTime() : 0;
        cmp = at - bt;
        break;
      }
      case 'cost': cmp = a.costUSD - b.costUSD; break;
      case 'turns': cmp = a.turnCount - b.turnCount; break;
      case 'title': cmp = (a.title || a.firstPrompt || '').localeCompare(b.title || b.firstPrompt || ''); break;
      case 'project': cmp = shortProject(a.project).localeCompare(shortProject(b.project)); break;
    }
    return sortDir === 'asc' ? cmp : -cmp;
  });

  const subagentCount = sessions.filter(s => s.isSubagent).length;

  const annotationColor = (a: string) => {
    switch (a) {
      case 'achieved': return 'var(--green)';
      case 'partial': return 'var(--orange)';
      case 'failed': return 'var(--accent)';
      case 'abandoned': return 'var(--fg-3)';
      default: return 'var(--fg-4)';
    }
  };

  const SortHeader = ({ label, field, width, align }: { label: string; field: SortField; width?: number | string; align?: string }) => (
    <th
      onClick={() => handleSort(field)}
      style={{
        padding: '8px 10px', textAlign: (align as any) || 'left', cursor: 'pointer',
        fontSize: 9, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.06em',
        color: sortField === field ? 'var(--fg)' : 'var(--fg-3)',
        borderBottom: '1px solid var(--border)', whiteSpace: 'nowrap',
        width: width || 'auto', userSelect: 'none',
      }}
    >
      {label} {sortField === field ? (sortDir === 'asc' ? '\u25b2' : '\u25bc') : ''}
    </th>
  );

  if (loading) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <span style={{ color: 'var(--fg-3)', fontSize: 13 }}>Loading sessions...</span>
      </div>
    );
  }

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      {/* Toolbar */}
      <div style={{
        padding: '8px 18px', display: 'flex', alignItems: 'center', gap: 10,
        borderBottom: '1px solid var(--border)', flexShrink: 0, flexWrap: 'wrap',
      }}>
        <input
          type="text"
          placeholder="Filter sessions..."
          value={filter}
          onChange={e => setFilter(e.target.value)}
          style={{
            padding: '4px 10px', borderRadius: 4,
            border: '1px solid var(--border)', background: 'var(--bg-2)',
            color: 'var(--fg)', fontSize: 11, width: 180, outline: 'none',
          }}
        />
        <div style={{ width: 1, height: 16, background: 'var(--border)' }} />
        <input
          type="text"
          placeholder="Deep search (ripgrep)..."
          value={deepQuery}
          onChange={e => setDeepQuery(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') handleDeepSearch(); }}
          style={{
            padding: '4px 10px', borderRadius: 4,
            border: `1px solid ${deepMatches ? 'var(--purple)' : 'var(--border)'}`,
            background: 'var(--bg-2)', color: 'var(--fg)', fontSize: 11, width: 200, outline: 'none',
          }}
        />
        <button
          onClick={handleDeepSearch}
          disabled={searching || !deepQuery.trim()}
          style={{
            padding: '4px 8px', borderRadius: 4, border: '1px solid var(--purple)',
            background: 'transparent', color: 'var(--purple)', fontSize: 10, fontWeight: 600,
            cursor: searching ? 'wait' : 'pointer', opacity: !deepQuery.trim() ? 0.4 : 1,
          }}
        >
          {searching ? '...' : 'Search'}
        </button>
        {deepMatches && (
          <button onClick={clearDeepSearch} style={{
            padding: '4px 6px', borderRadius: 4, border: 'none',
            background: 'var(--purple-dim)', color: 'var(--purple)', fontSize: 9, fontWeight: 600, cursor: 'pointer',
          }}>
            {deepMatches.size} match{deepMatches.size !== 1 ? 'es' : ''} ✕
          </button>
        )}
        <div style={{ width: 1, height: 16, background: 'var(--border)' }} />
        <button
          onClick={() => setShowSubagents(v => !v)}
          style={{
            padding: '3px 10px', borderRadius: 12,
            border: `1px solid ${showSubagents ? 'var(--fg-3)' : 'var(--border)'}`,
            background: showSubagents ? 'var(--bg-3)' : 'transparent',
            color: showSubagents ? 'var(--fg)' : 'var(--fg-3)',
            fontSize: 10, cursor: 'pointer',
          }}
        >
          Subagents {subagentCount > 0 && `(${subagentCount})`}
        </button>
        <span style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--fg-4)' }}>
          {sorted.length} of {sessions.length} sessions
        </span>
      </div>

      {/* Table */}
      <div style={{ flex: 1, overflowY: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 11 }}>
          <thead style={{ position: 'sticky', top: 0, background: 'var(--bg-1)', zIndex: 1 }}>
            <tr>
              <SortHeader label="Age" field="lastActive" width={70} />
              <SortHeader label="Project" field="project" width={100} />
              <SortHeader label="Title" field="title" />
              <SortHeader label="Turns" field="turns" width={60} align="right" />
              <SortHeader label="Cost" field="cost" width={70} align="right" />
              <th style={{
                padding: '8px 10px', textAlign: 'right', fontSize: 9, fontWeight: 700,
                textTransform: 'uppercase', letterSpacing: '0.06em', color: 'var(--fg-4)',
                borderBottom: '1px solid var(--border)', width: 70,
              }}>
                Tokens
              </th>
              <th style={{
                padding: '8px 10px', textAlign: 'center', fontSize: 9, fontWeight: 700,
                textTransform: 'uppercase', letterSpacing: '0.06em', color: 'var(--fg-4)',
                borderBottom: '1px solid var(--border)', width: 50,
              }} />
            </tr>
          </thead>
          <tbody>
            {sorted.length === 0 && (
              <tr>
                <td colSpan={7} style={{ padding: '40px 10px', textAlign: 'center', color: 'var(--fg-3)', fontSize: 13 }}>
                  {isSearching ? 'No sessions match your search.' : 'No sessions found.'}
                </td>
              </tr>
            )}
            {sorted.map(s => {
              const isSelected = selectedId === s.id;
              const snippet = deepMatches?.get(s.id);
              const title = s.title || s.firstPrompt || '(no prompt)';
              return (
                <tr
                  key={s.id}
                  onClick={() => onSelectSession(s)}
                  style={{
                    cursor: 'pointer',
                    background: isSelected ? 'var(--bg-2)' : 'transparent',
                    borderLeft: isSelected ? '3px solid var(--accent)' : '3px solid transparent',
                  }}
                  onMouseEnter={e => { if (!isSelected) (e.currentTarget as HTMLElement).style.background = 'var(--bg-1)'; }}
                  onMouseLeave={e => { if (!isSelected) (e.currentTarget as HTMLElement).style.background = 'transparent'; }}
                >
                  <td style={{ padding: '8px 10px', color: 'var(--fg-3)', fontSize: 10, whiteSpace: 'nowrap' }}>
                    {formatAge(s.lastActive)}
                  </td>
                  <td style={{ padding: '8px 10px', fontSize: 10 }}>
                    <span style={{ color: 'var(--purple)', fontFamily: 'var(--mono)' }}>
                      {shortProject(s.project)}
                    </span>
                  </td>
                  <td style={{ padding: '8px 10px' }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        {s.annotation && (
                          <span style={{
                            fontSize: 8, fontWeight: 700, textTransform: 'uppercase',
                            padding: '1px 4px', borderRadius: 2,
                            color: annotationColor(s.annotation),
                            border: `1px solid ${annotationColor(s.annotation)}`,
                          }}>
                            {s.annotation}
                          </span>
                        )}
                        {s.tags?.map(t => (
                          <span key={t} style={{
                            fontSize: 8, padding: '1px 4px', borderRadius: 2,
                            background: 'var(--accent-dim)', color: 'var(--accent)',
                          }}>
                            {t}
                          </span>
                        ))}
                        <span style={{
                          color: 'var(--fg)', fontSize: 11,
                          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }}>
                          {title}
                        </span>
                        {s.isSubagent && (
                          <span style={{ fontSize: 8, color: 'var(--fg-4)', fontStyle: 'italic' }}>agent</span>
                        )}
                      </div>
                      {snippet && (
                        <span style={{
                          fontSize: 9, fontFamily: 'var(--mono)', color: 'var(--purple)',
                          fontStyle: 'italic', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }}>
                          {snippet}
                        </span>
                      )}
                    </div>
                  </td>
                  <td style={{ padding: '8px 10px', textAlign: 'right', fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--teal)' }}>
                    {s.turnCount}t
                  </td>
                  <td style={{ padding: '8px 10px', textAlign: 'right', fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--green)' }}>
                    ${s.costUSD.toFixed(2)}
                  </td>
                  <td style={{ padding: '8px 10px', textAlign: 'right', fontFamily: 'var(--mono)', fontSize: 9, color: 'var(--fg-4)' }}>
                    {formatK(s.tokensIn)}/{formatK(s.tokensOut)}
                  </td>
                  <td style={{ padding: '4px 8px', textAlign: 'center' }}>
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        const cmd = `claude --resume ${s.id}`;
                        navigator.clipboard.writeText(cmd);
                        setCopiedId(s.id);
                        setTimeout(() => setCopiedId(prev => prev === s.id ? null : prev), 1500);
                      }}
                      title={`Copy: claude --resume ${s.id}`}
                      style={{
                        background: 'transparent',
                        border: `1px solid ${copiedId === s.id ? 'var(--green)' : 'var(--border)'}`,
                        color: copiedId === s.id ? 'var(--green)' : 'var(--fg-3)',
                        fontSize: 9, fontWeight: 600, cursor: 'pointer',
                        padding: '2px 6px', borderRadius: 3,
                        transition: 'all 0.15s',
                      }}
                    >
                      {copiedId === s.id ? 'Copied' : 'Copy'}
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
