import React, { useState, useEffect, useCallback } from 'react';

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
  note: string;
  isSubagent: boolean;
  permissionMode: string;
  starred: boolean;
  gitBranch?: string;
  lastPrompt?: string;
  lastAction?: string;
  model?: string;
  roiMultiplier?: number;
  taskType?: string;
  durationMin?: number;
}

type SortField = 'lastActive' | 'cost' | 'turns' | 'title' | 'project' | 'roi';
type SortDir = 'asc' | 'desc';

interface Props {
  onSelectSession: (session: HistorySession) => void;
  selectedId: string | null;
  onSessionCount?: (count: number) => void;
  starredOnly?: boolean;
  initialSessions?: HistorySession[] | null;
  onSessionsLoaded?: (sessions: HistorySession[]) => void;
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

export function SessionsTable({ onSelectSession, selectedId, onSessionCount, starredOnly, initialSessions, onSessionsLoaded }: Props) {
  const [sessions, setSessions] = useState<HistorySession[]>([]);
  const [loading, setLoading] = useState(true);
  const [sortField, setSortField] = useState<SortField>('lastActive');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  const [filter, setFilter] = useState('');
  const [dirFilter, setDirFilter] = useState('');
  const [deepQuery, setDeepQuery] = useState('');
  const [deepMatches, setDeepMatches] = useState<Map<string, string> | null>(null);
  const [searching, setSearching] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [genResult, setGenResult] = useState<string | null>(null);
  const [showSubagents, setShowSubagents] = useState(false);
  const [showAnalyzers, setShowAnalyzers] = useState(false);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [hourlyRate, setHourlyRate] = useState(150);
  const [roiDetailId, setRoiDetailId] = useState<string | null>(null);

  useEffect(() => {
    if (initialSessions) {
      setSessions(initialSessions);
      setLoading(false);
      onSessionCount?.(initialSessions.length);
      return;
    }
    let cancelled = false;
    async function load() {
      try {
        const resp = await fetch('/api/history');
        if (!resp.ok) return;
        const data = await resp.json();
        if (!cancelled) {
          const s = (data.sessions || []).map((sess: any) => ({ ...sess, note: sess.note || '' }));
          setSessions(s);
          setLoading(false);
          onSessionCount?.(s.length);
          onSessionsLoaded?.(s);
        }
      } catch {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => { cancelled = true; };
  }, [initialSessions]);

  useEffect(() => {
    fetch('/api/config/roi').then(r => r.json()).then(d => {
      if (d.hourlyRate > 0) setHourlyRate(d.hourlyRate);
    }).catch(() => {});
  }, []);

  function computeROI(s: HistorySession): { netROI: number; timeSavedMin: number; valueUSD: number; ratio: number } | null {
    const mult = s.roiMultiplier || 0;
    const dur = s.durationMin || 0;
    if (mult <= 0 || dur <= 0) return null;
    const timeSavedMin = dur * mult - dur;
    const valueUSD = timeSavedMin * (hourlyRate / 60);
    const netROI = valueUSD - s.costUSD;
    const ratio = s.costUSD > 0 ? netROI / s.costUSD : 0;
    return { netROI, timeSavedMin, valueUSD, ratio };
  }

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

  const handleGenerateTitles = async () => {
    setGenerating(true);
    setGenResult(null);
    try {
      const resp = await fetch('/api/sessions/generate-titles', { method: 'POST' });
      const data = await resp.json();
      setGenResult(`Generated ${data.generated} title${data.generated !== 1 ? 's' : ''}`);
      if (data.generated > 0) {
        const histResp = await fetch('/api/history');
        if (histResp.ok) {
          const histData = await histResp.json();
          const s = (histData.sessions || []).map((sess: any) => ({ ...sess, note: sess.note || '' }));
          setSessions(s);
          onSessionsLoaded?.(s);
        }
      }
    } catch {
      setGenResult('Failed to generate titles');
    } finally {
      setGenerating(false);
      setTimeout(() => setGenResult(null), 3000);
    }
  };

  const handleToggleStar = async (session: HistorySession) => {
    const newStarred = !session.starred;
    await fetch('/api/sessions/meta', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ filePath: session.filePath, starred: newStarred }),
    });
    const updated = sessions.map(s => s.id === session.id ? { ...s, starred: newStarred } : s);
    setSessions(updated);
    onSessionsLoaded?.(updated);
  };

  const annotationCycle = ['achieved', 'partial', 'failed', 'abandoned', ''];

  const handleCycleAnnotation = async (session: HistorySession) => {
    const current = session.annotation || '';
    const idx = annotationCycle.indexOf(current);
    const next = annotationCycle[(idx + 1) % annotationCycle.length];
    await fetch('/api/sessions/meta', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ filePath: session.filePath, annotation: next }),
    });
    setSessions(prev => prev.map(s => s.id === session.id ? { ...s, annotation: next } : s));
  };

  // Filter
  let visible = sessions;
  if (starredOnly) {
    visible = visible.filter(s => s.starred);
  }
  if (!showSubagents) {
    visible = visible.filter(s => !s.isSubagent);
  }
  const isAnalyzer = (s: HistorySession) =>
    (s.firstPrompt || '').toUpperCase().includes('SESSION ANALYZER') ||
    (s.title || '').toUpperCase().includes('SESSION ANALYZER');
  if (!showAnalyzers) {
    visible = visible.filter(s => !isAnalyzer(s));
  }
  // Hide near-empty sessions unless searching
  const isSearching = filter !== '' || deepMatches !== null;
  if (!isSearching) {
    visible = visible.filter(s => s.costUSD > 0 || s.turnCount > 5);
  }
  if (dirFilter) {
    const dq = dirFilter.toLowerCase();
    visible = visible.filter(s => (s.project || '').toLowerCase().includes(dq));
  }
  if (filter) {
    const q = filter.toLowerCase();
    visible = visible.filter(s =>
      (s.title || '').toLowerCase().includes(q) ||
      (s.firstPrompt || '').toLowerCase().includes(q) ||
      (s.lastPrompt || '').toLowerCase().includes(q) ||
      (s.gitBranch || '').toLowerCase().includes(q) ||
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

  // Sort (starred first, then by selected field)
  const sorted = [...visible].sort((a, b) => {
    if (a.starred !== b.starred) return a.starred ? -1 : 1;
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
      case 'title': cmp = (a.title || a.lastPrompt || a.firstPrompt || '').localeCompare(b.title || b.lastPrompt || b.firstPrompt || ''); break;
      case 'project': cmp = shortProject(a.project).localeCompare(shortProject(b.project)); break;
      case 'roi': {
        const ar = computeROI(a)?.netROI || 0;
        const br = computeROI(b)?.netROI || 0;
        cmp = ar - br;
        break;
      }
    }
    return sortDir === 'asc' ? cmp : -cmp;
  });

  const subagentCount = sessions.filter(s => s.isSubagent).length;
  const analyzerCount = sessions.filter(s => isAnalyzer(s)).length;

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
        padding: '10px 12px', textAlign: (align as any) || 'left', cursor: 'pointer',
        fontSize: 11, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.06em',
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
            padding: '5px 10px', borderRadius: 4,
            border: '1px solid var(--border)', background: 'var(--bg-2)',
            color: 'var(--fg)', fontSize: 12, width: 180, outline: 'none',
          }}
        />
        <input
          type="text"
          placeholder="Filter by path..."
          value={dirFilter}
          onChange={e => setDirFilter(e.target.value)}
          style={{
            padding: '5px 10px', borderRadius: 4,
            border: `1px solid ${dirFilter ? 'var(--blue)' : 'var(--border)'}`,
            background: 'var(--bg-2)',
            color: 'var(--fg)', fontSize: 12, width: 160, outline: 'none',
          }}
        />
        {dirFilter && (
          <button onClick={() => setDirFilter('')} style={{
            padding: '4px 6px', borderRadius: 4, border: 'none',
            background: 'var(--bg-3)', color: 'var(--fg-3)', fontSize: 10, fontWeight: 600, cursor: 'pointer',
          }}>
            ✕
          </button>
        )}
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
            background: 'var(--purple-dim)', color: 'var(--purple)', fontSize: 10, fontWeight: 600, cursor: 'pointer',
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
        {analyzerCount > 0 && (
          <button
            onClick={() => setShowAnalyzers(v => !v)}
            style={{
              padding: '3px 10px', borderRadius: 12,
              border: `1px solid ${showAnalyzers ? 'var(--fg-3)' : 'var(--border)'}`,
              background: showAnalyzers ? 'var(--bg-3)' : 'transparent',
              color: showAnalyzers ? 'var(--fg)' : 'var(--fg-3)',
              fontSize: 10, cursor: 'pointer',
            }}
          >
            Analyzers ({analyzerCount})
          </button>
        )}
        <button
          onClick={handleGenerateTitles}
          disabled={generating}
          style={{
            padding: '3px 10px', borderRadius: 12,
            border: '1px solid var(--border)',
            background: 'transparent',
            color: generating ? 'var(--fg-4)' : 'var(--fg-3)',
            fontSize: 10, cursor: generating ? 'wait' : 'pointer',
          }}
        >
          {generating ? 'Generating...' : 'Generate Titles'}
        </button>
        {genResult && (
          <span style={{ fontSize: 10, color: 'var(--green)' }}>{genResult}</span>
        )}
        <span style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--fg-4)' }}>
          {sorted.length} of {sessions.length} sessions
        </span>
      </div>

      {/* Table */}
      <div style={{ flex: 1, overflowY: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
          <thead style={{ position: 'sticky', top: 0, background: 'var(--bg-1)', zIndex: 1 }}>
            <tr>
              <SortHeader label="Age" field="lastActive" width={80} />
              <SortHeader label="Project" field="project" width={110} />
              <SortHeader label="Title" field="title" />
              <SortHeader label="Turns" field="turns" width={70} align="right" />
              <SortHeader label="Cost" field="cost" width={90} align="right" />
              <SortHeader label="ROI" field="roi" width={90} align="right" />
              <th style={{
                padding: '10px 12px', textAlign: 'center', fontSize: 11, fontWeight: 700,
                textTransform: 'uppercase', letterSpacing: '0.06em', color: 'var(--fg-4)',
                borderBottom: '1px solid var(--border)', width: 70,
              }}>
                Eval
              </th>
              <th style={{
                padding: '10px 12px', textAlign: 'right', fontSize: 11, fontWeight: 700,
                textTransform: 'uppercase', letterSpacing: '0.06em', color: 'var(--fg-4)',
                borderBottom: '1px solid var(--border)', width: 110,
              }}>
                Tokens
              </th>
              <th style={{
                padding: '10px 12px', textAlign: 'center', fontSize: 11, fontWeight: 700,
                textTransform: 'uppercase', letterSpacing: '0.06em', color: 'var(--fg-4)',
                borderBottom: '1px solid var(--border)', width: 60,
              }} />
            </tr>
          </thead>
          <tbody>
            {sorted.length === 0 && (
              <tr>
                <td colSpan={9} style={{ padding: '40px 10px', textAlign: 'center', color: 'var(--fg-3)', fontSize: 13 }}>
                  {isSearching ? 'No sessions match your search.' : 'No sessions found.'}
                </td>
              </tr>
            )}
            {sorted.map(s => {
              const isSelected = selectedId === s.id;
              const snippet = deepMatches?.get(s.id);
              const title = s.title || s.lastPrompt || s.firstPrompt || '(no prompt)';
              return (
                <React.Fragment key={s.id}>
                <tr
                  onClick={() => onSelectSession(s)}
                  style={{
                    cursor: 'pointer',
                    background: isSelected ? 'var(--bg-2)' : 'transparent',
                    borderLeft: isSelected ? '3px solid var(--accent)' : '3px solid transparent',
                  }}
                  onMouseEnter={e => { if (!isSelected) (e.currentTarget as HTMLElement).style.background = 'var(--bg-1)'; }}
                  onMouseLeave={e => { if (!isSelected) (e.currentTarget as HTMLElement).style.background = 'transparent'; }}
                >
                  <td style={{ padding: '12px 12px', color: 'var(--fg-3)', fontSize: 13, whiteSpace: 'nowrap' }}>
                    {formatAge(s.lastActive)}
                  </td>
                  <td style={{ padding: '12px 12px', fontSize: 13 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                      <span style={{ color: 'var(--purple)', fontFamily: 'var(--mono)', fontWeight: 600 }}>
                        {shortProject(s.project)}
                      </span>
                      {s.gitBranch && s.gitBranch !== 'main' && s.gitBranch !== 'master' && (
                        <span style={{
                          fontFamily: 'var(--mono)', fontSize: 11, padding: '2px 6px',
                          borderRadius: 3, background: 'rgba(167, 139, 250, 0.12)',
                          color: '#C4B5FD', border: '1px solid rgba(167, 139, 250, 0.25)',
                        }}>
                          {s.gitBranch.length > 16 ? s.gitBranch.slice(0, 14) + '…' : s.gitBranch}
                        </span>
                      )}
                    </div>
                  </td>
                  <td style={{ padding: '12px 12px' }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        {s.annotation && (
                          <span style={{
                            fontSize: 9, fontWeight: 700, textTransform: 'uppercase',
                            padding: '2px 5px', borderRadius: 3,
                            color: annotationColor(s.annotation),
                            border: `1px solid ${annotationColor(s.annotation)}`,
                          }}>
                            {s.annotation}
                          </span>
                        )}
                        {s.tags?.map(t => (
                          <span key={t} style={{
                            fontSize: 9, padding: '2px 5px', borderRadius: 3,
                            background: 'var(--accent-dim)', color: 'var(--accent)',
                          }}>
                            {t}
                          </span>
                        ))}
                        <span style={{
                          color: 'var(--fg)', fontSize: 13,
                          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }}>
                          {title}
                        </span>
                        {s.isSubagent && (
                          <span style={{ fontSize: 10, color: 'var(--fg-4)', fontStyle: 'italic' }}>agent</span>
                        )}
                      </div>
                      {s.lastAction && (
                        <span style={{
                          fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--fg-4)',
                          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }}>
                          {s.lastAction.length > 40 ? s.lastAction.slice(0, 38) + '…' : s.lastAction}
                        </span>
                      )}
                      {snippet && (
                        <span style={{
                          fontSize: 11, fontFamily: 'var(--mono)', color: 'var(--purple)',
                          fontStyle: 'italic', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }}>
                          {snippet}
                        </span>
                      )}
                    </div>
                  </td>
                  <td style={{ padding: '12px 12px', textAlign: 'right', fontFamily: 'var(--mono)', fontSize: 13, color: 'var(--teal)', fontWeight: 600 }}>
                    {s.turnCount}
                  </td>
                  <td style={{ padding: '12px 12px', textAlign: 'right', fontFamily: 'var(--mono)', fontSize: 14, color: 'var(--green)', fontWeight: 700 }}>
                    ${s.costUSD.toFixed(2)}
                  </td>
                  <td
                    onClick={(e) => { e.stopPropagation(); setRoiDetailId(prev => prev === s.id ? null : s.id); }}
                    title="Click for ROI breakdown"
                    style={{
                      padding: '12px 12px', textAlign: 'right', fontFamily: 'var(--mono)', fontSize: 14, fontWeight: 700,
                      cursor: 'pointer',
                      color: (() => { const r = computeROI(s); if (!r) return 'var(--fg-4)'; return r.netROI >= 0 ? 'var(--green)' : 'var(--accent)'; })(),
                    }}
                  >
                    {(() => {
                      const r = computeROI(s);
                      if (!r) return '--';
                      if (Math.abs(r.netROI) >= 1000) return `$${(r.netROI / 1000).toFixed(1)}K`;
                      return `$${Math.round(r.netROI)}`;
                    })()}
                  </td>
                  <td style={{ padding: '6px 10px', textAlign: 'center' }}>
                    <button
                      onClick={(e) => { e.stopPropagation(); handleCycleAnnotation(s); }}
                      title="Click to cycle: achieved, partial, failed, abandoned, clear"
                      style={{
                        background: 'transparent',
                        border: `1px solid ${s.annotation ? annotationColor(s.annotation) : 'var(--border)'}`,
                        color: s.annotation ? annotationColor(s.annotation) : 'var(--fg-4)',
                        fontSize: 9, fontWeight: 700, textTransform: 'uppercase',
                        padding: '3px 8px', borderRadius: 3, cursor: 'pointer',
                        transition: 'all 0.15s',
                      }}
                    >
                      {s.annotation || '+'}
                    </button>
                  </td>
                  <td style={{ padding: '12px 12px', textAlign: 'right', fontFamily: 'var(--mono)', fontSize: 12 }}>
                    <span style={{ color: 'var(--fg-3)' }}>{formatK(s.tokensIn)}</span>
                    <span style={{ color: 'var(--fg-4)', margin: '0 2px' }}>/</span>
                    <span style={{ color: 'var(--fg-3)' }}>{formatK(s.tokensOut)}</span>
                  </td>
                  <td style={{ padding: '6px 10px', textAlign: 'center' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, justifyContent: 'center' }}>
                      <button
                        onClick={(e) => { e.stopPropagation(); handleToggleStar(s); }}
                        title={s.starred ? 'Unpin session' : 'Pin session'}
                        style={{
                          background: 'transparent', border: 'none',
                          color: s.starred ? 'var(--orange)' : 'var(--fg-4)',
                          fontSize: 16, cursor: 'pointer', padding: '2px 4px',
                        }}
                      >
                        {s.starred ? '★' : '☆'}
                      </button>
                      <button
                        onClick={(e) => {
                          e.stopPropagation();
                          const cmd = s.project
                            ? `cd ${s.project} && claude --resume ${s.id}`
                            : `claude --resume ${s.id}`;
                          navigator.clipboard.writeText(cmd);
                          setCopiedId(s.id);
                          setTimeout(() => setCopiedId(prev => prev === s.id ? null : prev), 1500);
                        }}
                        title={s.project ? `Copy: cd ${s.project} && claude --resume ${s.id}` : `Copy: claude --resume ${s.id}`}
                        style={{
                          background: 'transparent',
                          border: `1px solid ${copiedId === s.id ? 'var(--green)' : 'var(--border)'}`,
                          color: copiedId === s.id ? 'var(--green)' : 'var(--fg-3)',
                          fontSize: 11, fontWeight: 600, cursor: 'pointer',
                          padding: '3px 8px', borderRadius: 3,
                          transition: 'all 0.15s',
                        }}
                      >
                        {copiedId === s.id ? 'Copied' : 'Copy'}
                      </button>
                    </div>
                  </td>
                </tr>
                {roiDetailId === s.id && (() => {
                  const r = computeROI(s);
                  if (!r) return null;
                  const dur = s.durationMin || 0;
                  const mult = s.roiMultiplier || 0;
                  const taskLabel = s.taskType || 'general';
                  void 0;
                  return (
                    <tr style={{ background: 'var(--bg-2)' }}>
                      <td colSpan={9} style={{ padding: '12px 24px', borderBottom: '1px solid var(--border)' }}>
                        <div style={{ display: 'flex', gap: 32, flexWrap: 'wrap', fontSize: 12, fontFamily: 'var(--mono)' }}>
                          <div>
                            <span style={{ color: 'var(--fg-4)' }}>Duration </span>
                            <span style={{ color: 'var(--fg)' }}>{Math.round(dur)}min</span>
                          </div>
                          <div>
                            <span style={{ color: 'var(--fg-4)' }}>Multiplier </span>
                            <span style={{ color: 'var(--teal)' }}>{mult.toFixed(1)}x</span>
                            <span style={{ color: 'var(--fg-4)', marginLeft: 4, fontSize: 10 }}>({taskLabel})</span>
                          </div>
                          <div>
                            <span style={{ color: 'var(--fg-4)' }}>Rate </span>
                            <span style={{ color: 'var(--fg)' }}>${hourlyRate}/hr</span>
                          </div>
                          <div>
                            <span style={{ color: 'var(--fg-4)' }}>Time saved </span>
                            <span style={{ color: 'var(--green)' }}>{Math.round(r.timeSavedMin)}min</span>
                          </div>
                          <div>
                            <span style={{ color: 'var(--fg-4)' }}>Value </span>
                            <span style={{ color: 'var(--green)' }}>${r.valueUSD.toFixed(2)}</span>
                          </div>
                          <div>
                            <span style={{ color: 'var(--fg-4)' }}>Cost </span>
                            <span style={{ color: 'var(--orange)' }}>${s.costUSD.toFixed(2)}</span>
                          </div>
                          <div>
                            <span style={{ color: 'var(--fg-4)' }}>Net ROI </span>
                            <span style={{ color: r.netROI >= 0 ? 'var(--green)' : 'var(--accent)', fontWeight: 700 }}>
                              ${r.netROI.toFixed(2)}
                            </span>
                          </div>
                          <div>
                            <span style={{ color: 'var(--fg-4)' }}>Ratio </span>
                            <span style={{ color: 'var(--fg)' }}>{Math.round(r.ratio)}x</span>
                          </div>
                        </div>
                        <div style={{ marginTop: 8, fontSize: 10, color: 'var(--fg-4)', lineHeight: 1.5 }}>
                          {taskLabel === 'general'
                            ? 'Baseline 1.5x multiplier (enterprise floor from Google RCT: 21-26% speedup).'
                            : `${taskLabel} multiplier (${mult}x) inferred from skill usage. Source: McKinsey/DX/GitHub RCT data with enterprise discount.`
                          }
                          {' '}Formula: (duration x multiplier x rate) - cost.
                        </div>
                      </td>
                    </tr>
                  );
                })()}
                </React.Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
