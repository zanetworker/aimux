import { useState } from 'react';
import type { Agent } from '../types';
import type { ContentSearchResult } from '../App';
import { normalizeLocation } from '../utils';

interface Props {
  agents: Agent[];
  statusFilter: number | null;
  onStatusFilter: (s: number | null) => void;
  providerFilter: string | null;
  onProviderFilter: (p: string | null) => void;
  locationFilter: string | null;
  onLocationFilter: (l: string | null) => void;
  recentFilter: boolean;
  onRecentFilter: (v: boolean) => void;
  searchQuery: string;
  onSearchChange: (q: string) => void;
  sortBy: string;
  onSortChange: (s: string) => void;
  onContentSearch: (query: string) => void;
  onClearContentSearch: () => void;
  contentResults: ContentSearchResult[] | null;
  isSearching: boolean;
  viewMode?: 'cards' | 'list';
  onViewModeChange?: (mode: 'cards' | 'list') => void;
}

export function FilterBar({
  agents,
  statusFilter,
  onStatusFilter,
  providerFilter,
  onProviderFilter,
  locationFilter,
  onLocationFilter,
  recentFilter,
  onRecentFilter,
  searchQuery,
  onSearchChange,
  sortBy,
  onSortChange,
  onContentSearch,
  onClearContentSearch,
  contentResults,
  isSearching,
  viewMode = 'cards',
  onViewModeChange,
}: Props) {
  const [deepQuery, setDeepQuery] = useState('');

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

  const locationCounts = {
    local: agents.filter(a => normalizeLocation(a) === 'local').length,
    remote: agents.filter(a => normalizeLocation(a) === 'remote').length,
    k8s: agents.filter(a => normalizeLocation(a) === 'k8s').length,
  };

  const locationDots = {
    local: '#94a3b8',
    remote: '#38bdf8',
    k8s: '#a78bfa',
  };

  const handleDeepSearch = () => {
    if (deepQuery.trim()) {
      onContentSearch(deepQuery.trim());
    }
  };

  const handleClearDeep = () => {
    setDeepQuery('');
    onClearContentSearch();
  };

  return (
    <div style={{
      background: 'var(--bg-0)',
      borderBottom: '1px solid var(--border)',
      padding: '8px 20px',
      display: 'flex',
      alignItems: 'center',
      gap: 12,
      flexShrink: 0,
      flexWrap: 'wrap',
    }}>
      {/* Status filters */}
      <FilterPill label="All" count={statusCounts.all} active={statusFilter === null} onClick={() => onStatusFilter(null)} />
      <FilterPill label="Active" count={statusCounts.active} dotColor={statusDots.active} active={statusFilter === 0} onClick={() => onStatusFilter(0)} />
      <FilterPill label="Idle" count={statusCounts.idle} dotColor={statusDots.idle} active={statusFilter === 1} onClick={() => onStatusFilter(1)} />
      <FilterPill label="Waiting" count={statusCounts.waiting} dotColor={statusDots.waiting} active={statusFilter === 2} onClick={() => onStatusFilter(2)} />
      <FilterPill label="Error" count={statusCounts.error} dotColor={statusDots.error} active={statusFilter === 3} onClick={() => onStatusFilter(3)} />

      <Divider />

      <FilterPill label="Recent" count={recentCount} dotColor="#34d399" active={recentFilter} onClick={() => onRecentFilter(!recentFilter)} />

      <Divider />

      {/* Provider filters */}
      <FilterPill label="Claude" count={providerCounts.claude} dotColor={providerDots.claude} active={providerFilter === 'claude'} onClick={() => onProviderFilter(providerFilter === 'claude' ? null : 'claude')} />
      <FilterPill label="Codex" count={providerCounts.codex} dotColor={providerDots.codex} active={providerFilter === 'codex'} onClick={() => onProviderFilter(providerFilter === 'codex' ? null : 'codex')} />
      <FilterPill label="Gemini" count={providerCounts.gemini} dotColor={providerDots.gemini} active={providerFilter === 'gemini'} onClick={() => onProviderFilter(providerFilter === 'gemini' ? null : 'gemini')} />

      <Divider />

      {/* Location filters */}
      <FilterPill label="Local" count={locationCounts.local} dotColor={locationDots.local} active={locationFilter === 'local'} onClick={() => onLocationFilter(locationFilter === 'local' ? null : 'local')} />
      <FilterPill label="Remote" count={locationCounts.remote} dotColor={locationDots.remote} active={locationFilter === 'remote'} onClick={() => onLocationFilter(locationFilter === 'remote' ? null : 'remote')} />
      {locationCounts.k8s > 0 && (
        <FilterPill label="K8s" count={locationCounts.k8s} dotColor={locationDots.k8s} active={locationFilter === 'k8s'} onClick={() => onLocationFilter(locationFilter === 'k8s' ? null : 'k8s')} />
      )}

      <Divider />

      {/* Metadata search */}
      <input
        type="text"
        placeholder="Filter repos, branches..."
        value={searchQuery}
        onChange={(e) => onSearchChange(e.target.value)}
        aria-label="Filter by repo name or branch"
        style={{
          padding: '5px 10px',
          borderRadius: 4,
          border: '1px solid var(--border)',
          background: 'var(--bg-2)',
          color: 'var(--fg)',
          fontSize: 12,
          width: 170,
          outline: 'none',
        }}
      />

      <Divider />

      {/* Deep content search */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
        <input
          type="text"
          placeholder="Search inside sessions..."
          value={deepQuery}
          onChange={(e) => setDeepQuery(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') handleDeepSearch(); }}
          aria-label="Search inside session content using ripgrep"
          style={{
            padding: '5px 10px',
            borderRadius: 4,
            border: `1px solid ${contentResults ? 'var(--purple)' : 'var(--border)'}`,
            background: 'var(--bg-2)',
            color: 'var(--fg)',
            fontSize: 12,
            width: 190,
            outline: 'none',
          }}
        />
        <button
          onClick={handleDeepSearch}
          disabled={isSearching || !deepQuery.trim()}
          aria-label="Run deep search"
          style={{
            padding: '5px 10px',
            borderRadius: 4,
            border: '1px solid var(--purple)',
            background: 'transparent',
            color: 'var(--purple)',
            fontSize: 12,
            fontWeight: 600,
            cursor: isSearching ? 'wait' : 'pointer',
            opacity: !deepQuery.trim() ? 0.4 : 1,
          }}
        >
          {isSearching ? '...' : 'Search'}
        </button>
        {contentResults && (
          <button
            onClick={handleClearDeep}
            aria-label="Clear search results"
            style={{
              padding: '5px 8px',
              borderRadius: 4,
              border: 'none',
              background: 'var(--purple-dim)',
              color: 'var(--purple)',
              fontSize: 11,
              fontWeight: 600,
              cursor: 'pointer',
            }}
          >
            {contentResults.length} match{contentResults.length !== 1 ? 'es' : ''} ✕
          </button>
        )}
      </div>

      <Divider />

      {/* Sort */}
      <select
        value={sortBy}
        onChange={(e) => onSortChange(e.target.value)}
        aria-label="Sort sessions"
        style={{
          padding: '5px 10px',
          borderRadius: 4,
          border: '1px solid var(--border)',
          background: 'var(--bg-2)',
          color: 'var(--fg)',
          fontSize: 12,
          outline: 'none',
          cursor: 'pointer',
        }}
      >
        <option value="lastActive">Last Active</option>
        <option value="cost">Cost (high)</option>
        <option value="repo">Repo Name</option>
        <option value="status">Status</option>
      </select>

      {/* View mode toggle */}
      {onViewModeChange && (
        <div style={{ display: 'flex', gap: 0, border: '1px solid var(--border)', borderRadius: 4, overflow: 'hidden' }}>
          {(['cards', 'list'] as const).map(mode => (
            <button key={mode} onClick={() => onViewModeChange(mode)}
              title={mode === 'cards' ? 'Card view' : 'List view'}
              style={{
                padding: '4px 8px', border: 'none', cursor: 'pointer', fontSize: 12,
                background: viewMode === mode ? 'var(--bg-3)' : 'var(--bg-1)',
                color: viewMode === mode ? 'var(--fg)' : 'var(--fg-4)',
              }}>
              {mode === 'cards' ? '▦' : '☰'}
            </button>
          ))}
        </div>
      )}
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
      aria-pressed={active}
      aria-label={`${label}: ${count}`}
      style={{
        padding: '4px 12px',
        borderRadius: 12,
        border: `1px solid ${active ? 'var(--fg-3)' : 'var(--border)'}`,
        background: active ? 'var(--bg-3)' : 'transparent',
        color: active ? 'var(--fg)' : 'var(--fg-3)',
        fontSize: 12,
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
          width: 7,
          height: 7,
          borderRadius: '50%',
          background: dotColor,
        }} />
      )}
      <span>{label}</span>
      <span style={{ fontSize: 11, color: 'var(--fg-4)' }}>{count}</span>
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
