import { useState, useCallback } from 'react';
import { useAgentStream } from './hooks/useAgentStream';
import { StatsBar } from './components/StatsBar';
import { CardGrid } from './components/CardGrid';
import { FilterBar } from './components/FilterBar';
import { RightPanel } from './components/RightPanel';
import { LaunchDialog } from './components/LaunchDialog';
import './styles/theme.css';

export interface ContentSearchResult {
  sessionId: string;
  snippet: string;
}

export default function App() {
  const agents = useAgentStream();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showLaunch, setShowLaunch] = useState(false);
  const [statusFilter, setStatusFilter] = useState<number | null>(null);
  const [providerFilter, setProviderFilter] = useState<string | null>(null);
  const [recentFilter, setRecentFilter] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [sortBy, setSortBy] = useState('lastActive');
  const [panelFullscreen, setPanelFullscreen] = useState(false);
  const [contentResults, setContentResults] = useState<ContentSearchResult[] | null>(null);
  const [isSearching, setIsSearching] = useState(false);

  const selectedAgent = agents.find(
    a => a.SessionID === selectedId || String(a.PID) === selectedId
  );

  const handleContentSearch = useCallback(async (query: string) => {
    if (!query.trim()) {
      setContentResults(null);
      return;
    }
    setIsSearching(true);
    try {
      const resp = await fetch(`/api/search?q=${encodeURIComponent(query)}`);
      if (!resp.ok) return;
      const data = await resp.json();
      setContentResults(data.results || []);
    } catch {
      setContentResults(null);
    } finally {
      setIsSearching(false);
    }
  }, []);

  const clearContentSearch = useCallback(() => {
    setContentResults(null);
  }, []);

  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <StatsBar
        agents={agents}
        onLaunch={() => setShowLaunch(true)}
      />
      {!panelFullscreen && (
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
          onContentSearch={handleContentSearch}
          onClearContentSearch={clearContentSearch}
          contentResults={contentResults}
          isSearching={isSearching}
        />
      )}
      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {!panelFullscreen && (
          <CardGrid
            agents={agents}
            selectedId={selectedId}
            onSelect={setSelectedId}
            statusFilter={statusFilter}
            providerFilter={providerFilter}
            recentFilter={recentFilter}
            searchQuery={searchQuery}
            sortBy={sortBy}
            contentResults={contentResults}
          />
        )}
        {selectedAgent && (
          <RightPanel
            agent={selectedAgent}
            onClose={() => { setSelectedId(null); setPanelFullscreen(false); }}
            isFullscreen={panelFullscreen}
            onToggleFullscreen={() => setPanelFullscreen(f => !f)}
          />
        )}
      </div>
      <LaunchDialog open={showLaunch} onClose={() => setShowLaunch(false)} />
    </div>
  );
}
