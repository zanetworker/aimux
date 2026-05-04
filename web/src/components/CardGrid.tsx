import { useState } from 'react';
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
}: Props) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

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

  const sorted = [...filtered].sort((a, b) => {
    switch (sortBy) {
      case 'lastActive': {
        const aTime = a.LastActivity ? new Date(a.LastActivity).getTime() : 0;
        const bTime = b.LastActivity ? new Date(b.LastActivity).getTime() : 0;
        return bTime - aTime;
      }
      case 'cost':
        return (b.EstCostUSD || 0) - (a.EstCostUSD || 0);
      case 'repo':
        return a.Name.localeCompare(b.Name);
      case 'status':
        return a.Status - b.Status;
      default:
        return 0;
    }
  });

  // Group by project, preserving sort order within groups.
  // Group order = most recent activity in the group.
  const groupMap = new Map<string, Agent[]>();
  for (const a of sorted) {
    const name = projectName(a);
    if (!groupMap.has(name)) groupMap.set(name, []);
    groupMap.get(name)!.push(a);
  }

  const groups = [...groupMap.entries()].sort((a, b) => {
    const aTime = a[1][0].LastActivity ? new Date(a[1][0].LastActivity).getTime() : 0;
    const bTime = b[1][0].LastActivity ? new Date(b[1][0].LastActivity).getTime() : 0;
    return bTime - aTime;
  });

  if (sorted.length === 0) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <div style={{ color: 'var(--fg-3)', fontSize: 13 }}>
          {contentResults ? 'No sessions match your search.' : 'No sessions found.'}
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
                fontSize: 9, color: 'var(--fg-4)', width: 12,
                transition: 'transform 0.15s',
                transform: isCollapsed ? 'rotate(0deg)' : 'rotate(90deg)',
                display: 'inline-block',
              }}>
                ▶
              </span>
              {hasActive && (
                <div style={{
                  width: 6, height: 6, borderRadius: '50%',
                  background: 'var(--green)', flexShrink: 0,
                  animation: 'pulse 2s ease-in-out infinite',
                }} />
              )}
              {hasAttention && !hasActive && (
                <div style={{
                  width: 6, height: 6, borderRadius: '50%',
                  background: 'var(--orange)', flexShrink: 0,
                }} />
              )}
              <span style={{
                fontSize: 13, fontWeight: 600, color: 'var(--fg)',
                letterSpacing: '-0.01em',
              }}>
                {name}
              </span>
              <span style={{ fontSize: 10, color: 'var(--fg-4)' }}>
                {groupAgents.length} session{groupAgents.length !== 1 ? 's' : ''}
              </span>
              <span style={{
                fontSize: 10, fontFamily: 'var(--mono)',
                color: 'var(--green)', marginLeft: 'auto',
              }}>
                ${groupCost.toFixed(2)}
              </span>
            </div>

            {/* Cards */}
            {!isCollapsed && (
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
                      onClick={() => onSelect(agent.SessionID || agent.PID.toString())}
                      onKill={handleKill}
                      searchSnippet={contentMatchMap.get(agent.SessionID)}
                    />
                  </div>
                ))}
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
