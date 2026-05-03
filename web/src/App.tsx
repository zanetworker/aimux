import { useState } from 'react';
import { useAgentStream } from './hooks/useAgentStream';
import { StatsBar } from './components/StatsBar';
import './styles/theme.css';

export default function App() {
  const agents = useAgentStream();
  const [viewMode, setViewMode] = useState<'status' | 'repo'>('status');

  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <StatsBar
        agents={agents}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        onLaunch={() => {/* Task 10 */}}
      />
      <main style={{ flex: 1, display: 'flex', padding: 14 }}>
        <p style={{ color: 'var(--fg-3)', margin: 'auto' }}>
          {agents.length} agent(s) discovered. Board coming in Task 7.
        </p>
      </main>
    </div>
  );
}
