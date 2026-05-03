import type { Agent } from '../types';

interface Props {
  agents: Agent[];
  viewMode: 'status' | 'repo';
  onViewModeChange: (mode: 'status' | 'repo') => void;
  onLaunch: () => void;
}

export function StatsBar({ agents, viewMode, onViewModeChange, onLaunch }: Props) {
  const active = agents.filter(a => a.status === 'Active').length;
  const repos = new Set(agents.map(a => a.name)).size;
  const cost = agents.reduce((sum, a) => sum + a.estCostUSD, 0);
  const attention = agents.filter(a => a.status === 'Waiting').length;

  return (
    <header style={{
      display: 'flex', alignItems: 'center', justifyContent: 'space-between',
      padding: '12px 24px', background: 'var(--bg-1)',
      borderBottom: '1px solid var(--border)', flexShrink: 0,
    }}>
      <span style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.02em' }}>
        <span style={{ color: 'var(--accent)' }}>ai</span><span>mux</span>
      </span>

      <div style={{ display: 'flex', gap: 24 }}>
        <Stat value={active} label="Active" />
        <Stat value={repos} label="Repos" />
        <Stat value={`$${cost.toFixed(2)}`} label="Cost Today" />
        <Stat value={attention} label="Need Attention" highlight={attention > 0} />
      </div>

      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <ToggleBtn active={viewMode === 'status'} onClick={() => onViewModeChange('status')}>By Status</ToggleBtn>
        <ToggleBtn active={viewMode === 'repo'} onClick={() => onViewModeChange('repo')}>By Repo</ToggleBtn>
        <button onClick={onLaunch} style={{
          padding: '5px 12px', borderRadius: 4, border: '1px solid var(--accent)',
          background: 'transparent', color: 'var(--accent)', fontSize: 12,
          fontWeight: 600, cursor: 'pointer',
        }}>+ Launch</button>
      </div>
    </header>
  );
}

function Stat({ value, label, highlight }: { value: string | number; label: string; highlight?: boolean }) {
  return (
    <div style={{ textAlign: 'center' }}>
      <div style={{ fontSize: 20, fontWeight: 700, color: highlight ? 'var(--accent)' : 'var(--fg)' }}>{value}</div>
      <div style={{ fontSize: 10, color: 'var(--fg-3)', textTransform: 'uppercase' as const, letterSpacing: '0.06em' }}>{label}</div>
    </div>
  );
}

function ToggleBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button onClick={onClick} style={{
      padding: '5px 10px', borderRadius: 4,
      border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
      background: active ? 'var(--accent)' : 'var(--bg-3)',
      color: active ? '#fff' : 'var(--fg-3)',
      fontSize: 11, cursor: 'pointer', fontWeight: 500,
    }}>{children}</button>
  );
}
