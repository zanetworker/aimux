import { useState, useEffect, useCallback } from 'react';

interface Environment {
  name: string;
  type: string;
  gateway?: string;
  namespace?: string;
}

export function EnvironmentsList() {
  const [environments, setEnvironments] = useState<Environment[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const loadEnvironments = useCallback(async () => {
    try {
      const resp = await fetch('/api/environments');
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const data = await resp.json();
      setEnvironments(data.environments || []);
    } catch (err: any) {
      setError(err.message || 'Failed to load environments');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { loadEnvironments(); }, [loadEnvironments]);

  if (loading) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <span style={{ color: 'var(--fg-3)', fontSize: 13 }}>Loading environments...</span>
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

  if (environments.length === 0) {
    return (
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 12 }}>
        <span style={{ color: 'var(--fg-3)', fontSize: 14 }}>No environments configured.</span>
        <span style={{ color: 'var(--fg-4)', fontSize: 12 }}>
          Add environments in ~/.aimux/config.yaml under "environments:"
        </span>
      </div>
    );
  }

  const typeColor = (t: string) => {
    switch (t) {
      case 'local': return 'var(--green)';
      case 'openshell': return 'var(--teal)';
      case 'kubernetes': case 'k8s': return 'var(--purple)';
      default: return 'var(--fg-3)';
    }
  };

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      <div style={{
        padding: '12px 18px', borderBottom: '1px solid var(--border)', flexShrink: 0,
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
      }}>
        <span style={{ fontSize: 11, color: 'var(--fg-4)', textTransform: 'uppercase', letterSpacing: '0.06em', fontWeight: 700 }}>
          {environments.length} environment{environments.length !== 1 ? 's' : ''}
        </span>
      </div>
      <div style={{ flex: 1, overflowY: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
          <thead style={{ position: 'sticky', top: 0, background: 'var(--bg-1)', zIndex: 1 }}>
            <tr>
              {['Name', 'Type', 'Gateway', 'Namespace'].map(h => (
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
            {environments.map(env => (
              <tr
                key={env.name}
                style={{ borderBottom: '1px solid var(--border)' }}
                onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg-1)')}
                onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
              >
                <td style={{ padding: '12px', fontWeight: 600, color: 'var(--fg)', fontFamily: 'var(--mono)' }}>
                  {env.name}
                </td>
                <td style={{ padding: '12px' }}>
                  <span style={{
                    padding: '2px 8px', borderRadius: 3, fontSize: 11, fontWeight: 600,
                    background: `color-mix(in srgb, ${typeColor(env.type)} 15%, transparent)`,
                    color: typeColor(env.type),
                  }}>
                    {env.type}
                  </span>
                </td>
                <td style={{ padding: '12px', fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--fg-3)' }}>
                  {env.gateway || '--'}
                </td>
                <td style={{ padding: '12px', fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--fg-3)' }}>
                  {env.namespace || '--'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
