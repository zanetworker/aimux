import type { Agent } from '../types';

interface Props {
  agents: Agent[];
  onLaunch: () => void;
}

export function StatsBar({ agents, onLaunch }: Props) {
  const sessions = agents.length;
  const active = agents.filter(a => a.Status === 0).length;
  const repos = new Set(agents.map(a => a.Name)).size;
  const cost = agents.reduce((sum, a) => sum + (a.EstCostUSD || 0), 0);
  const attention = agents.filter(a => a.Status === 2 || a.Status === 3).length;

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
        <Stat value={sessions} label="Sessions" />
        <Stat value={active} label="Active" />
        <Stat value={repos} label="Repos" />
        <Stat value={`$${cost.toFixed(2)}`} label="Cost Today" />
        <Stat value={attention} label="Attention" highlight={attention > 0} />
      </div>

      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
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
