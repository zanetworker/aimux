import { useEffect, useState } from 'react';
import type { Turn } from '../types';

export function useTraceStream(sessionId: string | null): Turn[] {
  const [turns, setTurns] = useState<Turn[]>([]);

  useEffect(() => {
    if (!sessionId) {
      setTurns([]);
      return;
    }

    // Subscribe to trace events
    fetch(`/api/trace/subscribe/${sessionId}`, { method: 'POST' });

    const handleTraceEvent = (e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data);
        if (data.sessionId === sessionId && data.turns) {
          setTurns(prev => {
            const existing = new Set(prev.map(t => t.number));
            const newTurns = data.turns.filter((t: Turn) => !existing.has(t.number));
            return [...prev, ...newTurns];
          });
        }
      } catch {
        // ignore parse errors
      }
    };

    // Listen on the existing SSE connection for trace events
    // The EventSource is managed by useAgentStream, but we can create a second one
    const es = new EventSource('/api/events');
    es.addEventListener('trace', handleTraceEvent);

    return () => {
      es.close();
      fetch(`/api/trace/unsubscribe/${sessionId}`, { method: 'POST' });
      setTurns([]);
    };
  }, [sessionId]);

  return turns;
}
