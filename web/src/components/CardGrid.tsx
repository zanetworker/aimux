import { useState, useEffect } from 'react';
import type { Agent } from '../types';
import type { ContentSearchResult } from '../App';
import { AgentCard } from './AgentCard';

interface Props {
  agents: Agent[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  statusFilter: number | null;
  providerFilter: string | null;
  recentFilter: boolean;
  searchQuery: string;
  sortBy: string;
  contentResults?: ContentSearchResult[] | null;
  loading?: boolean;
  viewMode?: 'cards' | 'list';
}

function projectName(agent: Agent): string {
  return agent.Name.replace(/ #\d+$/, '');
}

export function CardGrid({
  agents,
  selectedId,
  onSelect,
  statusFilter,
  providerFilter,
  recentFilter,
  searchQuery,
  sortBy,
  contentResults,
  loading,
  viewMode = 'cards',
}: Props) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [starredFiles, setStarredFiles] = useState<Set<string>>(new Set());

  useEffect(() => {
    const starred = new Set<string>();
    for (const a of agents) {
      if (a.Starred && a.SessionFile) starred.add(a.SessionFile);
    }
    setStarredFiles(prev => {
      if (prev.size === starred.size && [...prev].every(f => starred.has(f))) return prev;
      return starred;
    });
  }, [agents]);

  const handleToggleStar = async (sessionFile: string) => {
    const isStarred = starredFiles.has(sessionFile);
    try {
      await fetch('/api/sessions/meta', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ filePath: sessionFile, starred: !isStarred }),
      });
      setStarredFiles(prev => {
        const next = new Set(prev);
        if (isStarred) { next.delete(sessionFile); } else { next.add(sessionFile); }
        return next;
      });
    } catch { /* ignore */ }
  };

  const handleKill = async (id: string) => {
    try {
      await fetch(`/api/agents/${id}/archive`, { method: 'POST' });
    } catch { /* ignore */ }
  };

  const toggleGroup = (name: string) => {
    setCollapsed(prev => {
      const next = new Set(prev);
      if (next.has(name)) { next.delete(name); } else { next.add(name); }
      return next;
    });
  };

  const contentMatchMap = new Map<string, string>();
  if (contentResults) {
    for (const r of contentResults) {
      contentMatchMap.set(r.sessionId, r.snippet);
    }
  }

  let filtered = agents;

  if (statusFilter !== null) {
    filtered = filtered.filter(a => a.Status === statusFilter);
  }
  if (providerFilter !== null) {
    filtered = filtered.filter(a => a.ProviderName.toLowerCase() === providerFilter.toLowerCase());
  }
  if (recentFilter) {
    const thirtyMinAgo = Date.now() - 30 * 60 * 1000;
    filtered = filtered.filter(a => new Date(a.LastActivity).getTime() > thirtyMinAgo);
  }
  if (searchQuery) {
    const query = searchQuery.toLowerCase();
    filtered = filtered.filter(a =>
      a.Name.toLowerCase().includes(query) ||
      (a.GitBranch || '').toLowerCase().includes(query) ||
      (a.TaskSubject || '').toLowerCase().includes(query) ||
      (a.WorkingDir || '').toLowerCase().includes(query) ||
      (a.Title || '').toLowerCase().includes(query)
    );
  }
  if (contentResults && contentResults.length > 0) {
    filtered = filtered.filter(a => contentMatchMap.has(a.SessionID));
  }

  // Stable sort: primary key from dropdown, SessionID as tiebreaker
  const sorted = [...filtered].sort((a, b) => {
    let cmp = 0;
    switch (sortBy) {
      case 'lastActive': {
        const aTime = a.LastActivity ? new Date(a.LastActivity).getTime() : 0;
        const bTime = b.LastActivity ? new Date(b.LastActivity).getTime() : 0;
        cmp = bTime - aTime;
        break;
      }
      case 'cost':
        cmp = (b.EstCostUSD || 0) - (a.EstCostUSD || 0);
        break;
      case 'repo':
        cmp = a.Name.localeCompare(b.Name);
        break;
      case 'status':
        cmp = a.Status - b.Status;
        break;
    }
    if (cmp !== 0) return cmp;
    return (a.SessionID || '').localeCompare(b.SessionID || '');
  });

  // Group by project, preserving sort order within groups.
  const groupMap = new Map<string, Agent[]>();
  for (const a of sorted) {
    const name = projectName(a);
    if (!groupMap.has(name)) groupMap.set(name, []);
    groupMap.get(name)!.push(a);
  }

  // Stable group order: sort by most recent activity, then project name as tiebreaker
  const groups = [...groupMap.entries()].sort((a, b) => {
    const aTime = a[1][0].LastActivity ? new Date(a[1][0].LastActivity).getTime() : 0;
    const bTime = b[1][0].LastActivity ? new Date(b[1][0].LastActivity).getTime() : 0;
    if (aTime !== bTime) return bTime - aTime;
    return a[0].localeCompare(b[0]);
  });

  if (sorted.length === 0) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <div style={{ color: 'var(--fg-3)', fontSize: 13 }}>
          {loading ? 'Discovering agents...' : contentResults ? 'No sessions match your search.' : 'No active agents.'}
        </div>
      </div>
    );
  }

  return (
    <div style={{ flex: 1, overflowY: 'auto', padding: '10px 18px' }}>
      {groups.map(([name, groupAgents]) => {
        const isCollapsed = collapsed.has(name);
        const hasActive = groupAgents.some(a => a.Status === 0);
        const hasAttention = groupAgents.some(a => a.Status === 2 || a.Status === 3);
        const groupCost = groupAgents.reduce((s, a) => s + (a.EstCostUSD || 0), 0);

        return (
          <div key={name} style={{ marginBottom: 12 }}>
            {/* Group header */}
            <div
              onClick={() => toggleGroup(name)}
              style={{
                display: 'flex', alignItems: 'center', gap: 8,
                padding: '6px 8px', cursor: 'pointer',
                borderBottom: '1px solid var(--border)',
                marginBottom: isCollapsed ? 0 : 8,
              }}
            >
              <span style={{
                fontSize: 11, color: 'var(--fg-4)', width: 12,
                transition: 'transform 0.15s',
                transform: isCollapsed ? 'rotate(0deg)' : 'rotate(90deg)',
                display: 'inline-block',
              }}>
                ▶
              </span>
              {hasActive && (
                <div style={{
                  width: 8, height: 8, borderRadius: '50%',
                  background: 'var(--green)', flexShrink: 0,
                  animation: 'pulse 2s ease-in-out infinite',
                }} />
              )}
              {hasAttention && !hasActive && (
                <div style={{
                  width: 8, height: 8, borderRadius: '50%',
                  background: 'var(--orange)', flexShrink: 0,
                }} />
              )}
              <span style={{
                fontSize: 14, fontWeight: 600, color: 'var(--fg)',
                letterSpacing: '-0.01em',
              }}>
                {name}
              </span>
              <span style={{ fontSize: 12, color: 'var(--fg-4)' }}>
                {groupAgents.length} session{groupAgents.length !== 1 ? 's' : ''}
              </span>
              <span style={{
                fontSize: 12, fontFamily: 'var(--mono)',
                color: 'var(--green)', marginLeft: 'auto',
              }}>
                ${groupCost.toFixed(2)}
              </span>
            </div>

            {/* Agents */}
            {!isCollapsed && viewMode === 'cards' && (
              <div
                role="list"
                style={{
                  display: 'grid',
                  gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))',
                  gridAutoRows: '1fr',
                  gap: 8,
                }}
              >
                {groupAgents.map(agent => (
                  <div key={agent.SessionID || agent.PID} role="listitem" style={{ display: 'flex' }}>
                    <AgentCard
                      agent={agent}
                      selected={selectedId === (agent.SessionID || agent.PID.toString())}
                      starred={starredFiles.has(agent.SessionFile)}
                      onClick={() => onSelect(agent.SessionID || agent.PID.toString())}
                      onKill={handleKill}
                      onToggleStar={handleToggleStar}
                      searchSnippet={contentMatchMap.get(agent.SessionID)}
                    />
                  </div>
                ))}
              </div>
            )}
            {!isCollapsed && viewMode === 'list' && (
              <div role="list">
                {groupAgents.map(agent => {
                  const id = agent.SessionID || agent.PID.toString();
                  const isSelected = selectedId === id;
                  const statusColors: Record<number, string> = { 0: 'var(--green)', 1: 'var(--fg-3)', 2: 'var(--orange)', 3: 'var(--accent)' };
                  const statusLabels: Record<number, string> = { 0: 'ACTIVE', 1: 'IDLE', 2: 'WAITING', 3: 'ERROR' };
                  const providerColors: Record<string, string> = { claude: 'var(--accent)', codex: 'var(--green)', gemini: 'var(--purple)' };
                  const age = agent.LastActivity ? (() => {
                    const ms = Date.now() - new Date(agent.LastActivity).getTime();
                    if (ms < 60000) return `${Math.floor(ms/1000)}s ago`;
                    if (ms < 3600000) return `${Math.floor(ms/60000)}m ago`;
                    if (ms < 86400000) return `${Math.floor(ms/3600000)}h ago`;
                    return `${Math.floor(ms/86400000)}d ago`;
                  })() : '';
                  return (
                    <div key={id} role="listitem" onClick={() => onSelect(id)}
                      style={{
                        display: 'flex', alignItems: 'center', gap: 12,
                        padding: '8px 10px', cursor: 'pointer', borderRadius: 4,
                        background: isSelected ? 'var(--bg-2)' : 'transparent',
                        borderBottom: '1px solid var(--border)',
                        transition: 'background 0.1s',
                      }}
                      onMouseEnter={e => { if (!isSelected) e.currentTarget.style.background = 'var(--bg-1)'; }}
                      onMouseLeave={e => { if (!isSelected) e.currentTarget.style.background = 'transparent'; }}>
                      <div style={{ width: 8, height: 8, borderRadius: '50%', background: statusColors[agent.Status] || 'var(--fg-4)', flexShrink: 0 }} />
                      <span style={{ fontSize: 12, fontWeight: 600, textTransform: 'uppercase', color: providerColors[agent.ProviderName] || 'var(--fg-3)', width: 55, flexShrink: 0 }}>
                        {agent.ProviderName}
                      </span>
                      <span style={{ fontSize: 11, color: statusColors[agent.Status], width: 55, flexShrink: 0 }}>
                        {statusLabels[agent.Status] || ''}
                      </span>
                      <span style={{ fontSize: 13, color: 'var(--fg)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {agent.Title || agent.Name}
                      </span>
                      <span style={{ fontSize: 11, color: 'var(--fg-4)', width: 60, textAlign: 'right', flexShrink: 0 }}>
                        {age}
                      </span>
                      <span style={{ fontSize: 13, fontFamily: 'var(--mono)', color: 'var(--green)', width: 80, textAlign: 'right', flexShrink: 0 }}>
                        ${(agent.EstCostUSD || 0).toFixed(2)}
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        );
      })}

      <style>{`
        @keyframes pulse {
          0%, 100% { opacity: 1; }
          50% { opacity: 0.5; }
        }
      `}</style>
    </div>
  );
}
