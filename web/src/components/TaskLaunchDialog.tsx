import { useState, useEffect } from 'react';

interface TaskItem {
  id: string; title: string; notes: string; due: string; status: string; listID: string;
}

interface Props {
  open: boolean;
  task: TaskItem | null;
  onClose: () => void;
  onLaunched?: (provider: string, dir: string, tmuxSession?: string) => void;
}

interface QuickDir { path: string; basename: string; exists: boolean; }
interface RecentDir { path: string; display: string; age: string; }
interface BrowseEntry { name: string; isDir: boolean; }

type DirTab = 'quick' | 'recent' | 'browse';

export function TaskLaunchDialog({ open, task, onClose, onLaunched }: Props) {
  const [provider, setProvider] = useState('claude');
  const [selectedDir, setSelectedDir] = useState('');
  const [userPrompt, setUserPrompt] = useState('');
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
    setSelectedDir('');
    fetch('/api/quick-launch')
      .then(r => r.json())
      .then(d => {
        const dirs = (d.directories || []).filter((x: QuickDir) => x.exists);
        setQuickDirs(dirs);
        if (dirs.length > 0) { setDirTab('quick'); setSelectedDir(dirs[0].path); }
      }).catch(() => {});
    fetch('/api/directories/recent')
      .then(r => r.json())
      .then(d => {
        const dirs = d.directories || [];
        setRecentDirs(dirs);
        if (!selectedDir && dirs.length > 0) setSelectedDir(dirs[0].path);
      }).catch(() => {});
  }, [open]);

  const fetchBrowse = (path: string) => {
    fetch(`/api/directories/browse?path=${encodeURIComponent(path)}`)
      .then(r => r.json())
      .then(d => { setBrowseEntries(d.entries || []); setBrowsePath(d.path); })
      .catch(() => {});
  };

  if (!open || !task) return null;

  const handleSubmit = async () => {
    if (!selectedDir) return;
    setSubmitting(true);
    try {
      const resp = await fetch('/api/agents/launch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider, dir: selectedDir, task_id: task.id,
          task_list_id: task.listID, user_prompt: userPrompt }),
      });
      const data = await resp.json();
      onLaunched?.(provider, selectedDir, data.tmux_session);
      onClose(); setUserPrompt(''); setProvider('claude'); setSelectedDir('');
    } finally { setSubmitting(false); }
  };

  const providerColors: Record<string, { bg: string; fg: string; border: string }> = {
    claude: { bg: 'var(--accent-dim)', fg: 'var(--accent)', border: 'var(--accent)' },
    codex: { bg: 'var(--green-dim)', fg: 'var(--green)', border: 'rgba(105,223,115,0.3)' },
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
        borderRadius: 10, padding: '28px 28px 24px', width: 440, maxHeight: '85vh', overflowY: 'auto' }}
        onClick={e => e.stopPropagation()}>

        <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg)', marginBottom: 24 }}>
          Launch from Task
        </div>

        {/* Task info */}
        <div style={{ background: 'var(--bg-0)', border: '1px solid var(--border)',
          borderRadius: 6, padding: 12, marginBottom: 20 }}>
          <div style={{ fontWeight: 600, fontSize: 13, color: 'var(--fg)', marginBottom: 4 }}>{task.title}</div>
          {task.notes && (
            <div style={{ fontSize: 11, color: 'var(--fg-3)', maxHeight: 50, overflow: 'auto',
              whiteSpace: 'pre-wrap', lineHeight: 1.5 }}>{task.notes}</div>
          )}
          {task.due && (
            <div style={{ fontSize: 10, color: 'var(--fg-4)', marginTop: 6 }}>
              Due: {new Date(task.due).toLocaleDateString()}
            </div>
          )}
        </div>

        {/* User prompt */}
        <div style={{ marginBottom: 20 }}>
          <div style={label}>Additional Instructions</div>
          <textarea value={userPrompt} onChange={e => setUserPrompt(e.target.value)}
            placeholder="Add specific instructions..."
            style={{ background: 'var(--bg-0)', border: '1px solid var(--border)', borderRadius: 6,
              color: 'var(--fg)', padding: '8px 12px', width: '100%', fontSize: 12,
              outline: 'none', resize: 'vertical', minHeight: 50, fontFamily: 'inherit' }}
            onFocus={e => { e.currentTarget.style.borderColor = 'var(--border-hover)'; }}
            onBlur={e => { e.currentTarget.style.borderColor = 'var(--border)'; }} />
        </div>

        {/* Provider */}
        <div style={{ marginBottom: 20 }}>
          <div style={label}>Provider</div>
          <div style={{ display: 'flex', gap: 8 }}>
            {(['claude', 'codex'] as const).map(name => (
              <button key={name} onClick={() => setProvider(name)}
                style={pill(provider === name, providerColors[name])}>{name}</button>
            ))}
          </div>
        </div>

        {/* Directory — tabbed */}
        <div style={{ marginBottom: 24 }}>
          <div style={label}>Directory</div>
          <div style={{ display: 'flex', gap: 0, marginBottom: 10 }}>
            {(['quick', 'recent', 'browse'] as const).map(tab => (
              <button key={tab}
                onClick={() => { setDirTab(tab); if (tab === 'browse' && !browsePath) fetchBrowse(selectedDir || '/'); }}
                style={{ padding: '4px 12px', fontSize: 10, fontWeight: dirTab === tab ? 600 : 400,
                  textTransform: 'uppercase', letterSpacing: '0.06em', cursor: 'pointer', border: 'none',
                  borderBottom: dirTab === tab ? '2px solid var(--accent)' : '2px solid transparent',
                  background: 'transparent', color: dirTab === tab ? 'var(--fg)' : 'var(--fg-3)' }}>
                {tab}
              </button>
            ))}
          </div>

          <div style={{ background: 'var(--bg-0)', border: '1px solid var(--border)',
            borderRadius: 6, padding: 10, minHeight: 60, maxHeight: 150, overflowY: 'auto' }}>

            {dirTab === 'quick' && (
              quickDirs.length > 0 ? (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                  {quickDirs.map(d => (
                    <button key={d.path} title={d.path} onClick={() => setSelectedDir(d.path)}
                      style={pill(selectedDir === d.path)}>{d.basename}</button>
                  ))}
                </div>
              ) : (
                <div style={{ color: 'var(--fg-4)', fontSize: 11, padding: 4 }}>No quick dirs configured.</div>
              )
            )}

            {dirTab === 'recent' && (
              recentDirs.length > 0 ? (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                  {recentDirs.map(d => (
                    <div key={d.path} onClick={() => setSelectedDir(d.path)}
                      style={{ padding: '6px 10px', borderRadius: 4, cursor: 'pointer', fontSize: 12,
                        background: selectedDir === d.path ? 'var(--bg-2)' : 'transparent',
                        color: selectedDir === d.path ? 'var(--fg)' : 'var(--fg-2)',
                        display: 'flex', justifyContent: 'space-between', transition: 'background 0.1s' }}>
                      <span style={{ fontFamily: 'var(--mono)', fontSize: 11 }}>{d.display}</span>
                      <span style={{ fontSize: 10, color: 'var(--fg-4)' }}>{d.age}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <div style={{ color: 'var(--fg-4)', fontSize: 11, padding: 4 }}>No recent directories.</div>
              )
            )}

            {dirTab === 'browse' && (
              <div>
                <div style={{ fontSize: 11, fontFamily: 'var(--mono)', color: 'var(--fg-3)',
                  marginBottom: 8, paddingBottom: 6, borderBottom: '1px solid var(--border)' }}>{browsePath}</div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
                  {browsePath !== '/' && (
                    <div onClick={() => fetchBrowse(browsePath.split('/').slice(0, -1).join('/') || '/')}
                      style={{ padding: '5px 8px', cursor: 'pointer', fontSize: 12,
                        color: 'var(--fg-3)', fontFamily: 'var(--mono)' }}>..</div>
                  )}
                  {browseEntries.filter(e => e.isDir).map(e => (
                    <div key={e.name}
                      onClick={() => fetchBrowse(browsePath === '/' ? `/${e.name}` : `${browsePath}/${e.name}`)}
                      style={{ padding: '5px 8px', cursor: 'pointer', fontSize: 12,
                        color: 'var(--teal)', fontFamily: 'var(--mono)' }}>{e.name}/</div>
                  ))}
                </div>
                <button onClick={() => setSelectedDir(browsePath)}
                  style={{ marginTop: 8, padding: '5px 0', borderRadius: 4, fontSize: 10,
                    fontWeight: 600, textTransform: 'uppercase', border: '1px solid var(--accent)',
                    background: 'var(--accent-dim)', color: 'var(--accent)', cursor: 'pointer', width: '100%' }}>
                  Select this directory
                </button>
              </div>
            )}
          </div>

          {selectedDir && (
            <div style={{ marginTop: 6, fontSize: 10, fontFamily: 'var(--mono)', color: 'var(--fg-4)' }}>
              {selectedDir}
            </div>
          )}
        </div>

        {/* Submit */}
        <button onClick={handleSubmit} disabled={!selectedDir || submitting}
          style={{ background: !selectedDir || submitting ? 'var(--bg-3)' : 'var(--accent)',
            color: !selectedDir || submitting ? 'var(--fg-3)' : '#fff', border: 'none', borderRadius: 6,
            padding: '9px 16px', fontWeight: 600, fontSize: 12,
            cursor: !selectedDir || submitting ? 'not-allowed' : 'pointer', width: '100%' }}>
          {submitting ? 'Launching...' : 'Launch Agent'}
        </button>
        <div onClick={onClose}
          style={{ color: 'var(--fg-3)', fontSize: 11, textAlign: 'center', marginTop: 10, cursor: 'pointer' }}>
          Cancel
        </div>
      </div>
    </div>
  );
}
