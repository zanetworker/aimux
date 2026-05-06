import { useState, useEffect } from 'react';

interface PluginManifest {
  name: string;
  tab: string;
  panels: {
    id: string;
    type: 'metric-row' | 'table' | 'bar-chart' | 'list';
    title: string;
    description?: string;
    sortable?: boolean;
    expandable?: boolean;
    width?: string;
  }[];
}

interface MetricItem { label: string; value: string | number; color: string; }
interface TableRow { cells: (string | number)[]; color?: string; }
interface BarItem { label: string; value: number; secondary?: number; legend?: string[]; }
interface ListItem { title: string; subtitle?: string; body?: string; tags?: string[]; }

const colorMap: Record<string, string> = {
  green: 'var(--green)', accent: 'var(--accent)', orange: 'var(--orange)',
  purple: 'var(--purple)', teal: 'var(--teal)', 'fg-3': 'var(--fg-3)',
};
const toColor = (c: string) => colorMap[c] || `var(--${c})`;

interface Props {
  plugin: PluginManifest;
}

export function PluginView({ plugin }: Props) {
  const [data, setData] = useState<Record<string, unknown> | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [insights, setInsights] = useState<Record<string, string> | null>(null);
  const [insightLoading, setInsightLoading] = useState(false);
  const [insightError, setInsightError] = useState<string | null>(null);

  useEffect(() => {
    setLoading(true);
    fetch(`/api/plugins/${plugin.name}/data`)
      .then(r => {
        if (!r.ok) return r.text().then(t => { throw new Error(t); });
        return r.json();
      })
      .then(d => { setData(d); setError(null); })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false));
  }, [plugin.name]);

  if (loading) {
    return <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--fg-3)', fontSize: 13 }}>Loading {plugin.tab}...</div>;
  }
  if (error) {
    return <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--accent)', fontSize: 12, fontFamily: 'var(--mono)', padding: 20, textAlign: 'center' as const }}>{error}</div>;
  }
  if (!data) return null;

  const handleGenerateInsights = async () => {
    setInsightLoading(true);
    setInsightError(null);
    try {
      const resp = await fetch('/api/insight', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ data: JSON.stringify(data) }),
      });
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error(text);
      }
      const result = await resp.json();
      try {
        const parsed = JSON.parse(result.insight);
        setInsights(parsed);
      } catch {
        const byLine: Record<string, string> = {};
        const lines = result.insight.split('\n').filter((l: string) => l.trim());
        for (const line of lines) {
          const match = line.match(/^[*-]?\s*\**([^:*]+)\**:\s*(.+)/);
          if (match) {
            byLine[match[1].trim().toLowerCase().replace(/\s+/g, '-')] = match[2].trim();
          }
        }
        if (Object.keys(byLine).length > 0) {
          setInsights(byLine);
        } else {
          setInsights({ _summary: result.insight });
        }
      }
    } catch (e: any) {
      setInsightError(e.message);
    } finally {
      setInsightLoading(false);
    }
  };

  const rows: { panels: typeof plugin.panels }[] = [];
  let i = 0;
  while (i < plugin.panels.length) {
    const p = plugin.panels[i];
    if (p.width === 'half' && i + 1 < plugin.panels.length && plugin.panels[i + 1].width === 'half') {
      rows.push({ panels: [p, plugin.panels[i + 1]] });
      i += 2;
    } else {
      rows.push({ panels: [p] });
      i++;
    }
  }

  const getInsight = (panelId: string): string | undefined => {
    if (!insights) return undefined;
    if (insights[panelId]) return insights[panelId];
    if (insights[panelId.replace(/-/g, '_')]) return insights[panelId.replace(/-/g, '_')];
    const titleKey = plugin.panels.find(p => p.id === panelId)?.title?.toLowerCase().replace(/\s+/g, '-');
    if (titleKey && insights[titleKey]) return insights[titleKey];
    const titleKeySpace = plugin.panels.find(p => p.id === panelId)?.title?.toLowerCase();
    if (titleKeySpace && insights[titleKeySpace]) return insights[titleKeySpace];
    return undefined;
  };

  return (
    <div style={{ flex: 1, overflowY: 'auto', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 16 }}>
      {/* Toolbar */}
      <div style={{
        display: 'flex', justifyContent: 'space-between', alignItems: 'center',
        padding: '8px 12px', background: 'var(--bg-1)', borderRadius: 6,
        border: '1px solid var(--border)',
      }}>
        <span style={{ fontSize: 11, color: 'var(--fg-2)' }}>{plugin.panels.length} sections</span>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          {insightError && <span style={{ fontSize: 10, color: 'var(--accent)' }}>{insightError}</span>}
          {insights && !insightLoading && (
            <button onClick={() => setInsights(null)} style={{
              background: 'transparent', border: '1px solid var(--border)', borderRadius: 4,
              padding: '4px 10px', fontSize: 10, color: 'var(--fg-2)', cursor: 'pointer',
            }}>Clear Insights</button>
          )}
          <button
            onClick={handleGenerateInsights}
            disabled={insightLoading}
            style={{
              background: insightLoading ? 'var(--purple-dim)' : 'var(--purple-dim)',
              border: '1px solid var(--purple)', borderRadius: 4,
              padding: '6px 16px', fontSize: 11, fontWeight: 700,
              color: 'var(--purple)', cursor: insightLoading ? 'wait' : 'pointer',
              letterSpacing: '0.02em',
            }}
          >
            {insightLoading ? 'Analyzing...' : insights ? 'Refresh Insights' : 'Generate Insights'}
          </button>
        </div>
      </div>
      {insights && Object.keys(insights).length > 0 && (
        <div style={{
          padding: '12px 16px', background: 'var(--bg-1)', border: '1px solid var(--purple)',
          borderRadius: 6, borderLeft: '4px solid var(--purple)',
        }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--purple)', textTransform: 'uppercase' as const, letterSpacing: '0.04em', marginBottom: 10 }}>
            LLM Insights
          </div>
          {insights._summary ? (
            <div style={{ fontSize: 12, color: 'var(--fg)', lineHeight: '1.7', whiteSpace: 'pre-wrap' }}>
              {insights._summary}
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              {Object.entries(insights).map(([key, value]) => (
                <div key={key} style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
                  <span style={{ fontSize: 10, fontWeight: 600, color: 'var(--purple)', minWidth: 140, fontFamily: 'var(--mono)' }}>
                    {key.replace(/-/g, ' ').replace(/_/g, ' ')}
                  </span>
                  <span style={{ fontSize: 11, color: 'var(--fg)', lineHeight: '1.5' }}>{value}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
      {rows.map((row, ri) => (
        <div key={ri} style={{ display: 'flex', gap: 16 }}>
          {row.panels.map(panel => {
            const panelData = data[panel.id] as Record<string, unknown> | undefined;
            return (
              <div key={panel.id} style={{ flex: 1, minWidth: 0 }}>
                <div style={{ marginBottom: 10 }}>
                  <div style={{ fontSize: 12, fontWeight: 700, color: 'var(--fg)', textTransform: 'uppercase' as const, letterSpacing: '0.04em' }}>
                    {panel.title}
                  </div>
                  {panel.description && (
                    <div style={{ fontSize: 11, color: 'var(--fg-2)', lineHeight: '1.5', marginTop: 3 }}>
                      {panel.description}
                    </div>
                  )}
                  {getInsight(panel.id) && (
                    <div style={{
                      fontSize: 11, color: 'var(--fg)', lineHeight: '1.6', marginTop: 6,
                      padding: '8px 10px', background: 'var(--purple-dim)', borderRadius: 4,
                      borderLeft: '3px solid var(--purple)',
                    }}>
                      <span style={{ color: 'var(--purple)', fontWeight: 600, fontSize: 9, textTransform: 'uppercase' as const, letterSpacing: '0.04em' }}>Insight: </span>
                      {getInsight(panel.id)}
                    </div>
                  )}
                </div>
                {!panelData ? (
                  <div style={{ color: 'var(--fg-3)', fontSize: 11, fontStyle: 'italic' }}>No data</div>
                ) : panel.type === 'metric-row' ? (
                  <MetricRow items={(panelData.items || []) as MetricItem[]} />
                ) : panel.type === 'table' ? (
                  <DataTable columns={(panelData.columns || []) as string[]} rows={(panelData.rows || []) as TableRow[]} sortable={panel.sortable} />
                ) : panel.type === 'bar-chart' ? (
                  <BarChart items={(panelData.items || []) as BarItem[]} />
                ) : panel.type === 'list' ? (
                  <ExpandableList items={(panelData.items || []) as ListItem[]} expandable={panel.expandable} />
                ) : null}
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
}

function MetricRow({ items }: { items: MetricItem[] }) {
  return (
    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
      {items.map((item, i) => (
        <div key={i} style={{ display: 'flex', alignItems: 'baseline', gap: 4, padding: '6px 12px', borderRadius: 4, background: 'var(--bg-1)', border: '1px solid var(--border)' }}>
          <span style={{ fontSize: 18, fontWeight: 700, fontFamily: 'var(--mono)', color: toColor(item.color) }}>{item.value}</span>
          <span style={{ fontSize: 9, color: 'var(--fg-4)', textTransform: 'uppercase' as const, letterSpacing: '0.04em' }}>{item.label}</span>
        </div>
      ))}
    </div>
  );
}

function DataTable({ columns, rows, sortable }: { columns: string[]; rows: TableRow[]; sortable?: boolean }) {
  const [sortCol, setSortCol] = useState<number | null>(null);
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('asc');

  const handleSort = (col: number) => {
    if (!sortable) return;
    if (sortCol === col) { setSortDir(d => d === 'asc' ? 'desc' : 'asc'); }
    else { setSortCol(col); setSortDir('asc'); }
  };

  let sorted = rows;
  if (sortable && sortCol !== null) {
    sorted = [...rows].sort((a, b) => {
      const av = a.cells[sortCol], bv = b.cells[sortCol];
      const cmp = typeof av === 'number' && typeof bv === 'number' ? av - bv : String(av).localeCompare(String(bv));
      return sortDir === 'asc' ? cmp : -cmp;
    });
  }

  const rowColor = (c?: string) => c ? toColor(c) : 'var(--fg)';

  const isNumericCol = (ci: number): boolean => {
    if (rows.length === 0) return false;
    return rows.some(r => typeof r.cells[ci] === 'number');
  };

  return (
    <div style={{ maxHeight: 320, overflowY: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 11 }}>
        <thead style={{ position: 'sticky', top: 0, background: 'var(--bg-0)', zIndex: 1 }}>
          <tr>
            {columns.map((col, ci) => {
              const align = isNumericCol(ci) ? 'right' as const : 'left' as const;
              return (
                <th key={ci} onClick={() => handleSort(ci)} style={{
                  padding: '6px 10px', textAlign: align, fontSize: 9, fontWeight: 700,
                  textTransform: 'uppercase' as const, letterSpacing: '0.06em', borderBottom: '1px solid var(--border)',
                  color: sortCol === ci ? 'var(--fg)' : 'var(--fg-2)', cursor: sortable ? 'pointer' : 'default',
                  userSelect: 'none' as const,
                }}>
                  {col} {sortCol === ci ? (sortDir === 'asc' ? '\u25b2' : '\u25bc') : ''}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {sorted.map((row, ri) => (
            <tr key={ri} onMouseEnter={e => ((e.currentTarget as HTMLElement).style.background = 'var(--bg-1)')} onMouseLeave={e => ((e.currentTarget as HTMLElement).style.background = 'transparent')}>
              {row.cells.map((cell, ci) => {
                const numeric = isNumericCol(ci);
                return (
                  <td key={ci} style={{
                    padding: '6px 10px',
                    textAlign: numeric ? 'right' as const : 'left' as const,
                    color: ci === 0 ? 'var(--fg)' : rowColor(row.color),
                    fontFamily: numeric ? 'var(--mono)' : 'inherit',
                    borderBottom: '1px solid var(--bg-2)',
                  }}>
                    {typeof cell === 'number' && columns[ci]?.toLowerCase().includes('rate') ? `${cell}%` : cell}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function BarChart({ items }: { items: BarItem[] }) {
  const max = Math.max(...items.map(i => i.value + (i.secondary || 0)), 1);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      {items.map((item, i) => (
        <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 10 }}>
          <span title={item.label} style={{ width: 200, minWidth: 200, textAlign: 'right' as const, color: 'var(--fg)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' as const, fontFamily: 'var(--mono)', fontSize: 10 }}>
            {item.label}
          </span>
          <div style={{ flex: 1, display: 'flex', height: 14, borderRadius: 2, overflow: 'hidden', background: 'var(--bg-2)' }}>
            <div style={{ width: `${(item.value / max) * 100}%`, background: 'var(--teal)', borderRadius: '2px 0 0 2px' }} />
            {item.secondary !== undefined && item.secondary > 0 && (
              <div style={{ width: `${(item.secondary / max) * 100}%`, background: 'var(--purple)' }} />
            )}
          </div>
          <span style={{ fontFamily: 'var(--mono)', fontSize: 10, color: 'var(--fg-2)', minWidth: 30 }}>
            {item.value}{item.secondary ? `+${item.secondary}` : ''}
          </span>
        </div>
      ))}
      {items[0]?.legend && (
        <div style={{ display: 'flex', gap: 12, marginTop: 4, paddingLeft: 208 }}>
          {items[0].legend.map((l, i) => (
            <div key={l} style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 10, color: 'var(--fg-2)' }}>
              <div style={{ width: 8, height: 8, borderRadius: 2, background: i === 0 ? 'var(--teal)' : 'var(--purple)' }} />
              {l}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function ExpandableList({ items, expandable }: { items: ListItem[]; expandable?: boolean }) {
  const [expanded, setExpanded] = useState<Set<number>>(new Set());

  const tagColor = (tag: string) => {
    if (tag === 'needs review' || tag === 'action needed') return { bg: 'var(--accent-dim)', fg: 'var(--accent)', border: 'var(--accent)' };
    if (tag === 'low sample' || tag === 'monitor') return { bg: 'var(--orange-dim)', fg: 'var(--orange)', border: 'var(--orange)' };
    if (tag === 'approved') return { bg: 'var(--green-dim)', fg: 'var(--green)', border: 'var(--green)' };
    if (tag === 'pending') return { bg: 'var(--orange-dim)', fg: 'var(--orange)', border: 'var(--orange)' };
    return { bg: 'var(--accent-dim)', fg: 'var(--accent)', border: 'var(--border)' };
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      {items.map((item, i) => {
        const isExpanded = expanded.has(i);
        const hasTag = item.tags && item.tags.length > 0;
        const borderColor = hasTag ? tagColor(item.tags![0]).border : 'var(--border)';
        return (
          <div key={i} style={{
            background: 'var(--bg-1)', borderRadius: 4,
            border: '1px solid var(--border)',
            borderLeft: `3px solid ${borderColor}`,
          }}>
            <div
              onClick={() => expandable && item.body ? setExpanded(prev => { const n = new Set(prev); n.has(i) ? n.delete(i) : n.add(i); return n; }) : undefined}
              style={{ padding: '8px 10px', display: 'flex', alignItems: 'center', gap: 8, cursor: expandable && item.body ? 'pointer' : 'default' }}
            >
              {expandable && item.body && (
                <span style={{ fontSize: 8, color: 'var(--fg-4)', transform: isExpanded ? 'rotate(90deg)' : 'none', transition: 'transform 0.15s', display: 'inline-block' }}>{'\u25b6'}</span>
              )}
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 12, color: 'var(--fg)', fontWeight: 600 }}>{item.title}</div>
                {item.subtitle && <div style={{ fontSize: 10, color: 'var(--fg-2)', marginTop: 2 }}>{item.subtitle}</div>}
              </div>
              {item.tags?.map(t => {
                const tc = tagColor(t);
                return (
                  <span key={t} style={{ fontSize: 8, fontWeight: 600, padding: '2px 6px', borderRadius: 3, background: tc.bg, color: tc.fg }}>{t}</span>
                );
              })}
            </div>
            {isExpanded && item.body && (
              <div style={{ padding: '4px 10px 10px 28px', fontSize: 11, color: 'var(--fg)', lineHeight: '1.6' }}>
                {item.body}
              </div>
            )}
          </div>
        );
      })}
      {items.length === 0 && (
        <div style={{ color: 'var(--fg-3)', fontSize: 11, fontStyle: 'italic', padding: '8px 10px' }}>None</div>
      )}
    </div>
  );
}
