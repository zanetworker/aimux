import { useState } from 'react';
import { useAgentStream } from './hooks/useAgentStream';
import { StatsBar } from './components/StatsBar';
import { KanbanBoard } from './components/KanbanBoard';
import { RightPanel } from './components/RightPanel';
import { LaunchDialog } from './components/LaunchDialog';
import './styles/theme.css';

export default function App() {
  const agents = useAgentStream();
  const [viewMode, setViewMode] = useState<'status' | 'repo'>('status');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showLaunch, setShowLaunch] = useState(false);

  const selectedAgent = agents.find(
    a => a.sessionId === selectedId || String(a.pid) === selectedId
  );

  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <StatsBar
        agents={agents}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        onLaunch={() => setShowLaunch(true)}
      />
      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        <KanbanBoard
          agents={agents}
          viewMode={viewMode}
          selectedId={selectedId}
          onSelect={setSelectedId}
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
