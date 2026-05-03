import { useState, useEffect, useRef } from 'react';
import type { Agent } from '../types';
import { StatusLabel } from '../types';
import { useTraceStream } from '../hooks/useTraceStream';
import { TraceView } from './TraceView';
import { SessionView } from './SessionView';

interface RightPanelProps {
  agent: Agent;
  onClose: () => void;
}

type Tab = 'trace' | 'session';

export function RightPanel({ agent, onClose }: RightPanelProps) {
  const [activeTab, setActiveTab] = useState<Tab>('trace');
  const [width, setWidth] = useState(() => {
    const saved = localStorage.getItem('aimux-panel-width');
    return saved ? parseInt(saved) : 440;
  });
  const [isResizing, setIsResizing] = useState(false);
  const panelRef = useRef<HTMLDivElement>(null);

  const turns = useTraceStream(agent.SessionID, agent.SessionFile);

  useEffect(() => {
    if (!isResizing) return;

    const handleMouseMove = (e: MouseEvent) => {
      if (!panelRef.current) return;
      const rect = panelRef.current.getBoundingClientRect();
      const newWidth = rect.right - e.clientX;
      if (newWidth >= 300 && newWidth <= 800) {
        setWidth(newWidth);
      }
    };

    const handleMouseUp = () => {
      setIsResizing(false);
      localStorage.setItem('aimux-panel-width', String(width));
    };

    document.addEventListener('mousemove', handleMouseMove);
    document.addEventListener('mouseup', handleMouseUp);

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mouseup', handleMouseUp);
    };
  }, [isResizing, width]);

  const formatTokens = (tokens: number): string => {
    if (!tokens) return '0';
    if (tokens < 1000) return String(tokens);
    return (tokens / 1000).toFixed(1) + 'k';
  };

  const formatCost = (cost: number): string => {
    if (!cost) return '$0.00';
    return `$${cost.toFixed(2)}`;
  };

  const statusLabel = StatusLabel[agent.Status] || 'Unknown';
  const statusColor = agent.Status === 0 ? 'var(--teal)' :
                      agent.Status === 3 ? 'var(--accent)' :
                      'var(--fg-2)';

  return (
    <div
      ref={panelRef}
      style={{
        width: `${width}px`,
        height: '100%',
        background: 'var(--bg-1)',
        borderLeft: '1px solid var(--border)',
        display: 'flex',
        flexDirection: 'column',
        position: 'relative',
      }}
    >
      {/* Resize handle */}
      <div
        onMouseDown={() => setIsResizing(true)}
        style={{
          position: 'absolute',
          left: 0,
          top: 0,
          bottom: 0,
          width: '4px',
          cursor: 'ew-resize',
          background: isResizing ? 'var(--accent)' : 'transparent',
          transition: 'background 0.15s',
        }}
        onMouseEnter={(e) => {
          if (!isResizing) e.currentTarget.style.background = 'var(--border-hover)';
        }}
        onMouseLeave={(e) => {
          if (!isResizing) e.currentTarget.style.background = 'transparent';
        }}
      />

      {/* Header */}
      <div style={{
        padding: '12px 16px',
        borderBottom: '1px solid var(--border)',
        display: 'flex',
        flexDirection: 'column',
        gap: '10px',
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div style={{ fontSize: '13px', fontWeight: 600, color: 'var(--fg)' }}>
            {agent.Name}
            <span style={{ color: 'var(--fg-3)', marginLeft: '6px' }}>
              ({agent.GitBranch || 'main'})
            </span>
          </div>
          <div style={{ display: 'flex', gap: '8px' }}>
            <button
              onClick={() => {
                if (confirm('Kill this session?')) {
                  fetch(`/api/agents/${agent.SessionID || String(agent.PID)}/archive`, { method: 'POST' });
                  onClose();
                }
              }}
              style={{
                background: 'transparent',
                border: '1px solid var(--accent)',
                color: 'var(--accent)',
                fontSize: 10,
                cursor: 'pointer',
                padding: '2px 8px',
                borderRadius: 3,
              }}
              title="Kill session"
            >
              Kill
            </button>
            <button
              onClick={onClose}
              style={{
                background: 'transparent',
                border: 'none',
                color: 'var(--fg-3)',
                fontSize: '14px',
                cursor: 'pointer',
                padding: '2px',
              }}
              title="Close"
            >
              ✕
            </button>
          </div>
        </div>

        {/* Tab switcher */}
        <div style={{
          background: 'var(--bg-3)',
          borderRadius: '4px',
          padding: '2px',
          border: '1px solid var(--border)',
          display: 'flex',
          gap: '2px',
        }}>
          {(['trace', 'session'] as Tab[]).map(tab => (
            <button
              key={tab}
              onClick={() => setActiveTab(tab)}
              style={{
                flex: 1,
                background: activeTab === tab ? 'var(--bg-0)' : 'transparent',
                border: 'none',
                color: activeTab === tab ? 'var(--fg)' : 'var(--fg-3)',
                fontSize: '10px',
                fontWeight: 600,
                textTransform: 'uppercase',
                letterSpacing: '0.04em',
                padding: '3px 12px',
                borderRadius: '3px',
                cursor: 'pointer',
                boxShadow: activeTab === tab ? '0 1px 3px rgba(0,0,0,0.3)' : 'none',
              }}
            >
              {tab}
            </button>
          ))}
        </div>
      </div>

      {/* Stats ribbon */}
      <div style={{
        padding: '8px 16px',
        borderBottom: '1px solid var(--border)',
        background: 'var(--bg-2)',
        display: 'flex',
        justifyContent: 'space-between',
        fontSize: '11px',
      }}>
        <div style={{ display: 'flex', gap: '12px' }}>
          <div>
            <span style={{ color: 'var(--fg-3)' }}>Status: </span>
            <span style={{ color: statusColor }}>{statusLabel}</span>
          </div>
          <div>
            <span style={{ color: 'var(--fg-3)' }}>Turns: </span>
            <span style={{ color: 'var(--fg-2)' }}>{turns.length}</span>
          </div>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <div>
            <span style={{ color: 'var(--fg-3)' }}>Tokens: </span>
            <span style={{ color: 'var(--fg-2)' }}>
              {formatTokens(agent.TokensIn)}/{formatTokens(agent.TokensOut)}
            </span>
          </div>
          <div>
            <span style={{ color: 'var(--fg-3)' }}>Cost: </span>
            <span style={{ color: 'var(--fg-2)' }}>{formatCost(agent.EstCostUSD)}</span>
          </div>
        </div>
      </div>

      {/* Tab content */}
      <div style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
        {activeTab === 'trace' && (
          <TraceView turns={turns} sessionId={agent.SessionID} />
        )}
        {activeTab === 'session' && agent.TMuxSession && (
          <div style={{ flex: 1, position: 'relative' }}>
            <SessionView tmuxSession={agent.TMuxSession} />
          </div>
        )}
        {activeTab === 'session' && !agent.TMuxSession && (
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <p style={{ color: 'var(--fg-3)', fontSize: 13 }}>No tmux session available for this agent.</p>
          </div>
        )}
      </div>

      {/* Live strip */}
      <div style={{
        padding: '6px 16px',
        borderTop: '1px solid var(--border)',
        background: 'var(--bg-2)',
        display: 'flex',
        alignItems: 'center',
        gap: '6px',
        fontSize: '10px',
        fontWeight: 600,
        textTransform: 'uppercase',
        letterSpacing: '0.04em',
        color: 'var(--accent)',
      }}>
        <span style={{
          width: '6px',
          height: '6px',
          borderRadius: '50%',
          background: 'var(--accent)',
          animation: 'pulse 2s infinite',
        }} />
        Live
      </div>

      <style>{`
        @keyframes pulse {
          0%, 100% { opacity: 1; }
          50% { opacity: 0.4; }
        }
      `}</style>
    </div>
  );
}
