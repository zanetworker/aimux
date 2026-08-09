import { useState, useEffect } from 'react';

interface Props {
  open: boolean;
  onClose: () => void;
  onLaunched?: (provider: string, dir: string, tmuxSession?: string) => void;
}

interface QuickDir { path: string; basename: string; exists: boolean; }
interface RecentDir { path: string; display: string; age: string; }
interface BrowseEntry { name: string; isDir: boolean; }

type DirTab = 'quick' | 'recent' | 'browse';

export function LaunchDialog({ open, onClose, onLaunched }: Props) {
  const [provider, setProvider] = useState('claude');
  const [dir, setDir] = useState('');
  const [prompt, setPrompt] = useState('');
  const [model, setModel] = useState('');
  const [mode, setMode] = useState('default');
  const [runtime, setRuntime] = useState('local');
  const [execution, setExecution] = useState('local');
  const [shell, setShell] = useState('');
  const [sessionMgr, setSessionMgr] = useState('tmux');
  const [otelEnabled, setOtelEnabled] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [dirTab, setDirTab] = useState<DirTab>('recent');
  const [quickDirs, setQuickDirs] = useState<QuickDir[]>([]);
  const [recentDirs, setRecentDirs] = useState<RecentDir[]>([]);
  const [browseEntries, setBrowseEntries] = useState<BrowseEntry[]>([]);
  const [browsePath, setBrowsePath] = useState('');

  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    if (open) window.addEventListener('keydown', handleKey);
    return () => window.removeEventListener('keydown', handleKey);
  }, [open, onClose]);

  useEffect(() => {
    if (!open) return;
    setDir('');
    fetch('/api/quick-launch')
      .then(r => r.json())
      .then(d => {
        const dirs = (d.directories || []).filter((x: QuickDir) => x.exists);
        setQuickDirs(dirs);
        if (dirs.length > 0) {
          setDirTab('quick');
          setDir(dirs[0].path);
        }
      }).catch(() => {});
    fetch('/api/directories/recent')
      .then(r => r.json())
      .then(d => {
        const dirs = d.directories || [];
        setRecentDirs(dirs);
        if (!dir && dirs.length > 0) setDir(dirs[0].path);
      }).catch(() => {});
  }, [open]);

  const fetchBrowse = (path: string) => {
    fetch(`/api/directories/browse?path=${encodeURIComponent(path)}`)
      .then(r => r.json())
      .then(d => { setBrowseEntries(d.entries || []); setBrowsePath(d.path); })
      .catch(() => {});
  };

  if (!open) return null;

  const handleSubmit = async () => {
    if (!dir) return;
    setSubmitting(true);
    try {
      const resp = await fetch('/api/agents/launch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          provider, dir, model, mode,
          runtime, execution, shell, session_manager: sessionMgr,
          otel_enabled: otelEnabled, user_prompt: prompt,
        }),
      });
      const data = await resp.json();
      onLaunched?.(provider, dir, data.tmux_session);
      onClose();
      setDir(''); setPrompt(''); setModel(''); setProvider('claude');
      setMode('default'); setRuntime('local'); setExecution('local');
      setShell(''); setSessionMgr('tmux'); setOtelEnabled(false);
    } finally { setSubmitting(false); }
  };

  const providerColors: Record<string, { bg: string; fg: string; border: string }> = {
    claude: { bg: 'var(--accent-dim)', fg: 'var(--accent)', border: 'var(--accent)' },
    codex: { bg: 'var(--green-dim)', fg: 'var(--green)', border: 'rgba(105,223,115,0.3)' },
    gemini: { bg: 'var(--purple-dim)', fg: 'var(--purple)', border: 'rgba(167,114,239,0.3)' },
  };

  const label = { display: 'block' as const, fontSize: 10, textTransform: 'uppercase' as const,
    letterSpacing: '0.08em', color: 'var(--fg-3)', marginBottom: 8, fontWeight: 500 };

  const pill = (selected: boolean, colors?: { bg: string; fg: string; border: string }) => ({
    padding: '5px 14px', borderRadius: 4, fontSize: 11, fontWeight: 600 as const,
    textTransform: 'uppercase' as const, letterSpacing: '0.04em', cursor: 'pointer' as const,
    border: `1px solid ${selected ? (colors?.border || 'var(--accent)') : 'var(--border)'}`,
    background: selected ? (colors?.bg || 'var(--accent-dim)') : 'transparent',
    color: selected ? (colors?.fg || 'var(--accent)') : 'var(--fg-3)',
    transition: 'all 0.15s',
  });

  return (
    <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.75)', zIndex: 1000,
      display: 'flex', alignItems: 'center', justifyContent: 'center' }}
      onClick={e => { if (e.target === e.currentTarget) onClose(); }}>
      <div style={{ background: 'var(--bg-1)', border: '1px solid var(--border-hover)',
        borderRadius: 10, padding: '28px 28px 24px', width: 420, maxHeight: '85vh', overflowY: 'auto' }}
        onClick={e => e.stopPropagation()}>

        <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg)', marginBottom: 24 }}>
          Launch New Agent
        </div>

        {/* Provider */}
        <div style={{ marginBottom: 20 }}>
          <div style={label}>Provider</div>
          <div style={{ display: 'flex', gap: 8 }}>
            {(['claude', 'codex', 'gemini'] as const).map(name => (
              <button key={name} onClick={() => setProvider(name)}
                style={pill(provider === name, providerColors[name])}>{name}</button>
            ))}
          </div>
        </div>

        {/* Directory */}
        <div style={{ marginBottom: 20 }}>
          <div style={label}>Directory</div>
          <div style={{ display: 'flex', gap: 0, marginBottom: 10 }}>
            {(['quick', 'recent', 'browse'] as const).map(tab => (
              <button key={tab}
                onClick={() => { setDirTab(tab); if (tab === 'browse' && !browsePath) fetchBrowse(dir || '/'); }}
                style={{ padding: '4px 12px', fontSize: 10, fontWeight: dirTab === tab ? 600 : 400,
                  textTransform: 'uppercase', letterSpacing: '0.06em', cursor: 'pointer', border: 'none',
                  borderBottom: dirTab === tab ? '2px solid var(--accent)' : '2px solid transparent',
                  background: 'transparent', color: dirTab === tab ? 'var(--fg)' : 'var(--fg-3)',
                  transition: 'all 0.15s' }}>
                {tab}
              </button>
            ))}
          </div>

          <div style={{ background: 'var(--bg-0)', border: '1px solid var(--border)',
            borderRadius: 6, padding: 10, minHeight: 80, maxHeight: 180, overflowY: 'auto' }}>

            {dirTab === 'quick' && (
              quickDirs.length > 0 ? (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                  {quickDirs.map(d => (
                    <button key={d.path} title={d.path} onClick={() => setDir(d.path)}
                      style={pill(dir === d.path)}>{d.basename}</button>
                  ))}
                </div>
              ) : (
                <div style={{ color: 'var(--fg-4)', fontSize: 11, padding: 8 }}>
                  No quick launch directories configured.
                </div>
              )
            )}

            {dirTab === 'recent' && (
              recentDirs.length > 0 ? (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                  {recentDirs.map(d => (
                    <div key={d.path} onClick={() => setDir(d.path)}
                      style={{ padding: '7px 10px', borderRadius: 4, cursor: 'pointer', fontSize: 12,
                        background: dir === d.path ? 'var(--bg-2)' : 'transparent',
                        color: dir === d.path ? 'var(--fg)' : 'var(--fg-2)',
                        display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                        transition: 'background 0.1s' }}>
                      <span style={{ fontFamily: 'var(--mono)', fontSize: 11 }}>{d.display}</span>
                      <span style={{ fontSize: 10, color: 'var(--fg-4)' }}>{d.age}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <div style={{ color: 'var(--fg-4)', fontSize: 11, padding: 8 }}>
                  No recent directories found.
                </div>
              )
            )}

            {dirTab === 'browse' && (
              <div>
                <div style={{ fontSize: 11, fontFamily: 'var(--mono)', color: 'var(--fg-3)',
                  marginBottom: 8, paddingBottom: 6, borderBottom: '1px solid var(--border)' }}>
                  {browsePath}
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
                  {browsePath !== '/' && (
                    <div onClick={() => fetchBrowse(browsePath.split('/').slice(0, -1).join('/') || '/')}
                      style={{ padding: '5px 8px', borderRadius: 3, cursor: 'pointer', fontSize: 12,
                        color: 'var(--fg-3)', fontFamily: 'var(--mono)' }}>..</div>
                  )}
                  {browseEntries.filter(e => e.isDir).map(e => (
                    <div key={e.name}
                      onClick={() => fetchBrowse(browsePath === '/' ? `/${e.name}` : `${browsePath}/${e.name}`)}
                      style={{ padding: '5px 8px', borderRadius: 3, cursor: 'pointer', fontSize: 12,
                        color: 'var(--teal)', fontFamily: 'var(--mono)' }}>{e.name}/</div>
                  ))}
                </div>
                <button onClick={() => setDir(browsePath)}
                  style={{ marginTop: 8, padding: '5px 0', borderRadius: 4, fontSize: 10,
                    fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.06em',
                    border: '1px solid var(--accent)', background: 'var(--accent-dim)',
                    color: 'var(--accent)', cursor: 'pointer', width: '100%' }}>
                  Select this directory
                </button>
              </div>
            )}
          </div>

          {dir && (
            <div style={{ marginTop: 6, fontSize: 10, fontFamily: 'var(--mono)',
              color: 'var(--fg-4)', padding: '4px 0' }}>
              {dir}
            </div>
          )}
        </div>

        {/* Prompt */}
        <div style={{ marginBottom: 20 }}>
          <div style={label}>Prompt</div>
          <textarea value={prompt} onChange={e => setPrompt(e.target.value)}
            placeholder="What should the agent work on?"
            style={{ background: 'var(--bg-0)', border: '1px solid var(--border)', borderRadius: 6,
              color: 'var(--fg)', padding: '8px 12px', width: '100%', fontSize: 12,
              outline: 'none', resize: 'vertical', minHeight: 50, fontFamily: 'inherit' }}
            onFocus={e => { e.currentTarget.style.borderColor = 'var(--border-hover)'; }}
            onBlur={e => { e.currentTarget.style.borderColor = 'var(--border)'; }} />
        </div>

        {/* Model */}
        <div style={{ marginBottom: 16 }}>
          <div style={label}>Model</div>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            {(provider === 'codex'
              ? ['default', 'o3', 'o4-mini']
              : provider === 'gemini'
              ? ['default', 'gemini-2.5-pro', 'gemini-2.5-flash']
              : ['default', 'opus', 'sonnet', 'haiku']
            ).map(m => (
              <button key={m} onClick={() => setModel(m === 'default' ? '' : m)}
                style={pill(model === (m === 'default' ? '' : m))}>{m}</button>
            ))}
          </div>
        </div>

        {/* Permissions */}
        <div style={{ marginBottom: 16 }}>
          <div style={label}>Permissions</div>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            {(provider === 'codex'
              ? ['default', 'full-auto', 'full-access', 'read-only']
              : provider === 'gemini'
              ? ['default', 'yolo', 'auto_edit', 'plan']
              : ['default', 'plan', 'acceptEdits', 'bypass', 'dontAsk']
            ).map(m => (
              <button key={m} onClick={() => setMode(m)}
                style={pill(mode === m)}>{m}</button>
            ))}
          </div>
        </div>

        {/* Runtime */}
        <div style={{ marginBottom: 16 }}>
          <div style={label}>Runtime</div>
          <div style={{ display: 'flex', gap: 8 }}>
            {['local', 'container', 'remote'].map(r => (
              <button key={r} onClick={() => setRuntime(r)}
                style={pill(runtime === r)}>{r}</button>
            ))}
          </div>
        </div>

        {/* Execution */}
        <div style={{ marginBottom: 16 }}>
          <div style={label}>Execution</div>
          <div style={{ display: 'flex', gap: 8 }}>
            {['local', 'hybrid'].map(e => (
              <button key={e} onClick={() => setExecution(e)}
                style={pill(execution === e)}>{e}</button>
            ))}
          </div>
        </div>

        {/* Shell + Session (compact row) */}
        <div style={{ display: 'flex', gap: 16, marginBottom: 16 }}>
          <div style={{ flex: 1 }}>
            <div style={label}>Shell</div>
            <div style={{ display: 'flex', gap: 6 }}>
              {['/bin/zsh', '/bin/bash'].map(s => (
                <button key={s} onClick={() => setShell(s)}
                  style={pill(shell === s)}>{s.split('/').pop()}</button>
              ))}
            </div>
          </div>
          <div style={{ flex: 1 }}>
            <div style={label}>Session</div>
            <div style={{ display: 'flex', gap: 6 }}>
              {['tmux', 'direct'].map(s => (
                <button key={s} onClick={() => setSessionMgr(s)}
                  style={pill(sessionMgr === s)}>{s}</button>
              ))}
            </div>
          </div>
        </div>

        {/* OTEL toggle */}
        <div style={{ marginBottom: 24 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <div style={label}>Tracing</div>
            <button onClick={() => setOtelEnabled(!otelEnabled)}
              style={{ ...pill(otelEnabled), fontSize: 10 }}>
              {otelEnabled ? 'ON' : 'OFF'}
            </button>
          </div>
        </div>

        {/* Submit */}
        <button onClick={handleSubmit} disabled={!dir || submitting}
          style={{ background: !dir || submitting ? 'var(--bg-3)' : 'var(--accent)',
            color: !dir || submitting ? 'var(--fg-3)' : '#fff', border: 'none', borderRadius: 6,
            padding: '9px 16px', fontWeight: 600, fontSize: 12,
            cursor: !dir || submitting ? 'not-allowed' : 'pointer', width: '100%' }}>
          {submitting ? 'Launching...' : 'Launch Agent'}
        </button>

        <div onClick={onClose}
          style={{ color: 'var(--fg-3)', fontSize: 11, textAlign: 'center', marginTop: 10,
            cursor: 'pointer' }}>Cancel</div>
      </div>
    </div>
  );
}
