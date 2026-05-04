import { useState } from 'react';
import type { Turn, ToolSpan } from '../types';

interface TraceViewProps {
  turns: Turn[];
  sessionId: string;
}

export function TraceView({ turns, sessionId }: TraceViewProps) {
  const [expandedTurns, setExpandedTurns] = useState<Set<number>>(new Set());

  const toggleTurn = (turnNumber: number) => {
    setExpandedTurns(prev => {
      const next = new Set(prev);
      if (next.has(turnNumber)) {
        next.delete(turnNumber);
      } else {
        next.add(turnNumber);
      }
      return next;
    });
  };

  const expandAll = () => {
    setExpandedTurns(new Set(turns.map(t => t.number)));
  };

  const collapseAll = () => {
    setExpandedTurns(new Set());
  };

  const handleAnnotate = async (turnNumber: number, label: 'G' | 'B' | 'W', note: string = '') => {
    await fetch(`/api/agents/${sessionId}/annotate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ turn: turnNumber, label, note }),
    });
  };

  const stripMarkdown = (text: string): string => {
    return text
      .replace(/\*\*(.*?)\*\*/g, '$1')
      .replace(/\*(.*?)\*/g, '$1')
      .replace(/`([^`]+)`/g, '$1')
      .replace(/^#{1,6}\s+/gm, '')
      .replace(/^\s*[-*+]\s+/gm, '- ')
      .replace(/\|[-:]+\|/g, '')
      .replace(/^\|(.+)\|$/gm, (_, content) => content.replace(/\|/g, ', ').trim())
      .replace(/\n{3,}/g, '\n\n')
      .trim();
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

  const shortenSnippet = (snippet: string): string => {
    return snippet
      .replace(/\/Users\/[^/]+\/go\/src\/github\.com\/[^/]+\/[^/]+\//g, '')
      .replace(/\/Users\/[^/]+\//g, '~/');
  };

  const renderToolPill = (tool: ToolSpan, idx: number) => {
    const icon = tool.success ? '✓' : '✗';
    const iconColor = tool.success ? 'var(--green)' : 'var(--accent)';

    let displayText = tool.name;
    const raw = tool.filePath || tool.snippet || '';
    if (raw) {
      let snippet = shortenSnippet(raw);
      if (snippet.length > 40) {
        snippet = snippet.substring(0, 37) + '...';
      }
      displayText = `${tool.name} ${snippet}`;
    }

    return (
      <span
        key={idx}
        style={{
          fontSize: 9,
          fontFamily: 'var(--mono)',
          background: 'var(--bg-1)',
          border: '1px solid var(--border)',
          padding: '2px 6px',
          borderRadius: 3,
          marginRight: 4,
          marginBottom: 3,
          display: 'inline-block',
        }}
        title={tool.errorMsg || tool.snippet}
      >
        <span style={{ color: iconColor, fontWeight: 700 }}>{icon}</span>{' '}
        <span style={{ color: 'var(--fg-2)' }}>{displayText}</span>
      </span>
    );
  };

  const codeBlockStyle: React.CSSProperties = {
    fontFamily: 'var(--mono)',
    fontSize: 10,
    lineHeight: '1.5',
    padding: '6px 8px',
    borderRadius: 3,
    background: 'var(--bg-0)',
    border: '1px solid var(--border)',
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-all',
    maxHeight: 200,
    overflowY: 'auto',
    color: 'var(--fg-2)',
  };

  const renderToolDetail = (tool: ToolSpan, idx: number) => {
    const hasDetail = tool.oldString || tool.newString || tool.command || tool.content || tool.pattern || tool.prompt;
    const filePath = tool.filePath ? shortenSnippet(tool.filePath) : '';

    return (
      <div
        key={idx}
        style={{
          padding: '8px 10px',
          background: 'var(--bg-0)',
          border: `1px solid ${tool.success ? 'var(--green-dim)' : 'var(--accent-dim)'}`,
          borderRadius: 4,
          marginBottom: 4,
        }}
      >
        {/* Header line */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: hasDetail || tool.errorMsg ? 6 : 0 }}>
          <span style={{
            color: tool.success ? 'var(--green)' : 'var(--accent)',
            fontSize: 10,
            fontWeight: 700,
          }}>
            {tool.success ? '✓' : '✗'}
          </span>
          <span style={{
            fontFamily: 'var(--mono)',
            fontSize: 11,
            color: 'var(--fg)',
            fontWeight: 600,
          }}>
            {tool.name}
          </span>
          {filePath && (
            <span style={{
              fontFamily: 'var(--mono)',
              fontSize: 10,
              color: 'var(--fg-3)',
            }}>
              {filePath}
            </span>
          )}
          {tool.description && (
            <span style={{
              fontSize: 10,
              color: 'var(--fg-3)',
              fontStyle: 'italic',
            }}>
              {tool.description}
            </span>
          )}
        </div>

        {/* Edit: diff view */}
        {tool.name === 'Edit' && (tool.oldString || tool.newString) && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginBottom: 4 }}>
            {tool.oldString && (
              <div>
                <div style={{ fontSize: 9, color: 'var(--accent)', fontWeight: 600, marginBottom: 2 }}>- OLD</div>
                <pre style={{ ...codeBlockStyle, borderColor: 'var(--accent-dim)', color: 'var(--fg-3)' }}>
                  {tool.oldString}
                </pre>
              </div>
            )}
            {tool.newString && (
              <div>
                <div style={{ fontSize: 9, color: 'var(--green)', fontWeight: 600, marginBottom: 2 }}>+ NEW</div>
                <pre style={{ ...codeBlockStyle, borderColor: 'var(--green-dim)', color: 'var(--fg-2)' }}>
                  {tool.newString}
                </pre>
              </div>
            )}
          </div>
        )}

        {/* Bash: full command */}
        {tool.name === 'Bash' && tool.command && (
          <pre style={{ ...codeBlockStyle, marginBottom: 4 }}>
            {tool.command}
          </pre>
        )}

        {/* Write: content preview */}
        {tool.name === 'Write' && tool.content && (
          <pre style={{ ...codeBlockStyle, marginBottom: 4 }}>
            {tool.content}
          </pre>
        )}

        {/* Grep/Glob: pattern */}
        {(tool.name === 'Grep' || tool.name === 'Glob') && tool.pattern && (
          <div style={{ fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--fg-2)', paddingLeft: 16, marginBottom: 4 }}>
            pattern: {tool.pattern}
            {tool.searchPath && <span style={{ color: 'var(--fg-3)' }}> in {shortenSnippet(tool.searchPath)}</span>}
          </div>
        )}

        {/* Agent: prompt */}
        {tool.name === 'Agent' && tool.prompt && (
          <pre style={{ ...codeBlockStyle, marginBottom: 4 }}>
            {tool.prompt}
          </pre>
        )}

        {/* Error */}
        {tool.errorMsg && (
          <div style={{
            fontFamily: 'var(--mono)',
            fontSize: 10,
            color: 'var(--accent)',
            paddingLeft: 16,
            lineHeight: '1.4',
          }}>
            {tool.errorMsg}
          </div>
        )}
      </div>
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
        fontSize: 13,
        background: 'var(--bg-0)',
        fontFamily: 'var(--font)',
      }}>
        No conversation history.
      </div>
    );
  }

  return (
    <div style={{
      height: '100%',
      overflowY: 'auto',
      background: 'var(--bg-0)',
      display: 'flex',
      flexDirection: 'column',
      flex: 1,
      fontFamily: 'var(--font)',
    }}>
      {/* Toolbar */}
      <div style={{
        padding: '6px 12px',
        borderBottom: '1px solid var(--border)',
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        background: 'var(--bg-0)',
        flexShrink: 0,
      }}>
        <span style={{
          fontSize: 10,
          color: 'var(--fg-3)',
          fontWeight: 600,
          textTransform: 'uppercase',
          letterSpacing: '0.06em',
        }}>
          {turns.length} turn{turns.length !== 1 ? 's' : ''}
        </span>
        <div style={{ display: 'flex', gap: 8 }}>
          {[{ label: 'Expand all', fn: expandAll }, { label: 'Collapse all', fn: collapseAll }].map(btn => (
            <button
              key={btn.label}
              onClick={btn.fn}
              style={{
                background: 'transparent',
                border: '1px solid var(--border)',
                color: 'var(--fg-3)',
                fontSize: 9,
                fontWeight: 600,
                padding: '2px 8px',
                borderRadius: 3,
                cursor: 'pointer',
                textTransform: 'uppercase',
                letterSpacing: '0.04em',
              }}
            >
              {btn.label}
            </button>
          ))}
        </div>
      </div>

      {/* Turns */}
      <div style={{ padding: '8px 10px', display: 'flex', flexDirection: 'column', gap: 2 }}>
        {turns.map((turn) => {
          const isExpanded = expandedTurns.has(turn.number);
          const outputText = stripMarkdown(turn.outputText || '');
          const hasContent = outputText.length > 0 || turn.actions.length > 0;
          const errorCount = turn.actions.filter(a => !a.success).length;

          return (
            <div key={turn.number}>
              {/* Collapsed row */}
              <div
                onClick={() => toggleTurn(turn.number)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '6px 8px',
                  background: isExpanded ? 'var(--bg-1)' : 'var(--bg-0)',
                  borderRadius: isExpanded ? '4px 4px 0 0' : 4,
                  cursor: 'pointer',
                  borderLeft: `2px solid ${errorCount > 0 ? 'var(--accent)' : turn.actions.length > 0 ? 'var(--border)' : 'var(--border)'}`,
                  transition: 'background 0.1s',
                }}
                onMouseEnter={(e) => { e.currentTarget.style.background = 'var(--bg-1)'; }}
                onMouseLeave={(e) => { e.currentTarget.style.background = isExpanded ? 'var(--bg-1)' : 'var(--bg-0)'; }}
              >
                {/* Chevron */}
                <span style={{
                  fontSize: 8,
                  color: 'var(--fg-4)',
                  width: 10,
                  textAlign: 'center',
                  transition: 'transform 0.15s',
                  transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)',
                }}>
                  ▶
                </span>

                {/* Turn number */}
                <span style={{
                  fontSize: 10,
                  fontFamily: 'var(--mono)',
                  color: 'var(--fg-3)',
                  fontWeight: 700,
                  minWidth: 24,
                }}>
                  #{turn.number}
                </span>

                {/* Prompt preview */}
                <span style={{
                  fontSize: 12,
                  color: 'var(--fg)',
                  flex: 1,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                  fontWeight: 500,
                }}>
                  {turn.userText
                    ? (turn.userText.length > 60 ? turn.userText.substring(0, 57) + '...' : turn.userText)
                    : '(system)'}
                </span>

                {/* Tool count */}
                {turn.actions.length > 0 && (
                  <span style={{
                    fontSize: 9,
                    fontFamily: 'var(--mono)',
                    background: errorCount > 0 ? 'var(--accent-dim)' : 'var(--bg-2)',
                    color: errorCount > 0 ? 'var(--accent)' : 'var(--fg-3)',
                    padding: '1px 5px',
                    borderRadius: 3,
                    fontWeight: 600,
                  }}>
                    {turn.actions.length} tool{turn.actions.length !== 1 ? 's' : ''}
                    {errorCount > 0 && ` (${errorCount} err)`}
                  </span>
                )}

                {/* Tokens */}
                <span style={{
                  fontSize: 9,
                  fontFamily: 'var(--mono)',
                  color: 'var(--fg-4)',
                }}>
                  {formatTokens(turn.tokensIn, turn.tokensOut)}
                </span>

                {/* Cost */}
                <span style={{
                  fontSize: 9,
                  fontFamily: 'var(--mono)',
                  color: 'var(--green)',
                  fontWeight: 600,
                }}>
                  {formatCost(turn.costUSD)}
                </span>

                {/* Time */}
                <span style={{
                  fontSize: 9,
                  fontFamily: 'var(--mono)',
                  color: 'var(--fg-4)',
                }}>
                  {new Date(turn.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                </span>
              </div>

              {/* Expanded content */}
              {isExpanded && hasContent && (
                <div style={{
                  background: 'var(--bg-1)',
                  borderLeft: `2px solid ${errorCount > 0 ? 'var(--accent)' : 'var(--border)'}`,
                  borderRadius: '0 0 4px 4px',
                  padding: '12px 16px 12px 24px',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 12,
                }}>
                  {/* Full user prompt */}
                  {turn.userText && (
                    <div>
                      <div style={{
                        fontSize: 10,
                        fontWeight: 600,
                        color: 'var(--fg-3)',
                        textTransform: 'uppercase',
                        letterSpacing: '0.06em',
                        marginBottom: 6,
                      }}>
                        Prompt
                      </div>
                      <div style={{
                        fontSize: 12,
                        color: 'var(--fg)',
                        lineHeight: '1.6',
                        whiteSpace: 'pre-wrap',
                        wordBreak: 'break-word',
                      }}>
                        {turn.userText}
                      </div>
                    </div>
                  )}

                  {/* Tool calls */}
                  {turn.actions.length > 0 && (
                    <div>
                      <div style={{
                        fontSize: 10,
                        fontWeight: 600,
                        color: 'var(--fg-3)',
                        textTransform: 'uppercase',
                        letterSpacing: '0.06em',
                        marginBottom: 6,
                      }}>
                        Tools ({turn.actions.length})
                      </div>
                      {turn.actions.map((tool, idx) => renderToolDetail(tool, idx))}
                    </div>
                  )}

                  {/* Response */}
                  {outputText && (
                    <div>
                      <div style={{
                        fontSize: 10,
                        fontWeight: 600,
                        color: 'var(--fg-3)',
                        textTransform: 'uppercase',
                        letterSpacing: '0.06em',
                        marginBottom: 6,
                      }}>
                        Response
                      </div>
                      <div style={{
                        fontSize: 12,
                        color: 'var(--fg-2)',
                        lineHeight: '1.6',
                        whiteSpace: 'pre-wrap',
                        wordBreak: 'break-word',
                      }}>
                        {outputText}
                      </div>
                    </div>
                  )}

                  {/* Footer */}
                  <div style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    fontSize: 10,
                    color: 'var(--fg-4)',
                    borderTop: '1px solid var(--border)',
                    paddingTop: 8,
                    marginTop: 2,
                  }}>
                    <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                      <span style={{ fontFamily: 'var(--mono)', color: 'var(--fg-3)' }}>
                        {formatTokens(turn.tokensIn, turn.tokensOut)} tokens
                      </span>
                      <span style={{ fontFamily: 'var(--mono)', color: 'var(--green)' }}>
                        {formatCost(turn.costUSD)}
                      </span>
                      {turn.model && (
                        <span style={{ fontFamily: 'var(--mono)', color: 'var(--fg-4)' }}>
                          {turn.model}
                        </span>
                      )}
                    </div>

                    <div style={{ display: 'flex', gap: 4 }}>
                      {(['G', 'B', 'W'] as const).map(label => {
                        const color = label === 'G' ? 'var(--green)' : label === 'B' ? 'var(--accent)' : 'var(--orange)';
                        const dimColor = label === 'G' ? 'var(--green-dim)' : label === 'B' ? 'var(--accent-dim)' : 'var(--orange-dim)';
                        return (
                          <button
                            key={label}
                            onClick={(e) => {
                              e.stopPropagation();
                              handleAnnotate(turn.number, label);
                            }}
                            style={{
                              background: 'transparent',
                              border: '1px solid var(--border)',
                              borderRadius: 3,
                              padding: '2px 6px',
                              fontSize: 10,
                              fontWeight: 600,
                              color,
                              cursor: 'pointer',
                            }}
                            onMouseEnter={(e) => {
                              e.currentTarget.style.background = dimColor;
                              e.currentTarget.style.borderColor = color;
                            }}
                            onMouseLeave={(e) => {
                              e.currentTarget.style.background = 'transparent';
                              e.currentTarget.style.borderColor = 'var(--border)';
                            }}
                          >
                            {label}
                          </button>
                        );
                      })}
                    </div>
                  </div>
                </div>
              )}

              {/* Collapsed tool pills */}
              {!isExpanded && turn.actions.length > 0 && (
                <div style={{
                  padding: '2px 8px 4px 28px',
                  background: 'var(--bg-0)',
                }}>
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
    </div>
  );
}
