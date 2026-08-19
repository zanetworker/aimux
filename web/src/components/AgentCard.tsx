import type { Agent } from '../types';
import { StatusLabel } from '../types';

interface Props {
  agent: Agent;
  selected: boolean;
  starred?: boolean;
  onClick: () => void;
  onKill?: (id: string) => void;
  onToggleStar?: (sessionFile: string) => void;
  searchSnippet?: string;
}

export function AgentCard({ agent, selected, starred, onClick, onKill, onToggleStar, searchSnippet }: Props) {
  const providerColors: Record<string, { background: string; color: string }> = {
    claude: { background: 'var(--accent-dim)', color: 'var(--accent)' },
    codex: { background: 'rgba(74,222,128,0.15)', color: '#4ade80' },
  };

  const providerStyle = providerColors[agent.ProviderName.toLowerCase()] || providerColors.claude;

  const statusColors: Record<number, { dot: string; bg: string; color: string }> = {
    0: { dot: 'var(--green)', bg: 'var(--green-dim)', color: 'var(--green)' },
    1: { dot: 'var(--fg-3)', bg: 'var(--bg-2)', color: 'var(--fg-3)' },
    2: { dot: 'var(--orange)', bg: 'var(--orange-dim)', color: 'var(--orange)' },
    3: { dot: 'var(--accent)', bg: 'var(--accent-dim)', color: 'var(--accent)' },
    4: { dot: 'var(--fg-3)', bg: 'var(--bg-2)', color: 'var(--fg-3)' },
  };

  const statusStyle = statusColors[agent.Status] || statusColors[4];

  const shortenPath = (path: string): string => {
    return path
      .replace(/\/Users\/[^/]+\/go\/src\/github\.com\/[^/]+\//g, '')
      .replace(/\/Users\/[^/]+\//g, '~/');
  };

  const formatK = (n: number) => {
    if (n < 1000) return String(n);
    return (n / 1000).toFixed(1) + 'k';
  };

  const timeSinceActivity = () => {
    if (!agent.LastActivity) return '';
    const diff = Math.floor((Date.now() - new Date(agent.LastActivity).getTime()) / 1000);
    if (diff < 60) return `${diff}s ago`;
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  };

  const isRemote = agent.Location === 'remote';
  const isK8s = agent.Location === 'k8s';

  const borderLeftColor = agent.Status === 3 ? 'var(--accent)' :
    agent.Status === 2 ? 'var(--orange)' :
    isRemote ? 'var(--teal)' :
    isK8s ? 'var(--purple)' :
    agent.Status === 0 ? 'var(--green)' : 'var(--fg-4)';

  const cardBg = agent.Status === 2 ? 'var(--orange-dim)' :
    agent.Status === 3 ? 'var(--accent-dim)' :
    isRemote ? 'rgba(55,163,163,0.04)' :
    isK8s ? 'rgba(167,114,239,0.04)' : 'var(--bg-0)';

  const title = agent.Title || '';

  return (
    <div
      onClick={onClick}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onClick(); } }}
      role="button"
      tabIndex={0}
      aria-label={`${title || agent.Name}: ${StatusLabel[agent.Status]}, $${(agent.EstCostUSD || 0).toFixed(2)}`}
      className="agent-card"
      data-selected={selected || undefined}
      style={{
        position: 'relative',
        background: cardBg,
        border: `1px solid ${selected ? 'var(--accent)' : 'var(--border)'}`,
        borderLeft: `3px solid ${selected ? 'var(--accent)' : borderLeftColor}`,
        borderRadius: 8,
        padding: '14px 16px',
        cursor: 'pointer',
        transition: 'border-color 0.15s ease',
        outline: 'none',
        display: 'flex',
        flexDirection: 'column',
        width: '100%',
      }}
      onFocus={(e) => { e.currentTarget.style.outline = '2px solid var(--accent)'; e.currentTarget.style.outlineOffset = '2px'; }}
      onBlur={(e) => { e.currentTarget.style.outline = 'none'; }}
    >
      {/* Row 1: status dot + provider + status badge + time */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8 }}>
        <div style={{
          width: 8, height: 8, borderRadius: '50%',
          background: statusStyle.dot, flexShrink: 0,
          animation: agent.Status === 0 ? 'pulse 2s ease-in-out infinite' : 'none',
        }} />
        <span style={{
          padding: '2px 6px', borderRadius: 3, fontSize: 11, fontWeight: 600,
          textTransform: 'uppercase', letterSpacing: '0.04em', ...providerStyle,
        }}>
          {agent.ProviderName}
        </span>
        <span style={{
          padding: '2px 5px', borderRadius: 2, fontSize: 10, fontWeight: 700,
          textTransform: 'uppercase', letterSpacing: '0.06em',
          background: statusStyle.bg, color: statusStyle.color,
        }}>
          {StatusLabel[agent.Status]}
        </span>
        <span style={{
          padding: '2px 5px', borderRadius: 2, fontSize: 10, fontWeight: 600,
          letterSpacing: '0.04em', fontFamily: 'var(--mono)',
          background: agent.TMuxSession ? 'var(--teal-dim)' : 'var(--bg-2)',
          color: agent.TMuxSession ? 'var(--teal)' : 'var(--fg-4)',
        }}>
          {agent.TMuxSession ? 'tmux' : 'direct'}
        </span>
        {(isRemote || isK8s) && (
          <span style={{
            padding: '2px 6px', borderRadius: 2, fontSize: 10, fontWeight: 700,
            textTransform: 'uppercase', letterSpacing: '0.05em',
            background: isRemote ? 'rgba(55,163,163,0.15)' : 'rgba(167,114,239,0.15)',
            color: isRemote ? 'var(--teal)' : 'var(--purple)',
            border: `1px solid ${isRemote ? 'rgba(55,163,163,0.3)' : 'rgba(167,114,239,0.3)'}`,
          }}>
            {isRemote ? '⬡ sandbox' : 'k8s'}
          </span>
        )}
        <span style={{ fontSize: 11, color: 'var(--fg-4)', marginLeft: 'auto' }}>
          {timeSinceActivity()}
        </span>
        {/* Star button */}
        <button
          onClick={(e) => {
            e.stopPropagation();
            if (onToggleStar && agent.SessionFile) onToggleStar(agent.SessionFile);
          }}
          style={{
            background: 'transparent', border: 'none',
            color: starred ? 'var(--orange)' : 'var(--fg-4)',
            fontSize: 14, cursor: 'pointer', padding: '0 4px',
            opacity: starred ? 1 : 0, transition: 'opacity 0.15s',
          }}
          className="star-btn"
          title={starred ? 'Unpin session' : 'Pin session'}
        >
          {starred ? '★' : '☆'}
        </button>
      </div>

      {/* Kill button — top-right, appears on card hover */}
      {agent.LastAction === 'Deleting' ? (
        <div style={{
          position: 'absolute', top: 8, right: 8,
          fontSize: 10, fontWeight: 600, color: 'var(--accent)',
          background: 'var(--accent-dim)', padding: '2px 7px', borderRadius: 3,
          letterSpacing: '0.04em', textTransform: 'uppercase',
        }}>
          Deleting…
        </div>
      ) : onKill && (
        <button
          onClick={(e) => { e.stopPropagation(); onKill(agent.SessionID || String(agent.PID)); }}
          className="kill-btn"
          title="Kill session"
          style={{
            position: 'absolute', top: 8, right: 8,
            background: 'var(--bg-1)', border: '1px solid var(--border)',
            color: 'var(--fg-3)', fontSize: 10, fontWeight: 600,
            cursor: 'pointer', opacity: 0, transition: 'opacity 0.15s, color 0.15s, border-color 0.15s',
            padding: '2px 8px', borderRadius: 3, lineHeight: '1.4',
          }}
          onMouseEnter={e => {
            (e.currentTarget as HTMLButtonElement).style.color = 'var(--accent)';
            (e.currentTarget as HTMLButtonElement).style.borderColor = 'var(--accent)';
            (e.currentTarget as HTMLButtonElement).style.background = 'var(--accent-dim)';
          }}
          onMouseLeave={e => {
            (e.currentTarget as HTMLButtonElement).style.color = 'var(--fg-3)';
            (e.currentTarget as HTMLButtonElement).style.borderColor = 'var(--border)';
            (e.currentTarget as HTMLButtonElement).style.background = 'var(--bg-1)';
          }}
        >
          Kill
        </button>
      )}

      {/* Row 2: Title (the main visual anchor) */}
      <div style={{
        fontSize: 14, fontWeight: 600, color: 'var(--fg)', lineHeight: '1.4',
        marginBottom: 6,
        overflow: 'hidden', textOverflow: 'ellipsis',
        display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical' as const,
      }}>
        {title || agent.Name}
      </div>

      {/* Row 3: repo + branch context line */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 5, marginBottom: 8 }}>
        <span style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--fg-3)' }}>
          {agent.Name.replace(/ #\d+$/, '')}
        </span>
        <span style={{
          fontFamily: 'var(--mono)', fontSize: 11, padding: '2px 5px',
          borderRadius: 2, background: 'var(--bg-3)', color: 'var(--accent)',
        }}>
          {agent.GitBranch || 'main'}
        </span>
      </div>

      {/* Row 4: Last action */}
      {agent.LastAction && (
        <div style={{
          fontFamily: 'var(--mono)', fontSize: 11, padding: '5px 8px',
          borderRadius: 3, background: 'var(--bg-1)', border: '1px solid var(--border)',
          marginBottom: 8, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
          color: 'var(--fg-3)',
        }}>
          {shortenPath(agent.LastAction)}
        </div>
      )}

      {/* Search snippet */}
      {searchSnippet && (
        <div style={{
          fontSize: 11, fontFamily: 'var(--mono)', color: 'var(--purple)',
          fontStyle: 'italic', padding: '4px 8px', background: 'var(--purple-dim)',
          borderRadius: 3, marginBottom: 8, overflow: 'hidden',
          textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        }}>
          {searchSnippet}
        </div>
      )}

      {/* Spacer */}
      <div style={{ flex: 1 }} />

      {/* Footer: model + cpu/mem + tokens + cost */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--fg-4)' }}>
          {agent.Model}
        </span>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <span style={{
            fontFamily: 'var(--mono)', fontSize: 10,
            color: (agent.CPUPercent || 0) >= 50 ? 'var(--accent)' : (agent.CPUPercent || 0) >= 10 ? 'var(--orange)' : 'var(--fg-4)',
          }}>
            {Math.round(agent.CPUPercent || 0)}%
          </span>
          <span style={{
            fontFamily: 'var(--mono)', fontSize: 10,
            color: (agent.MemoryMB || 0) >= 1000 ? 'var(--accent)' : (agent.MemoryMB || 0) >= 500 ? 'var(--orange)' : 'var(--fg-4)',
          }}>
            {(agent.MemoryMB || 0) >= 1000 ? ((agent.MemoryMB || 0) / 1000).toFixed(1) + 'G' : (agent.MemoryMB || 0) + 'M'}
          </span>
          <span style={{ fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--fg-4)' }}>
            {formatK(agent.TokensIn)} in / {formatK(agent.TokensOut)} out
          </span>
          <span style={{ fontSize: 13, fontWeight: 700, color: 'var(--green)' }}>
            ${(agent.EstCostUSD || 0).toFixed(2)}
          </span>
        </div>
      </div>

      <style>{`
        @keyframes pulse {
          0%, 100% { opacity: 1; }
          50% { opacity: 0.5; }
        }
        .agent-card:hover:not([data-selected]) {
          border-color: var(--border-hover) !important;
        }
        .agent-card:hover .star-btn {
          opacity: 1 !important;
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
