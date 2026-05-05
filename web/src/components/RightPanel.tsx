import { useState, useEffect, useRef } from 'react';
import type { Agent } from '../types';
import { StatusLabel } from '../types';
import { useTraceStream } from '../hooks/useTraceStream';
import { TraceView } from './TraceView';
import { SessionView } from './SessionView';

interface RightPanelProps {
  agent: Agent;
  onClose: () => void;
  isFullscreen?: boolean;
  onToggleFullscreen?: () => void;
}

type Tab = 'trace' | 'session';

export function RightPanel({ agent, onClose, isFullscreen, onToggleFullscreen }: RightPanelProps) {
  const [activeTab, setActiveTab] = useState<Tab>('trace');
  const [sessionMounted, setSessionMounted] = useState(false);
  const [sessionMeta, setSessionMeta] = useState<{ annotation: string; tags: string[]; note: string }>({ annotation: '', tags: [], note: '' });
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

  useEffect(() => {
    if (!agent.SessionFile) return;
    fetch(`/api/sessions/meta?file=${encodeURIComponent(agent.SessionFile)}`)
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d) setSessionMeta({ annotation: d.annotation || '', tags: d.tags || [], note: d.note || '' });
      })
      .catch(() => {});
  }, [agent.SessionFile]);

  const annotationCycle = ['achieved', 'partial', 'failed', 'abandoned', ''];
  const handleCycleMeta = async () => {
    if (!agent.SessionFile) return;
    const idx = annotationCycle.indexOf(sessionMeta.annotation);
    const next = annotationCycle[(idx + 1) % annotationCycle.length];
    await fetch('/api/sessions/meta', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ filePath: agent.SessionFile, annotation: next }),
    });
    setSessionMeta(prev => ({ ...prev, annotation: next }));
  };

  const metaColor = (a: string): string => {
    switch (a) {
      case 'achieved': return 'var(--green)';
      case 'partial': return 'var(--orange)';
      case 'failed': return 'var(--accent)';
      case 'abandoned': return 'var(--fg-3)';
      default: return 'var(--fg-4)';
    }
  };

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
        width: isFullscreen ? '100%' : `${width}px`,
        height: '100%',
        background: '#000000',
        borderLeft: isFullscreen ? 'none' : '1px solid #111',
        display: 'flex',
        flexDirection: 'column',
        position: 'relative',
      }}
    >
      {/* Resize handle (hidden in fullscreen) */}
      {!isFullscreen && (
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
      )}

      {/* Header */}
      <div style={{
        padding: '12px 16px',
        borderBottom: '1px solid #111',
        display: 'flex',
        flexDirection: 'column',
        gap: '10px',
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div style={{ fontSize: '13px', fontWeight: 600, color: '#e6e6e6' }}>
            {agent.Name}
            <span style={{ color: 'var(--fg-3)', marginLeft: '6px' }}>
              ({agent.GitBranch || 'main'})
            </span>
          </div>
          <div style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
            {onToggleFullscreen && (
              <button
                onClick={onToggleFullscreen}
                style={{
                  background: 'transparent',
                  border: '1px solid #333',
                  color: '#888',
                  fontSize: 10,
                  cursor: 'pointer',
                  padding: '2px 8px',
                  borderRadius: 3,
                }}
                title={isFullscreen ? 'Exit fullscreen' : 'Fullscreen'}
              >
                {isFullscreen ? 'Shrink' : 'Expand'}
              </button>
            )}
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

        {/* Session meta */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          <button
            onClick={handleCycleMeta}
            title="Cycle annotation"
            style={{
              background: 'transparent',
              border: `1px solid ${sessionMeta.annotation ? metaColor(sessionMeta.annotation) : '#333'}`,
              color: sessionMeta.annotation ? metaColor(sessionMeta.annotation) : '#555',
              fontSize: 9, fontWeight: 700, textTransform: 'uppercase',
              padding: '2px 6px', borderRadius: 3, cursor: 'pointer',
            }}
          >
            {sessionMeta.annotation || 'eval'}
          </button>
          {sessionMeta.tags?.map(t => (
            <span key={t} style={{
              fontSize: 8, padding: '1px 4px', borderRadius: 2,
              background: 'var(--accent-dim)', color: 'var(--accent)',
            }}>
              {t}
            </span>
          ))}
          {sessionMeta.note && (
            <span style={{ fontSize: 9, fontStyle: 'italic', color: '#888' }}>
              &ldquo;{sessionMeta.note}&rdquo;
            </span>
          )}
        </div>

        {/* Tab switcher */}
        <div style={{
          background: '#0a0a0a',
          borderRadius: '4px',
          padding: '2px',
          border: '1px solid #1a1a1a',
          display: 'flex',
          gap: '2px',
        }}>
          {(['trace', 'session'] as Tab[]).map(tab => (
            <button
              key={tab}
              onClick={() => { setActiveTab(tab); if (tab === 'session') setSessionMounted(true); }}
              style={{
                flex: 1,
                background: activeTab === tab ? '#000000' : 'transparent',
                border: 'none',
                color: activeTab === tab ? '#e6e6e6' : '#555',
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
        borderBottom: '1px solid #111',
        background: '#050505',
        display: 'flex',
        justifyContent: 'space-between',
        fontSize: '11px',
      }}>
        <div style={{ display: 'flex', gap: '12px' }}>
          <div>
            <span style={{ color: '#555' }}>Status: </span>
            <span style={{ color: statusColor }}>{statusLabel}</span>
          </div>
          <div>
            <span style={{ color: '#555' }}>Turns: </span>
            <span style={{ color: '#b0b0b0' }}>{turns.length}</span>
          </div>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <div>
            <span style={{ color: '#555' }}>Tokens: </span>
            <span style={{ color: '#b0b0b0' }}>
              {formatTokens(agent.TokensIn)}/{formatTokens(agent.TokensOut)}
            </span>
          </div>
          <div>
            <span style={{ color: '#555' }}>Cost: </span>
            <span style={{ color: '#b0b0b0' }}>{formatCost(agent.EstCostUSD)}</span>
          </div>
        </div>
      </div>

      {/* Tab content: both rendered, toggle visibility to preserve session WebSocket */}
      <div style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column', minHeight: 0 }}>
        <div style={{ flex: 1, display: activeTab === 'trace' ? 'flex' : 'none', flexDirection: 'column', minHeight: 0 }}>
          <TraceView turns={turns} sessionId={agent.SessionID} />
        </div>
        <div style={{ flex: 1, position: 'relative', minHeight: 0, overflow: 'hidden', display: activeTab === 'session' ? 'block' : 'none' }}>
          {sessionMounted && (
            <SessionView
              tmuxSession={agent.TMuxSession || undefined}
              sessionId={agent.SessionID || undefined}
              provider={agent.ProviderName || undefined}
              workingDir={agent.WorkingDir || undefined}
              key={agent.TMuxSession || agent.SessionID}
            />
          )}
        </div>
      </div>

      {/* Live strip */}
      <div style={{
        padding: '6px 16px',
        borderTop: '1px solid #111',
        background: '#050505',
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
