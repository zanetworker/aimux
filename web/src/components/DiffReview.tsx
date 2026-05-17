import React, { useState, useEffect } from 'react';

interface DiffLine {
  type: 'add' | 'del' | 'ctx' | 'collapse';
  text: string;
  oldNum?: number;
  newNum?: number;
  count?: number;
}

interface DiffHunk {
  lines: DiffLine[];
}

interface FileDiff {
  path: string;
  shortPath: string;
  status: string;
  added: number;
  removed: number;
  hunks: DiffHunk[];
}

interface Props {
  sessionFile: string;
  refreshInterval?: number;
  compact?: boolean;
}

export function DiffReview({ sessionFile, refreshInterval, compact }: Props) {
  const [files, setFiles] = useState<FileDiff[]>([]);
  const [totalAdded, setTotalAdded] = useState(0);
  const [totalRemoved, setTotalRemoved] = useState(0);
  const [loading, setLoading] = useState(true);
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [fileFilter, setFileFilter] = useState('');
  const [collapsedFiles, setCollapsedFiles] = useState<Set<string>>(new Set());
  const [collapsedDirs, setCollapsedDirs] = useState<Set<string>>(new Set());

  useEffect(() => {
    if (!sessionFile) return;

    const fetchDiffs = (isInitial: boolean) => {
      if (isInitial) setLoading(true);
      fetch(`/api/sessions/diffs?file=${encodeURIComponent(sessionFile)}`)
        .then(r => r.ok ? r.json() : null)
        .then(data => {
          if (data) {
            setFiles(data.files || []);
            setTotalAdded(data.totalAdded || 0);
            setTotalRemoved(data.totalRemoved || 0);
            if (isInitial && data.files?.length > 0) {
              setSelectedFile(data.files[0].path);
            }
          }
          if (isInitial) setLoading(false);
        })
        .catch(() => { if (isInitial) setLoading(false); });
    };

    fetchDiffs(true);

    if (refreshInterval && refreshInterval > 0) {
      const id = setInterval(() => fetchDiffs(false), refreshInterval);
      return () => clearInterval(id);
    }
  }, [sessionFile, refreshInterval]);

  if (loading) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--fg-3)' }}>
        Loading diffs...
      </div>
    );
  }

  if (files.length === 0) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--fg-3)', fontSize: 13 }}>
        No code changes in this session
      </div>
    );
  }

  const filtered = fileFilter
    ? files.filter(f => f.path.toLowerCase().includes(fileFilter.toLowerCase()))
    : files;

  const selectedDiff = files.find(f => f.path === selectedFile);

  const toggleCollapse = (path: string) => {
    setCollapsedFiles(prev => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path); else next.add(path);
      return next;
    });
  };

  const statusIcon = (status: string) => {
    switch (status) {
      case 'added': return { label: 'A', color: 'var(--green)' };
      case 'modified': return { label: 'M', color: 'var(--orange)' };
      case 'deleted': return { label: 'D', color: 'var(--accent)' };
      default: return { label: '?', color: 'var(--fg-4)' };
    }
  };

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      {/* Header */}
      <div style={{
        padding: '8px 12px', borderBottom: '1px solid var(--border)',
        display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0,
      }}>
        <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--fg)' }}>
          {files.length} file{files.length !== 1 ? 's' : ''} changed
        </span>
        <span style={{ fontSize: 11, color: 'var(--green)', fontFamily: 'var(--mono)' }}>+{totalAdded}</span>
        <span style={{ fontSize: 11, color: 'var(--accent)', fontFamily: 'var(--mono)' }}>-{totalRemoved}</span>
      </div>

      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {/* File tree */}
        <div style={{
          width: compact ? 240 : 280, borderRight: '1px solid var(--border)',
          overflowY: 'auto', flexShrink: 0,
        }}>
          <input
            type="text"
            placeholder="Filter files..."
            value={fileFilter}
            onChange={e => setFileFilter(e.target.value)}
            style={{
              width: '100%', padding: '6px 10px', border: 'none',
              borderBottom: '1px solid var(--border)', background: 'var(--bg-1)',
              color: 'var(--fg)', fontSize: 11, outline: 'none',
              boxSizing: 'border-box',
            }}
          />
          {renderFileTree(filtered, selectedFile, collapsedDirs, setCollapsedDirs, setSelectedFile, statusIcon)}
        </div>

        {/* Diff content */}
        <div style={{ flex: 1, overflowY: 'auto', fontFamily: 'var(--mono)', fontSize: 12, lineHeight: '1.6' }}>
          {selectedDiff ? (
            <div>
              {/* File header */}
              <div style={{
                padding: '8px 16px', background: 'var(--bg-1)',
                borderBottom: '1px solid var(--border)',
                display: 'flex', alignItems: 'center', gap: 8,
                position: 'sticky', top: 0, zIndex: 1,
              }}>
                <span style={{ color: 'var(--fg)', fontSize: 12, fontWeight: 600 }}>
                  {selectedDiff.path}
                </span>
                <span style={{
                  fontSize: 9, fontWeight: 700, padding: '1px 4px', borderRadius: 2,
                  color: statusIcon(selectedDiff.status).color,
                  background: selectedDiff.status === 'added' ? 'rgba(105,223,115,0.1)' : 'rgba(255,159,28,0.1)',
                }}>
                  {selectedDiff.status === 'added' ? 'new file' : 'modified'}
                </span>
                <span style={{ fontSize: 10, color: 'var(--green)', marginLeft: 'auto' }}>+{selectedDiff.added}</span>
                {selectedDiff.removed > 0 && (
                  <span style={{ fontSize: 10, color: 'var(--accent)' }}>-{selectedDiff.removed}</span>
                )}
                <button
                  onClick={() => toggleCollapse(selectedDiff.path)}
                  style={{
                    background: 'transparent', border: '1px solid var(--border)',
                    color: 'var(--fg-3)', fontSize: 9, padding: '2px 6px',
                    borderRadius: 3, cursor: 'pointer',
                  }}
                >
                  {collapsedFiles.has(selectedDiff.path) ? 'Expand' : 'Collapse'}
                </button>
              </div>

              {/* Diff lines */}
              {!collapsedFiles.has(selectedDiff.path) && selectedDiff.hunks.map((hunk, hi) => (
                <div key={hi}>
                  {hi > 0 && (
                    <div style={{
                      padding: '4px 16px', background: 'var(--bg-1)',
                      borderTop: '1px solid var(--border)', borderBottom: '1px solid var(--border)',
                      color: 'var(--fg-4)', fontSize: 10, fontStyle: 'italic',
                    }}>
                      ···
                    </div>
                  )}
                  {hunk.lines.map((line, li) => {
                    const bg = line.type === 'del' ? 'rgba(255,49,49,0.06)'
                      : line.type === 'add' ? 'rgba(105,223,115,0.06)'
                      : 'transparent';
                    const color = line.type === 'del' ? 'var(--accent)'
                      : line.type === 'add' ? 'var(--green)'
                      : 'var(--fg-3)';
                    const prefix = line.type === 'del' ? '-' : line.type === 'add' ? '+' : ' ';
                    const lineNum = line.type === 'del' ? line.oldNum : line.newNum;

                    return (
                      <div key={`${hi}-${li}`} style={{
                        display: 'flex', background: bg, minHeight: '1.6em',
                        borderLeft: line.type === 'del' ? '3px solid var(--accent)'
                          : line.type === 'add' ? '3px solid var(--green)'
                          : '3px solid transparent',
                      }}>
                        <span style={{
                          width: 48, textAlign: 'right', padding: '0 8px',
                          color: 'var(--fg-4)', userSelect: 'none', flexShrink: 0,
                          fontSize: 11, opacity: 0.5,
                        }}>
                          {lineNum || ''}
                        </span>
                        <span style={{
                          width: 16, textAlign: 'center',
                          color: color, userSelect: 'none', flexShrink: 0,
                          fontWeight: 700,
                        }}>
                          {prefix}
                        </span>
                        <span style={{
                          color, whiteSpace: 'pre-wrap', wordBreak: 'break-all',
                          padding: '0 8px', flex: 1,
                        }}>
                          {line.text}
                        </span>
                      </div>
                    );
                  })}
                </div>
              ))}
            </div>
          ) : (
            <div style={{ padding: 20, color: 'var(--fg-3)', fontSize: 13 }}>
              Select a file to view changes
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

interface TreeNode {
  name: string;
  path: string;
  isDir: boolean;
  children: TreeNode[];
  file?: FileDiff;
}

function buildTree(files: FileDiff[]): TreeNode[] {
  const root: TreeNode = { name: '', path: '', isDir: true, children: [] };

  for (const f of files) {
    const parts = f.path.split('/');
    let current = root;

    for (let i = 0; i < parts.length; i++) {
      const part = parts[i];
      const isLast = i === parts.length - 1;
      const partPath = parts.slice(0, i + 1).join('/');

      if (isLast) {
        current.children.push({ name: part, path: f.path, isDir: false, children: [], file: f });
      } else {
        let dir = current.children.find(c => c.isDir && c.name === part);
        if (!dir) {
          dir = { name: part, path: partPath, isDir: true, children: [] };
          current.children.push(dir);
        }
        current = dir;
      }
    }
  }

  return flattenSingleChildDirs(root.children);
}

function flattenSingleChildDirs(nodes: TreeNode[]): TreeNode[] {
  return nodes.map(node => {
    if (!node.isDir) return node;
    node.children = flattenSingleChildDirs(node.children);
    if (node.children.length === 1 && node.children[0].isDir) {
      const child = node.children[0];
      return { ...child, name: node.name + '/' + child.name, path: child.path };
    }
    return node;
  });
}

function renderFileTree(
  files: FileDiff[],
  selectedFile: string | null,
  collapsedDirs: Set<string>,
  setCollapsedDirs: (fn: (prev: Set<string>) => Set<string>) => void,
  setSelectedFile: (path: string) => void,
  statusIcon: (status: string) => { label: string; color: string },
) {
  const tree = buildTree(files);

  const rows: React.ReactNode[] = [];

  function renderNode(node: TreeNode, depth: number) {
    if (node.isDir) {
      const isCollapsed = collapsedDirs.has(node.path);
      const dirFiles = collectFiles(node);
      const dirAdded = dirFiles.reduce((s, f) => s + f.added, 0);
      const dirRemoved = dirFiles.reduce((s, f) => s + f.removed, 0);

      rows.push(
        <div
          key={'dir-' + node.path}
          onClick={() => setCollapsedDirs(prev => {
            const next = new Set(prev);
            if (next.has(node.path)) next.delete(node.path); else next.add(node.path);
            return next;
          })}
          style={{
            padding: '4px 8px', paddingLeft: 8 + depth * 12,
            cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 4,
            color: 'var(--fg-3)', fontSize: 11,
          }}
        >
          <span style={{ fontSize: 10, width: 12, flexShrink: 0, userSelect: 'none' }}>
            {isCollapsed ? '▸' : '▾'}
          </span>
          <span style={{ fontSize: 9, opacity: 0.5, flexShrink: 0 }}>📁</span>
          <span title={node.path} style={{ fontFamily: 'var(--mono)', fontWeight: 600, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {node.name}
          </span>
          <span style={{ fontSize: 9, color: 'var(--fg-4)', flexShrink: 0 }}>
            {dirFiles.length}
          </span>
          <span style={{ fontSize: 9, color: 'var(--green)', fontFamily: 'var(--mono)', flexShrink: 0 }}>
            +{dirAdded}
          </span>
          {dirRemoved > 0 && (
            <span style={{ fontSize: 9, color: 'var(--accent)', fontFamily: 'var(--mono)', flexShrink: 0 }}>
              -{dirRemoved}
            </span>
          )}
        </div>
      );

      if (!isCollapsed) {
        const dirs = node.children.filter(c => c.isDir).sort((a, b) => a.name.localeCompare(b.name));
        const nodeFiles = node.children.filter(c => !c.isDir).sort((a, b) => a.name.localeCompare(b.name));
        for (const child of [...dirs, ...nodeFiles]) {
          renderNode(child, depth + 1);
        }
      }
    } else {
      const f = node.file!;
      const si = statusIcon(f.status);
      const isSelected = f.path === selectedFile;

      rows.push(
        <div
          key={'file-' + f.path}
          onClick={() => setSelectedFile(f.path)}
          style={{
            padding: '4px 8px', paddingLeft: 20 + depth * 12,
            cursor: 'pointer',
            background: isSelected ? 'var(--bg-2)' : 'transparent',
            borderLeft: isSelected ? '2px solid var(--accent)' : '2px solid transparent',
            display: 'flex', alignItems: 'center', gap: 5,
          }}
        >
          <span style={{ fontSize: 9, fontWeight: 700, color: si.color, width: 12, textAlign: 'center', flexShrink: 0 }}>
            {si.label}
          </span>
          <span title={f.path} style={{
            fontSize: 11, color: isSelected ? 'var(--fg)' : 'var(--fg-2)',
            overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1,
            fontFamily: 'var(--mono)',
          }}>
            {node.name}
          </span>
          <span style={{ fontSize: 9, color: 'var(--green)', fontFamily: 'var(--mono)', flexShrink: 0 }}>
            +{f.added}
          </span>
          {f.removed > 0 && (
            <span style={{ fontSize: 9, color: 'var(--accent)', fontFamily: 'var(--mono)', flexShrink: 0 }}>
              -{f.removed}
            </span>
          )}
        </div>
      );
    }
  }

  const topDirs = tree.filter(n => n.isDir).sort((a, b) => a.name.localeCompare(b.name));
  const topFiles = tree.filter(n => !n.isDir).sort((a, b) => a.name.localeCompare(b.name));
  for (const node of [...topDirs, ...topFiles]) {
    renderNode(node, 0);
  }

  return <>{rows}</>;
}

function collectFiles(node: TreeNode): FileDiff[] {
  if (!node.isDir) return node.file ? [node.file] : [];
  return node.children.flatMap(c => collectFiles(c));
}
