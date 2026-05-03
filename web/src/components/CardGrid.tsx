import type { Agent } from '../types';
import { AgentCard } from './AgentCard';

interface Props {
  agents: Agent[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  statusFilter: number | null;
  providerFilter: string | null;
  searchQuery: string;
  sortBy: string;
}

export function CardGrid({
  agents,
  selectedId,
  onSelect,
  statusFilter,
  providerFilter,
  searchQuery,
  sortBy,
}: Props) {
  // Filter agents
  let filtered = agents;

  if (statusFilter !== null) {
    filtered = filtered.filter(a => a.Status === statusFilter);
  }

  if (providerFilter !== null) {
    filtered = filtered.filter(a => a.ProviderName.toLowerCase() === providerFilter.toLowerCase());
  }

  if (searchQuery) {
    const query = searchQuery.toLowerCase();
    filtered = filtered.filter(a =>
      a.Name.toLowerCase().includes(query) ||
      (a.GitBranch || '').toLowerCase().includes(query) ||
      (a.TaskSubject || '').toLowerCase().includes(query)
    );
  }

  // Sort agents
  const sorted = [...filtered].sort((a, b) => {
    switch (sortBy) {
      case 'lastActive': {
        const aTime = a.LastActivity ? new Date(a.LastActivity).getTime() : 0;
        const bTime = b.LastActivity ? new Date(b.LastActivity).getTime() : 0;
        return bTime - aTime; // desc
      }
      case 'cost':
        return (b.EstCostUSD || 0) - (a.EstCostUSD || 0); // desc
      case 'repo':
        return a.Name.localeCompare(b.Name); // asc
      case 'status':
        return a.Status - b.Status; // asc
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
      <div style={{
        display: 'grid',
        gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))',
        gap: 10,
      }}>
        {sorted.map(agent => (
          <AgentCard
            key={agent.SessionID || agent.PID}
            agent={agent}
            selected={selectedId === (agent.SessionID || agent.PID.toString())}
            onClick={() => onSelect(agent.SessionID || agent.PID.toString())}
          />
        ))}
      </div>
    </div>
  );
}
