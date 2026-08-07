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
import { PluginView } from './components/PluginView';
import TasksPanel from './components/TasksPanel';
import { TaskLaunchDialog } from './components/TaskLaunchDialog';
import './styles/theme.css';

export interface ContentSearchResult {
  sessionId: string;
  snippet: string;
}

type ViewTab = 'agents' | 'sessions' | string;

export default function App() {
  const { agents, loading: agentsLoading } = useAgentStream();
  const [activeTab, setActiveTab] = useState<ViewTab>('agents');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showLaunch, setShowLaunch] = useState(false);
  const [statusFilter, setStatusFilter] = useState<number | null>(null);
  const [providerFilter, setProviderFilter] = useState<string | null>(null);
  const [recentFilter, setRecentFilter] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [sortBy, setSortBy] = useState('lastActive');
  const [viewMode, setViewMode] = useState<'cards' | 'list'>('cards');
  const [panelFullscreen, setPanelFullscreen] = useState(false);
  const [contentResults, setContentResults] = useState<ContentSearchResult[] | null>(null);
  const [isSearching, setIsSearching] = useState(false);
  const [sessionAgent, setSessionAgent] = useState<Agent | null>(null);
  const [sessionCount, setSessionCount] = useState<number | null>(null);
  const [cachedSessions, setCachedSessions] = useState<HistorySession[] | null>(null);
  const [starredCount, setStarredCount] = useState(0);
  const [pluginTabs, setPluginTabs] = useState<{ name: string; tab: string; panels: { id: string; type: 'metric-row' | 'table' | 'bar-chart' | 'list'; title: string; sortable?: boolean; expandable?: boolean; width?: string }[] }[]>([]);
  const [showTasks, setShowTasks] = useState(false);
  const [taskLaunchTarget, setTaskLaunchTarget] = useState<any | null>(null);
  const [pendingTaskCount, setPendingTaskCount] = useState<number>(0);
  const [pendingLaunch, setPendingLaunch] = useState<{ provider: string; dir: string; tmuxSession?: string; existingPIDs: Set<number> } | null>(null);
  const [theme, setTheme] = useState<'dark' | 'light'>(() => {
    const stored = localStorage.getItem('aimux-theme');
    return stored === 'light' ? 'light' : 'dark';
  });

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('aimux-theme', theme);
  }, [theme]);

  useEffect(() => {
    if (cachedSessions !== null) return;
    if (activeTab !== 'sessions' && activeTab !== 'starred') return;
    fetch('/api/history')
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d?.sessions) {
          const s = (d.sessions as HistorySession[]).map(sess => ({ ...sess, note: sess.note || '' }));
          setCachedSessions(s);
          setSessionCount(s.length);
          setStarredCount(s.filter(sess => sess.starred).length);
        }
      })
      .catch(() => {});
  }, [activeTab, cachedSessions]);

  useEffect(() => {
    fetch('/api/plugins')
      .then(r => r.ok ? r.json() : null)
      .then(d => { if (d?.plugins?.length) setPluginTabs(d.plugins); })
      .catch(() => {});
  }, []);

  useEffect(() => {
    fetch('/api/tasks/lists')
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d?.lists?.length) {
          const firstListId = d.lists[0].id;
          fetch(`/api/tasks/lists/${firstListId}/tasks`)
            .then(r => r.ok ? r.json() : null)
            .then(td => {
              if (td?.tasks) {
                const needsAction = td.tasks.filter((t: any) => t.status === 'needsAction').length;
                setPendingTaskCount(needsAction);
              }
            })
            .catch(() => {});
        }
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    if (!pendingLaunch) return;
    const found = agents.find(a =>
      (pendingLaunch.tmuxSession && a.TMuxSession === pendingLaunch.tmuxSession) ||
      (a.ProviderName === pendingLaunch.provider &&
       a.WorkingDir === pendingLaunch.dir &&
       !pendingLaunch.existingPIDs.has(a.PID))
    );
    if (found) {
      setSelectedId(found.SessionID || String(found.PID));
      setPendingLaunch(null);
    }
  }, [agents, pendingLaunch]);

  useEffect(() => {
    if (!pendingLaunch) return;
    const timeout = setTimeout(() => setPendingLaunch(null), 15000);
    return () => clearTimeout(timeout);
  }, [pendingLaunch]);

  const selectedAgent = activeTab === 'agents'
    ? agents.find(a => a.SessionID === selectedId || String(a.PID) === selectedId)
    : undefined;

  // When the selected agent disappears (exited/killed), deselect and close panel
  useEffect(() => {
    if (selectedId && activeTab === 'agents' && agents.length > 0 && !selectedAgent && !pendingLaunch) {
      setSelectedId(null);
      setSessionAgent(null);
      setPanelFullscreen(false);
    }
  }, [selectedId, activeTab, agents, selectedAgent, pendingLaunch]);

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
      CPUPercent: 0,
      MemoryMB: 0,
      TokensIn: session.tokensIn,
      TokensOut: session.tokensOut,
      EstCostUSD: session.costUSD,
      LastActivity: session.lastActive,
      LastAction: '',
      TMuxSession: '',
      TeamName: '',
      TaskSubject: '',
      Title: session.title || session.firstPrompt || '',
      PermissionMode: session.permissionMode || '',
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
        onHome={() => { setActiveTab('agents'); setSelectedId(null); setSessionAgent(null); setPanelFullscreen(false); }}
        onToggleTasks={() => setShowTasks(t => !t)}
        taskCount={pendingTaskCount}
        tasksOpen={showTasks}
        theme={theme}
        onToggleTheme={() => setTheme(t => t === 'dark' ? 'light' : 'dark')}
      />
      {!panelFullscreen && (
        <>
          {/* Tab row */}
          <div style={{
            display: 'flex', padding: '0 18px', gap: 0,
            borderBottom: activeTab !== 'agents' ? '1px solid var(--border)' : 'none',
            flexShrink: 0,
          }}>
            {(['agents', 'sessions', 'starred', ...pluginTabs.map(p => `plugin:${p.name}`)]).map(tab => (
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
                {tab === 'agents' ? `Live Agents (${agents.length})`
                  : tab === 'sessions' ? `Sessions${sessionCount !== null ? ` (${sessionCount})` : ''}`
                  : tab === 'starred' ? `★ Starred${starredCount > 0 ? ` (${starredCount})` : ''}`
                  : pluginTabs.find(p => `plugin:${p.name}` === tab)?.tab || tab}
              </button>
            ))}
          </div>

          {/* Filter bar on its own row for agents tab */}
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
              viewMode={viewMode}
              onViewModeChange={setViewMode}
            />
          )}
        </>
      )}
      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {!panelFullscreen && activeTab === 'agents' && (
          <CardGrid
            agents={agents}
            selectedId={selectedId}
            onSelect={(id) => setSelectedId(prev => prev === id ? null : id)}
            statusFilter={statusFilter}
            providerFilter={providerFilter}
            recentFilter={recentFilter}
            searchQuery={searchQuery}
            sortBy={sortBy}
            contentResults={contentResults}
            loading={agentsLoading}
            viewMode={viewMode}
          />
        )}
        {!panelFullscreen && activeTab === 'sessions' && (
          <SessionsTable
            onSelectSession={handleSessionSelect}
            selectedId={selectedId}
            onSessionCount={setSessionCount}
            initialSessions={cachedSessions}
            onSessionsLoaded={(s) => { setCachedSessions(s); setStarredCount(s.filter(x => x.starred).length); }}
          />
        )}
        {!panelFullscreen && activeTab === 'starred' && (
          <SessionsTable
            onSelectSession={handleSessionSelect}
            selectedId={selectedId}
            starredOnly
            initialSessions={cachedSessions}
            onSessionsLoaded={(s) => { setCachedSessions(s); setStarredCount(s.filter(x => x.starred).length); }}
          />
        )}
        {!panelFullscreen && activeTab.startsWith('plugin:') && (() => {
          const p = pluginTabs.find(pt => `plugin:${pt.name}` === activeTab);
          return p ? <PluginView plugin={p} /> : null;
        })()}
        {panelAgent && (
          <RightPanel
            agent={panelAgent}
            onClose={handleClosePanel}
            isFullscreen={panelFullscreen}
            onToggleFullscreen={() => setPanelFullscreen(f => !f)}
          />
        )}
        <TasksPanel
          open={showTasks}
          onClose={() => setShowTasks(false)}
          onLaunchFromTask={(task: any) => setTaskLaunchTarget(task)}
        />
      </div>
      {pendingLaunch && (
        <div style={{
          position: 'fixed', bottom: 20, left: '50%', transform: 'translateX(-50%)',
          background: 'var(--bg-1)', border: '1px solid var(--accent)',
          borderRadius: 8, padding: '10px 20px', zIndex: 999,
          display: 'flex', alignItems: 'center', gap: 10,
          fontSize: 12, color: 'var(--fg-2)', boxShadow: '0 4px 12px rgba(0,0,0,0.3)',
        }}>
          <span style={{
            display: 'inline-block', width: 8, height: 8, borderRadius: '50%',
            background: 'var(--accent)', animation: 'pulse 1.5s ease-in-out infinite',
          }} />
          Launching {pendingLaunch.provider} in {pendingLaunch.dir.split('/').pop()}...
        </div>
      )}
      <LaunchDialog open={showLaunch} onClose={() => setShowLaunch(false)}
        onLaunched={(provider, dir, tmuxSession) => {
          setActiveTab('agents');
          setPendingLaunch({ provider, dir, tmuxSession, existingPIDs: new Set(agents.map(a => a.PID)) });
        }}
      />
      <TaskLaunchDialog
        open={!!taskLaunchTarget}
        task={taskLaunchTarget}
        onClose={() => setTaskLaunchTarget(null)}
        onLaunched={(provider, dir, tmuxSession) => {
          setActiveTab('agents');
          setPendingLaunch({ provider, dir, tmuxSession, existingPIDs: new Set(agents.map(a => a.PID)) });
        }}
      />
    </div>
  );
}
