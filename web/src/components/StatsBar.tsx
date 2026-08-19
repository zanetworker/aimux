import { useState, useEffect } from 'react';
import type { Agent } from '../types';

interface Props {
  agents: Agent[];
  onLaunch: () => void;
  onHome?: () => void;
  onToggleTasks?: () => void;
  taskCount?: number;
  tasksOpen?: boolean;
  theme?: 'dark' | 'light';
  onToggleTheme?: () => void;
}

export function StatsBar({ agents, onLaunch, onHome, onToggleTasks, taskCount, tasksOpen, theme, onToggleTheme }: Props) {
  const [gatewayStatus, setGatewayStatus] = useState<{ available: boolean; message: string } | null>(null);

  useEffect(() => {
    const check = () => {
      fetch('/api/health/remote')
        .then(r => r.json())
        .then(d => setGatewayStatus({ available: d.available, message: d.message || '' }))
        .catch(() => setGatewayStatus({ available: false, message: 'health check failed' }));
    };
    check();
    const interval = setInterval(check, 30_000);
    return () => clearInterval(interval);
  }, []);

  const sessions = agents.length;
  const active = agents.filter(a => a.Status === 0).length;
  const idle = agents.filter(a => a.Status === 1).length;
  const waiting = agents.filter(a => a.Status === 2).length;
  const errors = agents.filter(a => a.Status === 3).length;
  const repos = new Set(agents.map(a => a.Name.replace(/ #\d+$/, ''))).size;
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
      <span
        onClick={onHome}
        style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.02em', cursor: 'pointer' }}
        title="Home"
      >
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

      <div style={{ display: 'flex', gap: 8 }}>
        {onToggleTasks && (
          <button
            onClick={onToggleTasks}
            style={{
              padding: '5px 14px', borderRadius: 4,
              border: tasksOpen ? '1px solid var(--accent)' : '1px solid var(--border)',
              background: tasksOpen ? 'var(--accent-dim)' : 'transparent',
              color: tasksOpen ? 'var(--accent)' : 'var(--fg-3)',
              fontSize: 12, fontWeight: 600, cursor: 'pointer',
            }}
          >
            Tasks{taskCount ? ` (${taskCount})` : ''}
          </button>
        )}
        {onToggleTheme && (
          <button
            onClick={onToggleTheme}
            aria-label="Toggle theme"
            title={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
            style={{
              padding: '5px 10px', borderRadius: 4,
              border: '1px solid var(--border)',
              background: 'transparent',
              color: 'var(--fg-3)',
              fontSize: 14, cursor: 'pointer',
            }}
          >
            {theme === 'dark' ? '\u2600' : '\u263D'}
          </button>
        )}
        {gatewayStatus && (
          <div
            title={gatewayStatus.available ? 'OpenShell gateway connected' : (gatewayStatus.message || 'OpenShell gateway unreachable')}
            style={{
              display: 'flex', alignItems: 'center', gap: 5,
              padding: '4px 10px', borderRadius: 4, fontSize: 11, fontWeight: 500,
              border: `1px solid ${gatewayStatus.available ? 'var(--teal)' : 'var(--accent)'}`,
              color: gatewayStatus.available ? 'var(--teal)' : 'var(--accent)',
              background: gatewayStatus.available ? 'rgba(55,163,163,0.08)' : 'var(--accent-dim)',
              cursor: gatewayStatus.available ? 'default' : 'help',
            }}
          >
            <span style={{
              width: 6, height: 6, borderRadius: '50%', flexShrink: 0,
              background: gatewayStatus.available ? 'var(--teal)' : 'var(--accent)',
            }} />
            {gatewayStatus.available ? 'Gateway' : 'Gateway down'}
          </div>
        )}
        <button
          onClick={onLaunch}
          aria-label="Launch new agent session"
          style={{
            padding: '5px 14px', borderRadius: 4, border: '1px solid var(--accent)',
            background: 'transparent', color: 'var(--accent)', fontSize: 12,
            fontWeight: 600, cursor: 'pointer', letterSpacing: '0.02em',
          }}
        >
          + Launch
        </button>
      </div>
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
      <span style={{ fontSize: 11, color: 'var(--fg-4)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
        {suffix ? label + suffix : label}
      </span>
    </div>
  );
}

function Sep() {
  return <div style={{ width: 1, height: 18, background: 'var(--border)', alignSelf: 'center' }} />;
}
