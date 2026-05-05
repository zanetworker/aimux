import { useState, useCallback, useEffect } from 'react';
import { useAgentStream } from './hooks/useAgentStream';
import type { Agent } from './types';
import { StatsBar } from './components/StatsBar';
import { CardGrid } from './components/CardGrid';
import { FilterBar } from './components/FilterBar';
import { RightPanel } from './components/RightPanel';
import { SessionsTable } from './components/SessionsTable';
import type { HistorySession } from './components/SessionsTable';
import { LaunchDialog } from './components/LaunchDialog';
import './styles/theme.css';

export interface ContentSearchResult {
  sessionId: string;
  snippet: string;
}

type ViewTab = 'agents' | 'sessions';

export default function App() {
  const agents = useAgentStream();
  const [activeTab, setActiveTab] = useState<ViewTab>('agents');
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
  const [sessionAgent, setSessionAgent] = useState<Agent | null>(null);
  const [sessionCount, setSessionCount] = useState<number | null>(null);

  useEffect(() => {
    fetch('/api/history')
      .then(r => r.ok ? r.json() : null)
      .then(d => { if (d?.sessions) setSessionCount(d.sessions.length); })
      .catch(() => {});
  }, []);

  const selectedAgent = activeTab === 'agents'
    ? agents.find(a => a.SessionID === selectedId || String(a.PID) === selectedId)
    : undefined;

  const panelAgent = selectedAgent || sessionAgent;

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

  const handleSessionSelect = useCallback((session: HistorySession) => {
    setSelectedId(session.id);
    const proj = session.project || '';
    const parts = proj.replace(/\/+$/, '').split('/');
    const name = parts[parts.length - 1] || proj;
    setSessionAgent({
      PID: 0,
      SessionID: session.id,
      Name: name,
      ProviderName: session.provider || 'claude',
      SessionFile: session.filePath,
      Model: '',
      WorkingDir: session.project,
      Status: 1,
      GitBranch: '',
      TokensIn: session.tokensIn,
      TokensOut: session.tokensOut,
      EstCostUSD: session.costUSD,
      LastActivity: session.lastActive,
      LastAction: '',
      TMuxSession: '',
      TeamName: '',
      TaskSubject: '',
      Title: session.title || session.firstPrompt || '',
    });
  }, []);

  const handleClosePanel = useCallback(() => {
    setSelectedId(null);
    setSessionAgent(null);
    setPanelFullscreen(false);
  }, []);

  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <StatsBar
        agents={agents}
        onLaunch={() => setShowLaunch(true)}
      />
      {!panelFullscreen && (
        <div style={{
          display: 'flex', alignItems: 'center',
          borderBottom: '1px solid var(--border)', flexShrink: 0,
        }}>
          {/* Tab switcher */}
          <div style={{ display: 'flex', padding: '0 18px', gap: 0 }}>
            {(['agents', 'sessions'] as ViewTab[]).map(tab => (
              <button
                key={tab}
                onClick={() => { setActiveTab(tab); setSelectedId(null); setSessionAgent(null); }}
                style={{
                  padding: '8px 16px',
                  background: 'transparent',
                  border: 'none',
                  borderBottom: activeTab === tab ? '2px solid var(--accent)' : '2px solid transparent',
                  color: activeTab === tab ? 'var(--fg)' : 'var(--fg-3)',
                  fontSize: 11,
                  fontWeight: activeTab === tab ? 600 : 400,
                  textTransform: 'uppercase',
                  letterSpacing: '0.04em',
                  cursor: 'pointer',
                  transition: 'all 0.15s',
                }}
              >
                {tab === 'agents' ? `Agents (${agents.length})` : `Sessions${sessionCount !== null ? ` (${sessionCount})` : ''}`}
              </button>
            ))}
          </div>

          {/* Show filter bar only for agents tab */}
          {activeTab === 'agents' && (
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
        </div>
      )}
      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {!panelFullscreen && activeTab === 'agents' && (
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
        {!panelFullscreen && activeTab === 'sessions' && (
          <SessionsTable
            onSelectSession={handleSessionSelect}
            selectedId={selectedId}
            onSessionCount={setSessionCount}
          />
        )}
        {panelAgent && (
          <RightPanel
            agent={panelAgent}
            onClose={handleClosePanel}
            isFullscreen={panelFullscreen}
            onToggleFullscreen={() => setPanelFullscreen(f => !f)}
          />
        )}
      </div>
      <LaunchDialog open={showLaunch} onClose={() => setShowLaunch(false)} />
    </div>
  );
}
