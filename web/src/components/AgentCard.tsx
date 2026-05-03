import type { Agent } from '../types';

interface Props {
  agent: Agent;
  selected: boolean;
  onClick: () => void;
}

export function AgentCard({ agent, selected, onClick }: Props) {
  const providerColors = {
    claude: {
      background: 'var(--accent-dim)',
      color: 'var(--accent)',
      border: '1px solid var(--accent)',
    },
    codex: {
      background: 'var(--green-dim)',
      color: 'var(--green)',
      border: '1px solid rgba(105,223,115,0.3)',
    },
    gemini: {
      background: 'var(--purple-dim)',
      color: 'var(--purple)',
      border: '1px solid rgba(167,114,239,0.3)',
    },
  };

  const providerStyle = providerColors[agent.providerName.toLowerCase() as keyof typeof providerColors] || providerColors.claude;

  const timeSinceActivity = () => {
    if (!agent.lastActivity) return 'unknown';
    const now = new Date();
    const last = new Date(agent.lastActivity);
    const diff = Math.floor((now.getTime() - last.getTime()) / 1000);
    if (diff < 60) return `${diff}s ago`;
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  };

  const borderColor = agent.status === 'Waiting'
    ? 'var(--orange)'
    : selected
      ? 'var(--accent)'
      : 'var(--border)';

  const boxShadow = selected ? '0 0 8px var(--accent-dim)' : 'none';
  const hoverBg = 'var(--bg-3)';
  const hoverBorder = 'var(--border-hover)';

  return (
    <div
      onClick={onClick}
      style={{
        background: 'var(--bg-2)',
        border: `1px solid ${borderColor}`,
        borderRadius: 6,
        padding: '10px 12px',
        cursor: 'pointer',
        boxShadow,
        transition: 'all 0.15s ease',
      }}
      onMouseEnter={(e) => {
        if (!selected && agent.status !== 'Waiting') {
          e.currentTarget.style.borderColor = hoverBorder;
          e.currentTarget.style.background = hoverBg;
        }
      }}
      onMouseLeave={(e) => {
        if (!selected && agent.status !== 'Waiting') {
          e.currentTarget.style.borderColor = 'var(--border)';
          e.currentTarget.style.background = 'var(--bg-2)';
        }
      }}
    >
      {/* Top row: provider badge + time */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
        <span style={{
          padding: '2px 6px',
          borderRadius: 3,
          fontSize: 10,
          fontWeight: 600,
          textTransform: 'uppercase' as const,
          letterSpacing: '0.05em',
          ...providerStyle,
        }}>
          {agent.providerName}
        </span>
        <span style={{ fontSize: 10, color: 'var(--fg-3)' }}>
          {timeSinceActivity()}
        </span>
      </div>

      {/* Repo name */}
      <div style={{ fontSize: 13, fontWeight: 700, marginBottom: 4, color: 'var(--fg)' }}>
        {agent.name}
      </div>

      {/* Git branch */}
      <div style={{ marginBottom: 6 }}>
        <span style={{
          fontFamily: 'var(--mono)',
          fontSize: 11,
          padding: '2px 4px',
          borderRadius: 3,
          background: 'var(--bg-4)',
          color: 'var(--accent)',
        }}>
          {agent.gitBranch || 'main'}
        </span>
      </div>

      {/* Last action */}
      <div style={{ fontSize: 11, fontStyle: 'italic', color: 'var(--fg-3)', marginBottom: 8 }}>
        {agent.lastAction || 'No activity'}
      </div>

      {/* Bottom row: model + cost */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontSize: 11, color: 'var(--fg-2)' }}>
          {agent.model}
        </span>
        <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--green)' }}>
          ${agent.estCostUSD.toFixed(3)}
        </span>
      </div>
    </div>
  );
}
