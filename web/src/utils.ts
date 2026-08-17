import type { Agent } from './types';

/**
 * Normalize an agent's location to one of 'local' | 'remote' | 'k8s'.
 * Treats missing/empty Location as 'local', and detects k8s either from
 * the Location field or a 'k8s://' working-directory prefix.
 */
export function normalizeLocation(agent: Agent): string {
  if (agent.Location === 'remote') return 'remote';
  if (agent.Location === 'k8s' || agent.WorkingDir?.startsWith('k8s://')) return 'k8s';
  return 'local';
}

/**
 * Format a date string into a human-readable age with enough precision
 * to locate a session after a restart.
 */
export function formatAge(dateStr: string): string {
  if (!dateStr) return '?';
  const date = new Date(dateStr);
  const d = Date.now() - date.getTime();
  if (d < 60_000) return 'now';
  if (d < 3_600_000) return `${Math.floor(d / 60_000)}m ago`;
  if (d < 86_400_000) return `${Math.floor(d / 3_600_000)}h ago`;
  const time = date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false });
  if (d < 7 * 86_400_000) {
    const day = date.toLocaleDateString(undefined, { weekday: 'short' });
    return `${day} ${time}`;
  }
  if (d < 365 * 86_400_000) {
    const md = date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
    return `${md} ${time}`;
  }
  return date.toLocaleDateString(undefined, { month: 'short', day: '2-digit', year: 'numeric' });
}
