import type { Agent } from '../types';
import { StatusLabel } from '../types';

interface Props {
  agent: Agent;
  selected: boolean;
  onClick: () => void;
  onKill?: (id: string) => void;
  searchSnippet?: string;
}

export function AgentCard({ agent, selected, onClick, onKill, searchSnippet }: Props) {
  const providerColors = {
    claude: {
      background: 'var(--accent-dim)',
      color: 'var(--accent)',
    },
    codex: {
      background: 'rgba(74,222,128,0.15)',
      color: '#4ade80',
    },
    gemini: {
      background: 'rgba(167,139,250,0.15)',
      color: '#a78bfa',
    },
  };

  const providerStyle = providerColors[agent.ProviderName.toLowerCase() as keyof typeof providerColors] || providerColors.claude;

  const statusColors = {
    0: { dot: 'var(--green)', bg: 'var(--green-dim)', color: 'var(--green)' },
    1: { dot: 'var(--fg-3)', bg: 'var(--bg-2)', color: 'var(--fg-3)' },
    2: { dot: 'var(--orange)', bg: 'var(--orange-dim)', color: 'var(--orange)' },
    3: { dot: 'var(--accent)', bg: 'var(--accent-dim)', color: 'var(--accent)' },
    4: { dot: 'var(--fg-3)', bg: 'var(--bg-2)', color: 'var(--fg-3)' },
  };

  const statusStyle = statusColors[agent.Status as keyof typeof statusColors] || statusColors[4];

  const actionIcons = {
    0: '▶',  // Active - play
    1: '■',  // Idle - square
    2: '⏸',  // Waiting - pause
    3: '✕',  // Error - X
    4: '■',  // Unknown - square
  };

  const shortenPath = (path: string): string => {
    return path
      .replace(/\/Users\/[^/]+\/go\/src\/github\.com\/[^/]+\//g, '')
      .replace(/\/Users\/[^/]+\//g, '~/');
  };

  const formatTokenCount = (tokensIn: number, tokensOut: number): string => {
    const formatK = (n: number) => {
      if (n < 1000) return String(n);
      return (n / 1000).toFixed(1) + 'k';
    };
    return `${formatK(tokensIn)} in / ${formatK(tokensOut)} out`;
  };

  const timeSinceActivity = () => {
    if (!agent.LastActivity) return 'unknown';
    const now = new Date();
    const last = new Date(agent.LastActivity);
    const diff = Math.floor((now.getTime() - last.getTime()) / 1000);
    if (diff < 60) return `${diff}s ago`;
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  };

  const borderLeftColor = agent.Status === 0 ? 'var(--green)' : 'var(--fg-4)';
  const borderLeftColorOverride = agent.Status === 2 ? 'var(--orange)' : agent.Status === 3 ? 'var(--accent)' : borderLeftColor;

  const borderColor = selected ? 'var(--accent)' : 'var(--border)';

  const cardBg = agent.Status === 2
    ? 'var(--orange-dim)'
    : agent.Status === 3
      ? 'var(--accent-dim)'
      : 'var(--bg-0)';

  const showAttention = agent.Status === 2 || agent.Status === 3;

  return (
    <div
      onClick={onClick}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onClick(); } }}
      role="button"
      tabIndex={0}
      aria-label={`${agent.ProviderName} session: ${agent.Name}, branch ${agent.GitBranch || 'main'}, status ${StatusLabel[agent.Status]}, cost $${(agent.EstCostUSD || 0).toFixed(2)}`}
      className="agent-card"
      style={{
        position: 'relative',
        background: cardBg,
        border: `1px solid ${borderColor}`,
        borderLeft: `3px solid ${borderLeftColorOverride}`,
        borderRadius: 8,
        padding: '14px 16px',
        cursor: 'pointer',
        transition: 'border-color 0.15s ease, outline 0.1s ease',
        outline: 'none',
      }}
      onFocus={(e) => { e.currentTarget.style.outline = '2px solid var(--accent)'; e.currentTarget.style.outlineOffset = '2px'; }}
      onBlur={(e) => { e.currentTarget.style.outline = 'none'; }}
      onMouseEnter={(e) => {
        if (!selected) {
          e.currentTarget.style.borderColor = 'var(--border-hover)';
        }
      }}
      onMouseLeave={(e) => {
        if (!selected) {
          e.currentTarget.style.borderColor = 'var(--border)';
        }
      }}
    >
      {/* Attention bell */}
      {showAttention && (
        <div style={{
          position: 'absolute',
          top: 8,
          right: 8,
          fontSize: 14,
          animation: 'ring 2s ease-in-out infinite',
        }}>
          🔔
        </div>
      )}

      {/* Kill button */}
      <button
        onClick={(e) => {
          e.stopPropagation();
          if (onKill) {
            if (confirm('Kill this session?')) {
              onKill(agent.SessionID || String(agent.PID));
            }
          }
        }}
        className="kill-btn"
        style={{
          position: 'absolute',
          top: 8,
          right: showAttention ? 28 : 8,
          background: 'transparent',
          border: 'none',
          color: 'var(--fg-4)',
          fontSize: 12,
          cursor: 'pointer',
          opacity: 0,
          transition: 'opacity 0.15s',
          padding: '2px 4px',
        }}
        title="Kill session"
      >
        ✕
      </button>

      {/* Header row */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <div style={{
          width: 7,
          height: 7,
          borderRadius: '50%',
          background: statusStyle.dot,
          animation: agent.Status === 0 ? 'pulse 2s ease-in-out infinite' : 'none',
        }} />
        <span style={{
          padding: '2px 6px',
          borderRadius: 3,
          fontSize: 10,
          fontWeight: 600,
          textTransform: 'uppercase' as const,
          letterSpacing: '0.05em',
          ...providerStyle,
        }}>
          {agent.ProviderName}
        </span>
        <span style={{
          padding: '1px 5px',
          borderRadius: 2,
          fontSize: 8,
          fontWeight: 700,
          textTransform: 'uppercase' as const,
          letterSpacing: '0.06em',
          background: statusStyle.bg,
          color: statusStyle.color,
        }}>
          {StatusLabel[agent.Status]}
        </span>
        <span style={{ fontSize: 10, color: 'var(--fg-3)', marginLeft: 'auto' }}>
          {timeSinceActivity()}
        </span>
      </div>

      {/* Repo name */}
      <div style={{ fontSize: 14, fontWeight: 700, marginBottom: 6, color: 'var(--fg)' }}>
        {agent.Name}
      </div>

      {/* Branch + Dir */}
      <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
        <span style={{
          fontFamily: 'var(--mono)',
          fontSize: 10,
          padding: '2px 6px',
          borderRadius: 3,
          background: 'var(--bg-4)',
          color: 'var(--accent)',
          display: 'inline-block',
        }}>
          {agent.GitBranch || 'main'}
        </span>
        {agent.WorkingDir && (
          <span style={{
            fontFamily: 'var(--mono)',
            fontSize: 9,
            color: 'var(--fg-4)',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            maxWidth: 200,
          }}
          title={agent.WorkingDir}
          >
            {shortenPath(agent.WorkingDir)}
          </span>
        )}
      </div>

      {/* Description: Title if available, else LastAction */}
      {(agent.Title || (!agent.Title && agent.LastAction)) && (
        <div style={{
          fontSize: 12,
          color: 'var(--fg-2)',
          marginBottom: 8,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          display: '-webkit-box',
          WebkitLineClamp: 2,
          WebkitBoxOrient: 'vertical' as const,
          lineHeight: '1.4',
        }}>
          {agent.Title || shortenPath(agent.LastAction || '')}
        </div>
      )}

      {/* Last action - only show if Title exists */}
      {agent.Title && agent.LastAction && (
        <div style={{
          fontFamily: 'var(--mono)',
          fontSize: 10,
          padding: '6px 8px',
          borderRadius: 4,
          background: 'var(--bg-0)',
          border: '1px solid var(--border)',
          marginBottom: 10,
          whiteSpace: 'nowrap',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          display: 'flex',
          alignItems: 'center',
          gap: 6,
        }}>
          <span style={{
            color: statusStyle.color,
            fontSize: 9,
          }}>
            {actionIcons[agent.Status as keyof typeof actionIcons]}
          </span>
          <span style={{ color: 'var(--fg-2)' }}>
            {shortenPath(agent.LastAction)}
          </span>
        </div>
      )}

      {/* Deep search snippet */}
      {searchSnippet && (
        <div style={{
          fontSize: 10,
          fontFamily: 'var(--mono)',
          color: 'var(--purple)',
          fontStyle: 'italic',
          padding: '4px 8px',
          background: 'var(--purple-dim)',
          borderRadius: 3,
          marginBottom: 8,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}>
          {searchSnippet}
        </div>
      )}

      {/* Bottom row */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span style={{ fontFamily: 'var(--mono)', fontSize: 9, color: 'var(--fg-3)' }}>
            {agent.Model}
          </span>
          <span style={{ fontFamily: 'var(--mono)', fontSize: 9, color: 'var(--fg-3)' }}>
            {formatTokenCount(agent.TokensIn, agent.TokensOut)}
          </span>
        </div>
        <span style={{ fontSize: 11, fontWeight: 700, color: '#4ade80' }}>
          ${(agent.EstCostUSD || 0).toFixed(2)}
        </span>
      </div>

      <style>{`
        @keyframes ring {
          0%, 100% { transform: rotate(0); }
          10% { transform: rotate(12deg); }
          20% { transform: rotate(-12deg); }
          30% { transform: rotate(8deg); }
          40% { transform: rotate(-8deg); }
          50% { transform: rotate(0); }
        }
        @keyframes pulse {
          0%, 100% { opacity: 1; }
          50% { opacity: 0.5; }
        }
        .agent-card:hover .kill-btn {
          opacity: 1 !important;
        }
        .kill-btn:hover {
          color: var(--accent) !important;
        }
      `}</style>
    </div>
  );
}
