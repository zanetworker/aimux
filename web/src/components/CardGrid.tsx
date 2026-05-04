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
  const handleKill = async (id: string) => {
    try {
      await fetch(`/api/agents/${id}/archive`, { method: 'POST' });
    } catch {
      // ignore
    }
  };

  // Build content match lookup: sessionId -> snippet
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

  // When deep search is active, only show matching sessions
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

  return (
    <div style={{
      flex: 1,
      overflowY: 'auto',
      padding: '14px 18px',
    }}>
      <div
        role="list"
        aria-label="Agent sessions"
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))',
          gridAutoRows: '1fr',
          gap: 10,
        }}
      >
        {sorted.map(agent => (
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
      {sorted.length === 0 && (
        <div style={{
          textAlign: 'center',
          padding: 40,
          color: 'var(--fg-3)',
          fontSize: 13,
        }}>
          {contentResults ? 'No sessions match your search.' : 'No sessions found.'}
        </div>
      )}
    </div>
  );
}
