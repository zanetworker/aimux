import { useState } from 'react';
import type { Turn, ToolSpan } from '../types';
import { Markdown } from './Markdown';

interface TraceViewProps {
  turns: Turn[];
  sessionId: string;
}

export function TraceView({ turns, sessionId }: TraceViewProps) {
  const [expandedTurns, setExpandedTurns] = useState<Set<number>>(new Set());

  const toggleTurn = (turnNumber: number) => {
    setExpandedTurns(prev => {
      const next = new Set(prev);
      if (next.has(turnNumber)) next.delete(turnNumber); else next.add(turnNumber);
      return next;
    });
  };

  const expandAll = () => setExpandedTurns(new Set(turns.map(t => t.number)));
  const collapseAll = () => setExpandedTurns(new Set());

  const handleAnnotate = async (turnNumber: number, label: 'G' | 'B' | 'W') => {
    await fetch(`/api/agents/${sessionId}/annotate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ turn: turnNumber, label, note: '' }),
    });
  };

  const formatTokens = (tokensIn: number, tokensOut: number): string => {
    const fk = (n: number) => n < 1000 ? String(n) : (n / 1000).toFixed(1) + 'k';
    return `${fk(tokensIn)}/${fk(tokensOut)}`;
  };

  const formatCost = (cost: number): string => {
    if (cost < 0.01) return '<$0.01';
    return `$${cost.toFixed(2)}`;
  };

  const shorten = (s: string): string => {
    return s
      .replace(/\/Users\/[^/]+\/go\/src\/github\.com\/[^/]+\/[^/]+\//g, '')
      .replace(/\/Users\/[^/]+\//g, '~/');
  };

  const renderToolPill = (tool: ToolSpan, idx: number) => {
    const icon = tool.success ? '✓' : '✗';
    const raw = tool.filePath || tool.snippet || '';
    let label = tool.name;
    if (raw) {
      const s = shorten(raw);
      label = `${tool.name} ${s.length > 35 ? s.substring(0, 32) + '...' : s}`;
    }

    return (
      <span key={idx} style={{
        fontSize: 9, fontFamily: 'var(--mono)', background: 'var(--bg-1)',
        border: '1px solid var(--border)', padding: '2px 6px', borderRadius: 3,
        marginRight: 3, marginBottom: 3, display: 'inline-block',
      }} title={tool.errorMsg || tool.snippet}>
        <span style={{ color: tool.success ? 'var(--green)' : 'var(--accent)', fontWeight: 700 }}>{icon}</span>{' '}
        <span style={{ color: 'var(--fg-2)' }}>{label}</span>
      </span>
    );
  };

  const renderToolDetail = (tool: ToolSpan, idx: number) => {
    const hasBody = tool.oldString || tool.newString || tool.command || tool.content || tool.pattern || tool.prompt;
    const fp = tool.filePath ? shorten(tool.filePath) : '';

    return (
      <div key={idx} style={{
        padding: '8px 10px', background: 'var(--bg-0)',
        border: `1px solid ${tool.success ? 'var(--green-dim)' : 'var(--accent-dim)'}`,
        borderRadius: 4, marginBottom: 4,
      }}>
        {/* Tool header */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: hasBody || tool.errorMsg ? 6 : 0 }}>
          <span style={{ color: tool.success ? 'var(--green)' : 'var(--accent)', fontSize: 10, fontWeight: 700 }}>
            {tool.success ? '✓' : '✗'}
          </span>
          <span style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--fg)', fontWeight: 600 }}>
            {tool.name}
          </span>
          {fp && <span style={{ fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--fg-3)' }}>{fp}</span>}
          {tool.description && <span style={{ fontSize: 10, color: 'var(--fg-3)', fontStyle: 'italic' }}>{tool.description}</span>}
        </div>

        {/* Edit diff */}
        {tool.name === 'Edit' && (tool.oldString || tool.newString) && (
          <div style={{
            fontFamily: 'var(--mono)', fontSize: 10, lineHeight: '1.5',
            borderRadius: 4, background: 'var(--bg-0)', border: '1px solid var(--border)',
            overflow: 'auto', maxHeight: 300, marginBottom: 4,
          }}>
            {renderUnifiedDiff(tool.oldString || '', tool.newString || '')}
          </div>
        )}

        {/* Bash */}
        {tool.name === 'Bash' && tool.command && (
          <pre style={{ ...codeBlock, marginBottom: 4, color: 'var(--teal)' }}><span style={{ color: 'var(--fg-4)' }}>$ </span>{tool.command}</pre>
        )}

        {/* Write */}
        {tool.name === 'Write' && tool.content && (
          <pre style={{ ...codeBlock, marginBottom: 4 }}>{tool.content}</pre>
        )}

        {/* Grep/Glob */}
        {(tool.name === 'Grep' || tool.name === 'Glob') && tool.pattern && (
          <div style={{ fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--teal)', paddingLeft: 16, marginBottom: 4 }}>
            /{tool.pattern}/
            {tool.searchPath && <span style={{ color: 'var(--fg-4)' }}> in {shorten(tool.searchPath)}</span>}
          </div>
        )}

        {/* Agent */}
        {tool.name === 'Agent' && tool.prompt && (
          <pre style={{ ...codeBlock, marginBottom: 4 }}>{tool.prompt}</pre>
        )}

        {/* Error */}
        {tool.errorMsg && (
          <div style={{ fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--accent)', paddingLeft: 16, lineHeight: '1.4' }}>
            {tool.errorMsg}
          </div>
        )}
      </div>
    );
  };

  if (turns.length === 0) {
    return (
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        height: '100%', color: 'var(--fg-3)', fontSize: 13,
        background: 'var(--bg-0)', fontFamily: 'var(--font)',
      }}>
        No conversation history.
      </div>
    );
  }

  return (
    <div style={{
      height: '100%', overflowY: 'auto', background: 'var(--bg-0)',
      display: 'flex', flexDirection: 'column', flex: 1, fontFamily: 'var(--font)',
    }}>
      {/* Toolbar */}
      <div style={{
        padding: '6px 12px', borderBottom: '1px solid var(--border)',
        display: 'flex', justifyContent: 'space-between', alignItems: 'center',
        background: 'var(--bg-0)', flexShrink: 0,
      }}>
        <span style={{ fontSize: 10, color: 'var(--fg-3)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.06em' }}>
          {turns.length} turn{turns.length !== 1 ? 's' : ''}
        </span>
        <div style={{ display: 'flex', gap: 6 }}>
          {[{ label: 'Expand all', fn: expandAll }, { label: 'Collapse all', fn: collapseAll }].map(btn => (
            <button key={btn.label} onClick={btn.fn} style={{
              background: 'transparent', border: '1px solid var(--border)', color: 'var(--fg-3)',
              fontSize: 9, fontWeight: 600, padding: '2px 8px', borderRadius: 3,
              cursor: 'pointer', textTransform: 'uppercase', letterSpacing: '0.04em',
            }}>
              {btn.label}
            </button>
          ))}
        </div>
      </div>

      {/* Turns */}
      <div style={{ padding: '6px 8px', display: 'flex', flexDirection: 'column', gap: 1 }}>
        {turns.map((turn) => {
          const isExpanded = expandedTurns.has(turn.number);
          const hasContent = (turn.outputText || '').trim().length > 0 || turn.actions.length > 0;
          const errorCount = turn.actions.filter(a => !a.success).length;

          return (
            <div key={turn.number}>
              {/* Collapsed summary row */}
              <div
                onClick={() => toggleTurn(turn.number)}
                className="trace-row"
                style={{
                  display: 'flex', alignItems: 'center', gap: 8,
                  padding: '7px 8px',
                  background: isExpanded ? 'var(--bg-1)' : 'var(--bg-0)',
                  borderRadius: isExpanded ? '6px 6px 0 0' : 6,
                  cursor: 'pointer',
                  borderLeft: `2px solid ${errorCount > 0 ? 'var(--accent)' : 'var(--border)'}`,
                }}
              >
                <span style={{
                  fontSize: 8, color: isExpanded ? 'var(--fg-3)' : 'var(--fg-4)',
                  width: 10, textAlign: 'center',
                  transition: 'transform 0.15s',
                  transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)',
                  display: 'inline-block',
                }}>▶</span>

                <span style={{ fontSize: 10, fontFamily: 'var(--mono)', color: 'var(--fg-4)', fontWeight: 700, minWidth: 20 }}>
                  {turn.number}
                </span>

                <span style={{
                  fontSize: 12, color: 'var(--fg)', flex: 1,
                  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontWeight: 500,
                }}>
                  {turn.userText ? (turn.userText.length > 80 ? turn.userText.substring(0, 77) + '...' : turn.userText) : '(system)'}
                </span>

                {turn.actions.length > 0 && (
                  <span style={{
                    fontSize: 9, fontFamily: 'var(--mono)',
                    background: errorCount > 0 ? 'var(--accent-dim)' : 'var(--bg-2)',
                    color: errorCount > 0 ? 'var(--accent)' : 'var(--fg-3)',
                    padding: '1px 5px', borderRadius: 3, fontWeight: 600,
                  }}>
                    {turn.actions.length} tool{turn.actions.length !== 1 ? 's' : ''}
                  </span>
                )}

                <span style={{ fontSize: 9, fontFamily: 'var(--mono)', color: 'var(--fg-4)' }}>
                  {formatTokens(turn.tokensIn, turn.tokensOut)}
                </span>
                <span style={{ fontSize: 9, fontFamily: 'var(--mono)', color: 'var(--green)', fontWeight: 600 }}>
                  {formatCost(turn.costUSD)}
                </span>
                <span style={{ fontSize: 9, fontFamily: 'var(--mono)', color: 'var(--fg-4)' }}>
                  {new Date(turn.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                </span>
              </div>

              {/* Expanded detail */}
              {isExpanded && hasContent && (
                <div style={{
                  background: 'var(--bg-1)', borderRadius: '0 0 6px 6px',
                  borderLeft: `2px solid ${errorCount > 0 ? 'var(--accent)' : 'var(--border)'}`,
                  padding: '12px 16px 12px 24px',
                  display: 'flex', flexDirection: 'column', gap: 14,
                }}>
                  {/* Prompt */}
                  {turn.userText && (
                    <div>
                      <SectionLabel>Prompt</SectionLabel>
                      <div style={{
                        fontSize: 12, color: 'var(--fg)', lineHeight: '1.6',
                        whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                        padding: '8px 10px', background: 'var(--bg-0)',
                        borderRadius: 4, border: '1px solid var(--border)',
                      }}>
                        {turn.userText}
                      </div>
                    </div>
                  )}

                  {/* Tools */}
                  {turn.actions.length > 0 && (
                    <div>
                      <SectionLabel>Tools ({turn.actions.length})</SectionLabel>
                      {turn.actions.map((tool, idx) => renderToolDetail(tool, idx))}
                    </div>
                  )}

                  {/* Response (rendered as markdown) */}
                  {turn.outputText && turn.outputText.trim() && (
                    <div>
                      <SectionLabel>Response</SectionLabel>
                      <div style={{
                        padding: '8px 10px', background: 'var(--bg-0)',
                        borderRadius: 4, border: '1px solid var(--border)',
                      }}>
                        <Markdown text={turn.outputText} color="var(--fg-2)" />
                      </div>
                    </div>
                  )}

                  {/* Footer */}
                  <div style={{
                    display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                    fontSize: 10, borderTop: '1px solid var(--border)', paddingTop: 8,
                  }}>
                    <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                      <span style={{ fontFamily: 'var(--mono)', color: 'var(--fg-3)' }}>
                        {formatTokens(turn.tokensIn, turn.tokensOut)} tokens
                      </span>
                      <span style={{ fontFamily: 'var(--mono)', color: 'var(--green)' }}>
                        {formatCost(turn.costUSD)}
                      </span>
                      {turn.model && (
                        <span style={{ fontFamily: 'var(--mono)', color: 'var(--fg-4)' }}>{turn.model}</span>
                      )}
                    </div>
                    <div style={{ display: 'flex', gap: 4 }}>
                      {(['G', 'B', 'W'] as const).map(label => {
                        const c = label === 'G' ? 'var(--green)' : label === 'B' ? 'var(--accent)' : 'var(--orange)';
                        const dc = label === 'G' ? 'var(--green-dim)' : label === 'B' ? 'var(--accent-dim)' : 'var(--orange-dim)';
                        return (
                          <button key={label}
                            onClick={(e) => { e.stopPropagation(); handleAnnotate(turn.number, label); }}
                            style={{
                              background: 'transparent', border: '1px solid var(--border)',
                              borderRadius: 3, padding: '2px 6px', fontSize: 10, fontWeight: 600,
                              color: c, cursor: 'pointer',
                            }}
                            onMouseEnter={(e) => { e.currentTarget.style.background = dc; e.currentTarget.style.borderColor = c; }}
                            onMouseLeave={(e) => { e.currentTarget.style.background = 'transparent'; e.currentTarget.style.borderColor = 'var(--border)'; }}
                          >{label}</button>
                        );
                      })}
                    </div>
                  </div>
                </div>
              )}

              {/* Collapsed tool pills */}
              {!isExpanded && turn.actions.length > 0 && (
                <div style={{ padding: '2px 8px 4px 28px', background: 'var(--bg-0)' }}>
                  {turn.actions.slice(0, 6).map((tool, idx) => renderToolPill(tool, idx))}
                  {turn.actions.length > 6 && (
                    <span style={{ fontSize: 9, color: 'var(--fg-4)', fontFamily: 'var(--mono)' }}>
                      +{turn.actions.length - 6} more
                    </span>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>

      <style>{`
        .trace-row:hover {
          background: var(--bg-1) !important;
        }
      `}</style>
    </div>
  );
}

function renderUnifiedDiff(oldStr: string, newStr: string): React.ReactNode {
  const oldLines = oldStr.split('\n');
  const newLines = newStr.split('\n');
  const diffLines: { type: 'ctx' | 'del' | 'add'; text: string }[] = [];

  // Simple LCS-based diff
  const oldSet = new Set(oldLines);
  const newSet = new Set(newLines);

  let oi = 0, ni = 0;
  while (oi < oldLines.length || ni < newLines.length) {
    if (oi < oldLines.length && ni < newLines.length && oldLines[oi] === newLines[ni]) {
      diffLines.push({ type: 'ctx', text: oldLines[oi] });
      oi++; ni++;
    } else if (oi < oldLines.length && !newSet.has(oldLines[oi])) {
      diffLines.push({ type: 'del', text: oldLines[oi] });
      oi++;
    } else if (ni < newLines.length && !oldSet.has(newLines[ni])) {
      diffLines.push({ type: 'add', text: newLines[ni] });
      ni++;
    } else if (oi < oldLines.length) {
      diffLines.push({ type: 'del', text: oldLines[oi] });
      oi++;
    } else {
      diffLines.push({ type: 'add', text: newLines[ni] });
      ni++;
    }
  }

  return (
    <div>
      {diffLines.map((line, i) => {
        const bg = line.type === 'del' ? 'rgba(255,49,49,0.08)' :
                   line.type === 'add' ? 'rgba(105,223,115,0.08)' : 'transparent';
        const color = line.type === 'del' ? 'var(--accent)' :
                      line.type === 'add' ? 'var(--green)' : 'var(--fg-3)';
        const prefix = line.type === 'del' ? '-' : line.type === 'add' ? '+' : ' ';
        return (
          <div key={i} style={{
            padding: '0 8px', background: bg, whiteSpace: 'pre-wrap',
            wordBreak: 'break-all', minHeight: '1.5em',
          }}>
            <span style={{ color: 'var(--fg-4)', display: 'inline-block', width: 14, userSelect: 'none' }}>{prefix}</span>
            <span style={{ color }}>{line.text}</span>
          </div>
        );
      })}
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      fontSize: 10, fontWeight: 600, color: 'var(--fg-3)',
      textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: 6,
    }}>
      {children}
    </div>
  );
}

const codeBlock: React.CSSProperties = {
  fontFamily: 'var(--mono)', fontSize: 10, lineHeight: '1.5',
  padding: '6px 8px', borderRadius: 4, background: 'var(--bg-0)',
  border: '1px solid var(--border)', whiteSpace: 'pre-wrap',
  wordBreak: 'break-all', maxHeight: 200, overflowY: 'auto', color: 'var(--fg-2)',
};
