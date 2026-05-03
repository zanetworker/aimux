import type { Turn, ToolSpan } from '../types';

interface TraceViewProps {
  turns: Turn[];
  sessionId: string;
}

export function TraceView({ turns, sessionId }: TraceViewProps) {
  const handleAnnotate = async (turnNumber: number, label: 'G' | 'B' | 'W', note: string = '') => {
    await fetch(`/api/agents/${sessionId}/annotate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ turn: turnNumber, label, note }),
    });
  };

  const formatTokens = (tokensIn: number, tokensOut: number): string => {
    const formatK = (n: number) => {
      if (n < 1000) return String(n);
      return (n / 1000).toFixed(1) + 'k';
    };
    return `${formatK(tokensIn)}/${formatK(tokensOut)}`;
  };

  const formatCost = (cost: number): string => {
    if (cost < 0.01) return '<$0.01';
    return `$${cost.toFixed(2)}`;
  };

  const renderToolPill = (tool: ToolSpan, idx: number) => {
    const icon = tool.success ? '✓' : '✗';
    const color = tool.success ? 'var(--green)' : 'var(--accent)';
    const displayText = tool.snippet || tool.name;
    return (
      <span
        key={idx}
        style={{
          fontSize: '9px',
          fontFamily: 'var(--mono)',
          background: 'var(--bg-0)',
          border: '1px solid var(--border)',
          padding: '1px 5px',
          borderRadius: '3px',
          marginRight: '4px',
          display: 'inline-block',
          color,
        }}
        title={tool.errorMsg || tool.snippet}
      >
        {icon} {displayText}
      </span>
    );
  };

  if (turns.length === 0) {
    return (
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        height: '100%',
        color: 'var(--fg-3)',
        fontSize: '13px',
      }}>
        No trace data yet
      </div>
    );
  }

  return (
    <div style={{
      height: '100%',
      overflowY: 'auto',
      padding: '12px',
      display: 'flex',
      flexDirection: 'column',
      gap: '8px',
      flex: 1,
    }}>
      {turns.map((turn) => (
        <div key={turn.number} style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
          {/* User turn */}
          {turn.userText && (
            <div style={{
              background: 'var(--bg-3)',
              borderRadius: '6px',
              padding: '8px 10px',
            }}>
              <div style={{
                fontSize: '10px',
                fontWeight: 600,
                color: 'var(--fg-3)',
                marginBottom: '4px',
                display: 'flex',
                justifyContent: 'space-between',
              }}>
                <span>Turn {turn.number} · YOU</span>
                <span>{new Date(turn.timestamp).toLocaleTimeString()}</span>
              </div>
              <div style={{
                fontSize: '12px',
                color: 'var(--fg-2)',
                lineHeight: '1.5',
              }}>
                {turn.userText}
              </div>
            </div>
          )}

          {/* Agent turn */}
          <div style={{
            background: 'var(--bg-2)',
            borderRadius: '6px',
            padding: '8px 10px',
            borderLeft: '2px solid var(--accent)',
          }}>
            <div style={{
              fontSize: '10px',
              fontWeight: 600,
              color: 'var(--fg-3)',
              marginBottom: '4px',
              display: 'flex',
              justifyContent: 'space-between',
            }}>
              <span>Turn {turn.number} · CLAUDE</span>
              <span>{new Date(turn.timestamp).toLocaleTimeString()}</span>
            </div>

            {/* Response text - truncated to 3 lines */}
            <div style={{
              fontSize: '12px',
              color: 'var(--fg-2)',
              lineHeight: '1.5',
              marginBottom: turn.actions.length > 0 ? '6px' : '0',
              overflow: 'hidden',
              display: '-webkit-box',
              WebkitLineClamp: 3,
              WebkitBoxOrient: 'vertical',
            }}>
              {turn.outputText || '(no response)'}
            </div>

            {/* Tool calls */}
            {turn.actions.length > 0 && (
              <div style={{ marginBottom: '6px' }}>
                {turn.actions.map((tool, idx) => renderToolPill(tool, idx))}
              </div>
            )}

            {/* Footer: tokens, cost, annotations */}
            <div style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              fontSize: '10px',
              color: 'var(--fg-3)',
              borderTop: '1px solid var(--border)',
              paddingTop: '6px',
              marginTop: '6px',
            }}>
              <div style={{ display: 'flex', gap: '12px' }}>
                <span>{formatTokens(turn.tokensIn, turn.tokensOut)}</span>
                <span>{formatCost(turn.costUSD)}</span>
              </div>

              <div style={{ display: 'flex', gap: '4px' }}>
                <button
                  onClick={() => handleAnnotate(turn.number, 'G')}
                  style={{
                    background: 'transparent',
                    border: '1px solid var(--border)',
                    borderRadius: '3px',
                    padding: '2px 6px',
                    fontSize: '10px',
                    fontWeight: 600,
                    color: 'var(--green)',
                    cursor: 'pointer',
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = 'var(--green-dim)';
                    e.currentTarget.style.borderColor = 'var(--green)';
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = 'transparent';
                    e.currentTarget.style.borderColor = 'var(--border)';
                  }}
                >
                  G
                </button>
                <button
                  onClick={() => handleAnnotate(turn.number, 'B')}
                  style={{
                    background: 'transparent',
                    border: '1px solid var(--border)',
                    borderRadius: '3px',
                    padding: '2px 6px',
                    fontSize: '10px',
                    fontWeight: 600,
                    color: 'var(--accent)',
                    cursor: 'pointer',
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = 'var(--accent-dim)';
                    e.currentTarget.style.borderColor = 'var(--accent)';
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = 'transparent';
                    e.currentTarget.style.borderColor = 'var(--border)';
                  }}
                >
                  B
                </button>
                <button
                  onClick={() => handleAnnotate(turn.number, 'W')}
                  style={{
                    background: 'transparent',
                    border: '1px solid var(--border)',
                    borderRadius: '3px',
                    padding: '2px 6px',
                    fontSize: '10px',
                    fontWeight: 600,
                    color: 'var(--orange)',
                    cursor: 'pointer',
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = 'var(--orange-dim)';
                    e.currentTarget.style.borderColor = 'var(--orange)';
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = 'transparent';
                    e.currentTarget.style.borderColor = 'var(--border)';
                  }}
                >
                  W
                </button>
              </div>
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}
