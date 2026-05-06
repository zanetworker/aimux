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
  const [showEvalHelp, setShowEvalHelp] = useState(false);
  const wasBypass = agent.PermissionMode === 'bypassPermissions' || agent.PermissionMode === 'bypass' || agent.PermissionMode === 'full-auto';
  const [skipPermissions, setSkipPermissions] = useState(wasBypass);
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

  const metaDimColor = (a: string): string => {
    switch (a) {
      case 'achieved': return 'var(--green-dim)';
      case 'partial': return 'var(--orange-dim)';
      case 'failed': return 'var(--accent-dim)';
      default: return 'transparent';
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
          <div style={{ display: 'flex', gap: '7px', alignItems: 'center' }}>
            <button
              onClick={onClose}
              title="Close panel"
              style={{
                width: 13, height: 13, borderRadius: '50%', border: 'none',
                background: '#ff5f57', cursor: 'pointer', padding: 0,
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                fontSize: 9, fontWeight: 800, color: '#4a0002', lineHeight: 1,
              }}
            >&times;</button>
            <button
              onClick={() => {
                if (confirm('Kill this session?')) {
                  fetch(`/api/agents/${agent.SessionID || String(agent.PID)}/archive`, { method: 'POST' });
                  onClose();
                }
              }}
              title="Kill session"
              style={{
                width: 13, height: 13, borderRadius: '50%', border: 'none',
                background: '#febc2e', cursor: 'pointer', padding: 0,
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                fontSize: 10, fontWeight: 800, color: '#5a4100', lineHeight: 1,
              }}
            >&ndash;</button>
            {onToggleFullscreen && (
              <button
                onClick={onToggleFullscreen}
                title={isFullscreen ? 'Exit fullscreen' : 'Fullscreen'}
                style={{
                  width: 13, height: 13, borderRadius: '50%', border: 'none',
                  background: '#28c840', cursor: 'pointer', padding: 0,
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                  fontSize: 8, fontWeight: 800, color: '#0a4a12', lineHeight: 1,
                }}
              >{isFullscreen ? '\u25c0\u25b6' : '\u25b6'}</button>
            )}
          </div>
        </div>

        {/* Session meta */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
            <button
              onClick={handleCycleMeta}
              style={{
                background: sessionMeta.annotation ? metaDimColor(sessionMeta.annotation) : 'transparent',
                border: `1px solid ${sessionMeta.annotation ? metaColor(sessionMeta.annotation) : '#333'}`,
                color: sessionMeta.annotation ? metaColor(sessionMeta.annotation) : '#555',
                fontSize: 9, fontWeight: 700, textTransform: 'uppercase',
                padding: '2px 6px', borderRadius: 3, cursor: 'pointer',
              }}
            >
              {sessionMeta.annotation || 'eval'}
            </button>
            <button
              onClick={() => setShowEvalHelp(v => !v)}
              style={{
                width: 16, height: 16, borderRadius: '50%', fontSize: 9, fontWeight: 700,
                background: showEvalHelp ? 'var(--bg-3)' : 'transparent',
                border: '1px solid #444', color: '#888',
                cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
                padding: 0, lineHeight: 1,
              }}
            >?</button>
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
          {showEvalHelp && (
            <div style={{
              background: 'var(--bg-2)', border: '1px solid var(--border)',
              borderRadius: 4, padding: '8px 10px', fontSize: 10, lineHeight: '1.6',
              color: 'var(--fg-2)',
            }}>
              <div style={{ fontWeight: 600, color: 'var(--fg)', marginBottom: 4, fontSize: 11 }}>Session Evaluation</div>
              <div>Click the badge to rate this session's overall outcome:</div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3, marginTop: 6, paddingLeft: 8 }}>
                <div><span style={{ color: 'var(--green)', fontWeight: 600 }}>Achieved</span> &mdash; goal fully met, session succeeded</div>
                <div><span style={{ color: 'var(--orange)', fontWeight: 600 }}>Partial</span> &mdash; some progress but not complete</div>
                <div><span style={{ color: 'var(--accent)', fontWeight: 600 }}>Failed</span> &mdash; did not accomplish the goal</div>
                <div><span style={{ color: 'var(--fg-3)', fontWeight: 600 }}>Abandoned</span> &mdash; gave up or switched approach</div>
              </div>
              <div style={{ marginTop: 6, color: 'var(--fg-3)', fontSize: 9 }}>
                Turn-level labels (Good/Bad/Waste/Error) appear on hover over each trace row.
                Use Export JSONL or Export OTEL in the trace toolbar to save annotated data.
              </div>
            </div>
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
          <TraceView turns={turns} sessionId={agent.SessionID} sessionFile={agent.SessionFile} provider={agent.ProviderName} />
        </div>
        <div style={{ flex: 1, position: 'relative', minHeight: 0, overflow: 'hidden', display: activeTab === 'session' ? 'flex' : 'none', flexDirection: 'column' }}>
          {/* Permission toggle bar */}
          {activeTab === 'session' && (
            <div style={{
              padding: '6px 12px', borderBottom: '1px solid #111',
              display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0,
              background: skipPermissions ? 'var(--orange-dim)' : 'var(--bg-0)',
            }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer', fontSize: 10 }}>
                <input
                  type="checkbox"
                  checked={skipPermissions}
                  onChange={e => setSkipPermissions(e.target.checked)}
                  style={{ accentColor: 'var(--orange)' }}
                />
                <span style={{ color: skipPermissions ? 'var(--orange)' : 'var(--fg-3)', fontWeight: skipPermissions ? 600 : 400 }}>
                  Skip permissions
                </span>
              </label>
              {wasBypass && (
                <span style={{ fontSize: 9, color: 'var(--fg-4)', fontStyle: 'italic' }}>
                  (original session used bypass mode)
                </span>
              )}
              {!wasBypass && skipPermissions && (
                <span style={{ fontSize: 9, color: 'var(--orange)', fontStyle: 'italic' }}>
                  {agent.ProviderName === 'codex' ? 'Session will resume with --full-auto' : 'Session will resume with --dangerously-skip-permissions'}
                </span>
              )}
            </div>
          )}
          <div style={{ flex: 1, position: 'relative', minHeight: 0, overflow: 'hidden' }}>
            {sessionMounted && (
              <SessionView
                tmuxSession={agent.TMuxSession || undefined}
                sessionId={agent.SessionID || undefined}
                provider={agent.ProviderName || undefined}
                workingDir={agent.WorkingDir || undefined}
                skipPermissions={skipPermissions}
                key={`${agent.TMuxSession || agent.SessionID}-${skipPermissions}`}
              />
            )}
          </div>
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
