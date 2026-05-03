import type { Agent } from '../types';

interface Props {
  agents: Agent[];
  statusFilter: number | null;
  onStatusFilter: (s: number | null) => void;
  providerFilter: string | null;
  onProviderFilter: (p: string | null) => void;
  recentFilter: boolean;
  onRecentFilter: (v: boolean) => void;
  searchQuery: string;
  onSearchChange: (q: string) => void;
  sortBy: string;
  onSortChange: (s: string) => void;
}

export function FilterBar({
  agents,
  statusFilter,
  onStatusFilter,
  providerFilter,
  onProviderFilter,
  recentFilter,
  onRecentFilter,
  searchQuery,
  onSearchChange,
  sortBy,
  onSortChange,
}: Props) {
  const thirtyMinAgo = Date.now() - 30 * 60 * 1000;
  const recentCount = agents.filter(a => new Date(a.LastActivity).getTime() > thirtyMinAgo).length;

  const statusCounts = {
    all: agents.length,
    active: agents.filter(a => a.Status === 0).length,
    idle: agents.filter(a => a.Status === 1).length,
    waiting: agents.filter(a => a.Status === 2).length,
    error: agents.filter(a => a.Status === 3).length,
  };

  const providerCounts = {
    claude: agents.filter(a => a.ProviderName.toLowerCase() === 'claude').length,
    codex: agents.filter(a => a.ProviderName.toLowerCase() === 'codex').length,
    gemini: agents.filter(a => a.ProviderName.toLowerCase() === 'gemini').length,
  };

  const statusDots = {
    active: '#4ade80',
    idle: '#525252',
    waiting: '#f59e0b',
    error: '#ef4444',
  };

  const providerDots = {
    claude: 'var(--accent)',
    codex: '#4ade80',
    gemini: '#a78bfa',
  };

  return (
    <div style={{
      background: 'var(--bg-1)',
      borderBottom: '1px solid var(--border)',
      padding: '8px 20px',
      display: 'flex',
      alignItems: 'center',
      gap: 12,
      flexShrink: 0,
    }}>
      {/* Status filters */}
      <FilterPill
        label="All"
        count={statusCounts.all}
        active={statusFilter === null}
        onClick={() => onStatusFilter(null)}
      />
      <FilterPill
        label="Active"
        count={statusCounts.active}
        dotColor={statusDots.active}
        active={statusFilter === 0}
        onClick={() => onStatusFilter(0)}
      />
      <FilterPill
        label="Idle"
        count={statusCounts.idle}
        dotColor={statusDots.idle}
        active={statusFilter === 1}
        onClick={() => onStatusFilter(1)}
      />
      <FilterPill
        label="Waiting"
        count={statusCounts.waiting}
        dotColor={statusDots.waiting}
        active={statusFilter === 2}
        onClick={() => onStatusFilter(2)}
      />
      <FilterPill
        label="Error"
        count={statusCounts.error}
        dotColor={statusDots.error}
        active={statusFilter === 3}
        onClick={() => onStatusFilter(3)}
      />

      <Divider />

      {/* Recent filter */}
      <FilterPill
        label="Recent"
        count={recentCount}
        dotColor="#34d399"
        active={recentFilter}
        onClick={() => onRecentFilter(!recentFilter)}
      />

      <Divider />

      {/* Provider filters */}
      <FilterPill
        label="Claude"
        count={providerCounts.claude}
        dotColor={providerDots.claude}
        active={providerFilter === 'claude'}
        onClick={() => onProviderFilter(providerFilter === 'claude' ? null : 'claude')}
      />
      <FilterPill
        label="Codex"
        count={providerCounts.codex}
        dotColor={providerDots.codex}
        active={providerFilter === 'codex'}
        onClick={() => onProviderFilter(providerFilter === 'codex' ? null : 'codex')}
      />
      <FilterPill
        label="Gemini"
        count={providerCounts.gemini}
        dotColor={providerDots.gemini}
        active={providerFilter === 'gemini'}
        onClick={() => onProviderFilter(providerFilter === 'gemini' ? null : 'gemini')}
      />

      <Divider />

      {/* Search */}
      <input
        type="text"
        placeholder="Search repos, branches..."
        value={searchQuery}
        onChange={(e) => onSearchChange(e.target.value)}
        style={{
          padding: '4px 10px',
          borderRadius: 4,
          border: '1px solid var(--border)',
          background: 'var(--bg-2)',
          color: 'var(--fg)',
          fontSize: 11,
          width: 180,
          outline: 'none',
        }}
      />

      <Divider />

      {/* Sort */}
      <select
        value={sortBy}
        onChange={(e) => onSortChange(e.target.value)}
        style={{
          padding: '4px 8px',
          borderRadius: 4,
          border: '1px solid var(--border)',
          background: 'var(--bg-2)',
          color: 'var(--fg)',
          fontSize: 10,
          outline: 'none',
          cursor: 'pointer',
        }}
      >
        <option value="lastActive">Last Active</option>
        <option value="cost">Cost (high)</option>
        <option value="repo">Repo Name</option>
        <option value="status">Status</option>
      </select>
    </div>
  );
}

function FilterPill({
  label,
  count,
  dotColor,
  active,
  onClick,
}: {
  label: string;
  count: number;
  dotColor?: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      style={{
        padding: '3px 10px',
        borderRadius: 12,
        border: `1px solid ${active ? 'var(--fg-3)' : 'var(--border)'}`,
        background: active ? 'var(--bg-3)' : 'transparent',
        color: active ? 'var(--fg)' : 'var(--fg-3)',
        fontSize: 10,
        fontWeight: active ? 600 : 400,
        cursor: 'pointer',
        display: 'flex',
        alignItems: 'center',
        gap: 5,
        transition: 'all 0.15s ease',
      }}
    >
      {dotColor && (
        <div style={{
          width: 6,
          height: 6,
          borderRadius: '50%',
          background: dotColor,
        }} />
      )}
      <span>{label}</span>
      <span style={{
        fontSize: 9,
        color: 'var(--fg-4)',
      }}>
        {count}
      </span>
    </button>
  );
}

function Divider() {
  return (
    <div style={{
      width: 1,
      height: 16,
      background: 'var(--border)',
    }} />
  );
}
