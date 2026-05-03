import { useState } from 'react';
import { useAgentStream } from './hooks/useAgentStream';
import { StatsBar } from './components/StatsBar';
import { CardGrid } from './components/CardGrid';
import { FilterBar } from './components/FilterBar';
import { RightPanel } from './components/RightPanel';
import { LaunchDialog } from './components/LaunchDialog';
import './styles/theme.css';

export default function App() {
  const agents = useAgentStream();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showLaunch, setShowLaunch] = useState(false);
  const [statusFilter, setStatusFilter] = useState<number | null>(null);
  const [providerFilter, setProviderFilter] = useState<string | null>(null);
  const [recentFilter, setRecentFilter] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [sortBy, setSortBy] = useState('lastActive');

  const selectedAgent = agents.find(
    a => a.SessionID === selectedId || String(a.PID) === selectedId
  );

  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <StatsBar
        agents={agents}
        onLaunch={() => setShowLaunch(true)}
      />
      <FilterBar
        agents={agents}
        statusFilter={statusFilter}
        onStatusFilter={setStatusFilter}
        providerFilter={providerFilter}
        onProviderFilter={setProviderFilter}
        recentFilter={recentFilter}
        onRecentFilter={setRecentFilter}
        searchQuery={searchQuery}
        onSearchChange={setSearchQuery}
        sortBy={sortBy}
        onSortChange={setSortBy}
      />
      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        <CardGrid
          agents={agents}
          selectedId={selectedId}
          onSelect={setSelectedId}
          statusFilter={statusFilter}
          providerFilter={providerFilter}
          recentFilter={recentFilter}
          searchQuery={searchQuery}
          sortBy={sortBy}
        />
        {selectedAgent && (
          <RightPanel
            agent={selectedAgent}
            onClose={() => setSelectedId(null)}
          />
        )}
      </div>
      <LaunchDialog open={showLaunch} onClose={() => setShowLaunch(false)} />
    </div>
  );
}
