// The word the dashboard prints for an agent's state: the server's session
// state service answer (Agent.Activity: working, needs_you, idle, gone), in the
// words the TUI and the phone use. The server also moves Status to match, so the
// status filters count the same thing.

export interface Stateful {
  Status: number;
  Activity?: string;
}

// Status numbers (agent.Status) in the same words.
export const STATUS_WORDS: Record<number, string> = {
  0: 'working',
  1: 'idle',
  2: 'needs you',
  3: 'error',
  4: 'unknown',
};

const ACTIVITY_WORDS: Record<string, string> = {
  needs_you: 'needs you',
  working: 'working',
  idle: 'idle',
};

// An error status wins (it says more than idle); then the activity; then the status.
export function stateLabel(a: Stateful): string {
  if (a.Status === 3) return STATUS_WORDS[3];
  return ACTIVITY_WORDS[a.Activity ?? ''] ?? STATUS_WORDS[a.Status] ?? STATUS_WORDS[4];
}
