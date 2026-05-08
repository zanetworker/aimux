import { useState, useEffect } from 'react';

interface TaskItem {
  id: string;
  title: string;
  notes: string;
  due: string;
  status: string;
  listID: string;
}

interface TaskListItem {
  id: string;
  name: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onLaunchFromTask: (task: TaskItem) => void;
}

function formatDueDate(isoDate: string): string {
  if (!isoDate) return '';
  const date = new Date(isoDate);
  return date.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
}

export default function TasksPanel({ open, onClose, onLaunchFromTask }: Props) {
  const [panelWidth, setPanelWidth] = useState(320);
  const [lists, setLists] = useState<TaskListItem[]>([]);
  const [selectedListId, setSelectedListId] = useState<string>('');
  const [tasks, setTasks] = useState<TaskItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Fetch task lists on open
  useEffect(() => {
    if (!open) return;

    setLoading(true);
    setError(null);

    fetch('/api/tasks/lists')
      .then(res => {
        if (!res.ok) throw new Error(`Failed to fetch lists: ${res.statusText}`);
        return res.json();
      })
      .then(data => {
        const fetchedLists = data.lists || [];
        setLists(fetchedLists);
        if (fetchedLists.length > 0) {
          setSelectedListId(fetchedLists[0].id);
        }
      })
      .catch(err => {
        setError(err.message);
      })
      .finally(() => {
        setLoading(false);
      });
  }, [open]);

  // Fetch tasks when list changes
  useEffect(() => {
    if (!selectedListId) return;

    setLoading(true);
    setError(null);

    fetch(`/api/tasks?list=${selectedListId}`)
      .then(res => {
        if (!res.ok) throw new Error(`Failed to fetch tasks: ${res.statusText}`);
        return res.json();
      })
      .then(data => {
        const fetchedTasks = data.tasks || [];
        setTasks(fetchedTasks.map((t: TaskItem) => ({ ...t, listID: selectedListId })));
      })
      .catch(err => {
        setError(err.message);
      })
      .finally(() => {
        setLoading(false);
      });
  }, [selectedListId]);

  const handleReopen = async (taskId: string) => {
    try {
      const res = await fetch(`/api/tasks/${taskId}/reopen`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ listId: selectedListId })
      });

      if (!res.ok) throw new Error(`Failed to reopen task: ${res.statusText}`);

      // Update local state
      setTasks(prevTasks =>
        prevTasks.map(t => (t.id === taskId ? { ...t, status: 'needsAction' } : t))
      );
    } catch (err) {
      setError((err as Error).message);
    }
  };

  if (!open) return null;

  const pendingTasks = tasks.filter(t => t.status === 'needsAction');
  const completedTasks = tasks.filter(t => t.status === 'completed');

  const handleDragStart = (e: React.MouseEvent) => {
    e.preventDefault();
    const startX = e.clientX;
    const startWidth = panelWidth;
    const onMouseMove = (ev: MouseEvent) => {
      const newWidth = Math.max(220, Math.min(600, startWidth + (startX - ev.clientX)));
      setPanelWidth(newWidth);
    };
    const onMouseUp = () => {
      document.removeEventListener('mousemove', onMouseMove);
      document.removeEventListener('mouseup', onMouseUp);
    };
    document.addEventListener('mousemove', onMouseMove);
    document.addEventListener('mouseup', onMouseUp);
  };

  return (
    <div
      style={{
        position: 'fixed',
        top: 0,
        right: 0,
        bottom: 0,
        width: `${panelWidth}px`,
        background: 'var(--bg-0)',
        color: 'var(--fg)',
        borderLeft: '1px solid var(--border)',
        display: 'flex',
        flexDirection: 'column',
        zIndex: 100
      }}
    >
      {/* Resize handle */}
      <div
        onMouseDown={handleDragStart}
        style={{
          position: 'absolute',
          left: -3,
          top: 0,
          bottom: 0,
          width: 6,
          cursor: 'col-resize',
          zIndex: 101,
        }}
      />
      {/* Header */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '12px',
          borderBottom: '1px solid var(--border)'
        }}
      >
        <div style={{ fontSize: '13px', fontWeight: 600 }}>
          Tasks ({tasks.length})
        </div>
        <button
          onClick={onClose}
          style={{
            background: 'none',
            border: 'none',
            color: 'var(--fg-3)',
            cursor: 'pointer',
            fontSize: '14px',
            padding: '2px 6px'
          }}
        >
          [x]
        </button>
      </div>

      {/* List selector */}
      {lists.length > 0 && (
        <div style={{ padding: '8px 12px', borderBottom: '1px solid var(--border)' }}>
          <select
            value={selectedListId}
            onChange={e => setSelectedListId(e.target.value)}
            style={{
              width: '100%',
              background: 'var(--bg-1)',
              color: 'var(--fg)',
              border: '1px solid var(--border)',
              borderRadius: '4px',
              padding: '4px 6px',
              fontSize: '12px'
            }}
          >
            {lists.map(list => (
              <option key={list.id} value={list.id}>
                {list.name}
              </option>
            ))}
          </select>
        </div>
      )}

      {/* Error message */}
      {error && (
        <div
          style={{
            padding: '8px 12px',
            background: 'var(--bg-1)',
            color: 'var(--accent)',
            fontSize: '11px',
            borderBottom: '1px solid var(--border)'
          }}
        >
          {error}
        </div>
      )}

      {/* Loading state */}
      {loading && (
        <div
          style={{
            padding: '12px',
            fontSize: '11px',
            color: 'var(--fg-4)',
            textAlign: 'center'
          }}
        >
          Loading...
        </div>
      )}

      {/* Tasks container */}
      {!loading && (
        <div style={{ flex: 1, overflowY: 'auto', padding: '12px' }}>
          {/* Pending section */}
          {pendingTasks.length > 0 && (
            <div style={{ marginBottom: '16px' }}>
              <div
                style={{
                  fontSize: '9px',
                  textTransform: 'uppercase',
                  color: 'var(--fg-4)',
                  letterSpacing: '0.06em',
                  marginBottom: '8px'
                }}
              >
                Pending
              </div>
              {pendingTasks.map(task => (
                <div
                  key={task.id}
                  style={{
                    marginBottom: '12px',
                    paddingBottom: '12px',
                    borderBottom: '1px solid var(--border)'
                  }}
                >
                  <div
                    style={{
                      fontSize: '12px',
                      lineHeight: 1.4,
                      marginBottom: '4px'
                    }}
                  >
                    {task.title}
                  </div>
                  {task.due && (
                    <div style={{ fontSize: '10px', color: 'var(--fg-4)', marginBottom: '4px' }}>
                      {formatDueDate(task.due)}
                    </div>
                  )}
                  <button
                    onClick={() => onLaunchFromTask(task)}
                    style={{
                      background: 'none',
                      border: 'none',
                      color: 'var(--accent)',
                      cursor: 'pointer',
                      fontSize: '10px',
                      fontWeight: 600,
                      padding: 0
                    }}
                  >
                    Launch
                  </button>
                </div>
              ))}
            </div>
          )}

          {/* Completed section */}
          {completedTasks.length > 0 && (
            <div>
              <div
                style={{
                  fontSize: '9px',
                  textTransform: 'uppercase',
                  color: 'var(--fg-4)',
                  letterSpacing: '0.06em',
                  marginBottom: '8px'
                }}
              >
                Completed
              </div>
              {completedTasks.map(task => (
                <div
                  key={task.id}
                  style={{
                    marginBottom: '12px',
                    paddingBottom: '12px',
                    borderBottom: '1px solid var(--border)'
                  }}
                >
                  <div
                    style={{
                      fontSize: '12px',
                      lineHeight: 1.4,
                      color: 'var(--fg-4)',
                      textDecoration: 'line-through',
                      marginBottom: '4px'
                    }}
                  >
                    {task.title}
                  </div>
                  <button
                    onClick={() => handleReopen(task.id)}
                    style={{
                      background: 'none',
                      border: 'none',
                      color: 'var(--fg-3)',
                      cursor: 'pointer',
                      fontSize: '10px',
                      padding: 0
                    }}
                  >
                    Reopen
                  </button>
                </div>
              ))}
            </div>
          )}

          {/* Empty state */}
          {!loading && tasks.length === 0 && (
            <div
              style={{
                fontSize: '11px',
                color: 'var(--fg-4)',
                textAlign: 'center',
                marginTop: '20px'
              }}
            >
              No tasks
            </div>
          )}
        </div>
      )}
    </div>
  );
}
