import { useState, useEffect, useCallback } from 'react';

interface AgentConfig {
  name: string;
  harness: string;
  inference?: string;
  model?: string;
  prompt?: string;
  mcp?: string[];
  skills?: string[];
  policy?: string;
}

interface Props {
  onLaunch?: (agentName: string) => void;
}

export function AgentDefinitions({ onLaunch }: Props) {
  const [configs, setConfigs] = useState<AgentConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const loadConfigs = useCallback(async () => {
    try {
      const resp = await fetch('/api/agent-configs');
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const data = await resp.json();
      setConfigs(data.configs || []);
    } catch (err: any) {
      setError(err.message || 'Failed to load agent configs');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { loadConfigs(); }, [loadConfigs]);

  if (loading) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <span style={{ color: 'var(--fg-3)', fontSize: 13 }}>Loading agent definitions...</span>
      </div>
    );
  }

  if (error) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <span style={{ color: 'var(--accent)', fontSize: 13 }}>Error: {error}</span>
      </div>
    );
  }

  if (configs.length === 0) {
    return (
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 12 }}>
        <span style={{ color: 'var(--fg-3)', fontSize: 14 }}>No agent definitions found.</span>
        <span style={{ color: 'var(--fg-4)', fontSize: 12 }}>
          Add agent configs in ~/.aimux/config.yaml under "agents:"
        </span>
      </div>
    );
  }

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      <div style={{
        padding: '12px 18px', borderBottom: '1px solid var(--border)', flexShrink: 0,
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
      }}>
        <span style={{ fontSize: 11, color: 'var(--fg-4)', textTransform: 'uppercase', letterSpacing: '0.06em', fontWeight: 700 }}>
          {configs.length} agent definition{configs.length !== 1 ? 's' : ''}
        </span>
      </div>
      <div style={{ flex: 1, overflowY: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
          <thead style={{ position: 'sticky', top: 0, background: 'var(--bg-1)', zIndex: 1 }}>
            <tr>
              {['Name', 'Harness', 'Model', 'Prompt', 'MCP', 'Skills', 'Policy', ''].map(h => (
                <th key={h} style={{
                  padding: '10px 12px', textAlign: 'left', fontSize: 11, fontWeight: 700,
                  textTransform: 'uppercase', letterSpacing: '0.06em', color: 'var(--fg-3)',
                  borderBottom: '1px solid var(--border)', whiteSpace: 'nowrap',
                }}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {configs.map(c => (
              <tr
                key={c.name}
                style={{ borderBottom: '1px solid var(--border)' }}
                onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg-1)')}
                onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
              >
                <td style={{ padding: '12px', fontWeight: 600, color: 'var(--fg)', fontFamily: 'var(--mono)' }}>
                  {c.name}
                </td>
                <td style={{ padding: '12px' }}>
                  <span style={{
                    padding: '2px 8px', borderRadius: 3, fontSize: 11, fontWeight: 600,
                    background: c.harness === 'claude' ? 'rgba(167, 139, 250, 0.15)' : 'var(--bg-3)',
                    color: c.harness === 'claude' ? '#C4B5FD' : 'var(--fg-2)',
                  }}>
                    {c.harness}
                  </span>
                </td>
                <td style={{ padding: '12px', fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--teal)' }}>
                  {c.model || '--'}
                </td>
                <td style={{ padding: '12px', color: 'var(--fg-3)', maxWidth: 300 }}>
                  <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'block' }}>
                    {c.prompt || '--'}
                  </span>
                </td>
                <td style={{ padding: '12px' }}>
                  {c.mcp && c.mcp.length > 0 ? (
                    <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                      {c.mcp.map(m => (
                        <span key={m} style={{
                          fontSize: 10, padding: '1px 5px', borderRadius: 2,
                          background: 'var(--bg-3)', color: 'var(--fg-3)', fontFamily: 'var(--mono)',
                        }}>
                          {m}
                        </span>
                      ))}
                    </div>
                  ) : <span style={{ color: 'var(--fg-4)' }}>--</span>}
                </td>
                <td style={{ padding: '12px' }}>
                  {c.skills && c.skills.length > 0 ? (
                    <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                      {c.skills.map(s => (
                        <span key={s} style={{
                          fontSize: 10, padding: '1px 5px', borderRadius: 2,
                          background: 'rgba(55, 163, 163, 0.12)', color: 'var(--teal)', fontFamily: 'var(--mono)',
                        }}>
                          {s}
                        </span>
                      ))}
                    </div>
                  ) : <span style={{ color: 'var(--fg-4)' }}>--</span>}
                </td>
                <td style={{ padding: '12px', fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--fg-3)' }}>
                  {c.policy || '--'}
                </td>
                <td style={{ padding: '12px', textAlign: 'center' }}>
                  {onLaunch && (
                    <button
                      onClick={() => onLaunch(c.name)}
                      style={{
                        padding: '4px 12px', borderRadius: 4, border: '1px solid var(--accent)',
                        background: 'transparent', color: 'var(--accent)', fontSize: 11,
                        fontWeight: 600, cursor: 'pointer',
                      }}
                    >
                      Start
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
