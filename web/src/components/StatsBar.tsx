import type { Agent } from '../types';

interface Props {
  agents: Agent[];
  onLaunch: () => void;
}

export function StatsBar({ agents, onLaunch }: Props) {
  const sessions = agents.length;
  const active = agents.filter(a => a.Status === 0).length;
  const idle = agents.filter(a => a.Status === 1).length;
  const waiting = agents.filter(a => a.Status === 2).length;
  const errors = agents.filter(a => a.Status === 3).length;
  const repos = new Set(agents.map(a => a.Name)).size;
  const totalCost = agents.reduce((sum, a) => sum + (a.EstCostUSD || 0), 0);
  const totalTokensIn = agents.reduce((sum, a) => sum + (a.TokensIn || 0), 0);
  const totalTokensOut = agents.reduce((sum, a) => sum + (a.TokensOut || 0), 0);

  const formatTokens = (n: number) => {
    if (n < 1000) return String(n);
    if (n < 1_000_000) return (n / 1000).toFixed(1) + 'k';
    return (n / 1_000_000).toFixed(1) + 'M';
  };

  return (
    <header
      role="banner"
      style={{
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: '10px 24px', background: 'var(--bg-0)',
        borderBottom: '1px solid var(--border)', flexShrink: 0,
      }}
    >
      <span style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.02em' }}>
        <span style={{ color: 'var(--accent)' }}>ai</span><span style={{ color: 'var(--fg)' }}>mux</span>
      </span>

      <div role="status" aria-label="Dashboard statistics" style={{ display: 'flex', gap: 6 }}>
        <StatChip value={sessions} label="sessions" color="var(--fg)" />
        <Sep />
        <StatChip value={active} label="active" color="var(--green)" />
        <StatChip value={idle} label="idle" color="var(--fg-3)" />
        <StatChip value={waiting} label="waiting" color={waiting > 0 ? 'var(--orange)' : 'var(--fg-4)'} />
        <StatChip value={errors} label="errors" color={errors > 0 ? 'var(--accent)' : 'var(--fg-4)'} />
        <Sep />
        <StatChip value={repos} label="repos" color="var(--fg-2)" />
        <Sep />
        <StatChip value={formatTokens(totalTokensIn)} label="in" color="var(--teal)" suffix=" tok" />
        <StatChip value={formatTokens(totalTokensOut)} label="out" color="var(--teal)" suffix=" tok" />
        <Sep />
        <StatChip value={`$${totalCost.toFixed(2)}`} label="total cost" color="var(--green)" />
      </div>

      <button
        onClick={onLaunch}
        aria-label="Launch new agent session"
        style={{
          padding: '5px 14px', borderRadius: 4, border: '1px solid var(--accent)',
          background: 'transparent', color: 'var(--accent)', fontSize: 11,
          fontWeight: 600, cursor: 'pointer', letterSpacing: '0.02em',
        }}
      >
        + Launch
      </button>
    </header>
  );
}

function StatChip({ value, label, color, suffix }: {
  value: string | number;
  label: string;
  color: string;
  suffix?: string;
}) {
  return (
    <div style={{
      display: 'flex', alignItems: 'baseline', gap: 3,
      padding: '2px 8px', borderRadius: 4,
      background: 'var(--bg-1)',
    }}>
      <span style={{ fontSize: 14, fontWeight: 700, fontFamily: 'var(--mono)', color }}>
        {value}
      </span>
      <span style={{ fontSize: 9, color: 'var(--fg-4)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
        {suffix ? label + suffix : label}
      </span>
    </div>
  );
}

function Sep() {
  return <div style={{ width: 1, height: 18, background: 'var(--border)', alignSelf: 'center' }} />;
}
