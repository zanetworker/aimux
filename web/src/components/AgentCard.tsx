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
  const providerColors: Record<string, { background: string; color: string }> = {
    claude: { background: 'var(--accent-dim)', color: 'var(--accent)' },
    codex: { background: 'rgba(74,222,128,0.15)', color: '#4ade80' },
    gemini: { background: 'rgba(167,139,250,0.15)', color: '#a78bfa' },
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

  const borderLeftColor = agent.Status === 0 ? 'var(--green)' :
    agent.Status === 2 ? 'var(--orange)' :
    agent.Status === 3 ? 'var(--accent)' : 'var(--fg-4)';

  const cardBg = agent.Status === 2 ? 'var(--orange-dim)' :
    agent.Status === 3 ? 'var(--accent-dim)' : 'var(--bg-0)';

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
        padding: '12px 14px',
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
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
        <div style={{
          width: 6, height: 6, borderRadius: '50%',
          background: statusStyle.dot, flexShrink: 0,
          animation: agent.Status === 0 ? 'pulse 2s ease-in-out infinite' : 'none',
        }} />
        <span style={{
          padding: '1px 5px', borderRadius: 3, fontSize: 9, fontWeight: 600,
          textTransform: 'uppercase', letterSpacing: '0.04em', ...providerStyle,
        }}>
          {agent.ProviderName}
        </span>
        <span style={{
          padding: '1px 4px', borderRadius: 2, fontSize: 8, fontWeight: 700,
          textTransform: 'uppercase', letterSpacing: '0.06em',
          background: statusStyle.bg, color: statusStyle.color,
        }}>
          {StatusLabel[agent.Status]}
        </span>
        <span style={{ fontSize: 9, color: 'var(--fg-4)', marginLeft: 'auto' }}>
          {timeSinceActivity()}
        </span>
        {/* Kill button */}
        <button
          onClick={(e) => {
            e.stopPropagation();
            if (onKill && confirm('Kill this session?')) {
              onKill(agent.SessionID || String(agent.PID));
            }
          }}
          className="kill-btn"
          style={{
            background: 'transparent', border: 'none', color: 'var(--fg-4)',
            fontSize: 11, cursor: 'pointer', opacity: 0, transition: 'opacity 0.15s',
            padding: '0 2px', lineHeight: 1,
          }}
          title="Kill session"
        >
          ✕
        </button>
      </div>

      {/* Row 2: Title (the main visual anchor) */}
      <div style={{
        fontSize: 13, fontWeight: 600, color: 'var(--fg)', lineHeight: '1.4',
        marginBottom: 4,
        overflow: 'hidden', textOverflow: 'ellipsis',
        display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical' as const,
      }}>
        {title || agent.Name}
      </div>

      {/* Row 3: repo + branch context line */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 5, marginBottom: 6 }}>
        <span style={{ fontFamily: 'var(--mono)', fontSize: 9, color: 'var(--fg-3)' }}>
          {agent.Name.replace(/ #\d+$/, '')}
        </span>
        <span style={{
          fontFamily: 'var(--mono)', fontSize: 9, padding: '1px 4px',
          borderRadius: 2, background: 'var(--bg-3)', color: 'var(--accent)',
        }}>
          {agent.GitBranch || 'main'}
        </span>
      </div>

      {/* Row 4: Last action */}
      {agent.LastAction && (
        <div style={{
          fontFamily: 'var(--mono)', fontSize: 9, padding: '4px 6px',
          borderRadius: 3, background: 'var(--bg-1)', border: '1px solid var(--border)',
          marginBottom: 6, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
          color: 'var(--fg-3)',
        }}>
          {shortenPath(agent.LastAction)}
        </div>
      )}

      {/* Search snippet */}
      {searchSnippet && (
        <div style={{
          fontSize: 9, fontFamily: 'var(--mono)', color: 'var(--purple)',
          fontStyle: 'italic', padding: '3px 6px', background: 'var(--purple-dim)',
          borderRadius: 3, marginBottom: 6, overflow: 'hidden',
          textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        }}>
          {searchSnippet}
        </div>
      )}

      {/* Spacer */}
      <div style={{ flex: 1 }} />

      {/* Footer: model + tokens + cost */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontFamily: 'var(--mono)', fontSize: 8, color: 'var(--fg-4)' }}>
          {agent.Model}
        </span>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <span style={{ fontFamily: 'var(--mono)', fontSize: 8, color: 'var(--fg-4)' }}>
            {formatK(agent.TokensIn)} in / {formatK(agent.TokensOut)} out
          </span>
          <span style={{ fontSize: 11, fontWeight: 700, color: 'var(--green)' }}>
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
