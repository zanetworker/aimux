import { useState } from 'react';
import { useAgentStream } from './hooks/useAgentStream';
import { StatsBar } from './components/StatsBar';
import { KanbanBoard } from './components/KanbanBoard';
import './styles/theme.css';

export default function App() {
  const agents = useAgentStream();
  const [viewMode, setViewMode] = useState<'status' | 'repo'>('status');
  const [selectedId, setSelectedId] = useState<string | null>(null);

  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <StatsBar
        agents={agents}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        onLaunch={() => {/* Task 10 */}}
      />
      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        <KanbanBoard
          agents={agents}
          viewMode={viewMode}
          selectedId={selectedId}
          onSelect={setSelectedId}
        />
      </div>
    </div>
  );
}
