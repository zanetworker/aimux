import { useEffect, useState } from 'react';
import type { Turn } from '../types';

export function useTraceStream(sessionId: string | null): Turn[] {
  const [turns, setTurns] = useState<Turn[]>([]);

  useEffect(() => {
    if (!sessionId) {
      setTurns([]);
      return;
    }

    let cancelled = false;

    async function fetchTrace() {
      try {
        const resp = await fetch(`/api/agents/${sessionId}/trace`);
        if (!resp.ok) return;
        const data = await resp.json();
        if (!cancelled && data.turns) {
          setTurns(data.turns.map((t: any) => ({
            number: t.number,
            timestamp: t.timestamp,
            userText: t.userText,
            outputText: t.outputText,
            actions: (t.actions || []).map((a: any) => ({
              name: a.name,
              snippet: a.snippet,
              success: a.success === 'true',
              errorMsg: a.errorMsg,
            })),
            tokensIn: t.tokensIn,
            tokensOut: t.tokensOut,
            costUSD: t.costUSD,
            model: t.model,
          })));
        }
      } catch {
        // ignore
      }
    }

    fetchTrace();
    // Poll every 5 seconds for updates
    const interval = setInterval(fetchTrace, 5000);

    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, [sessionId]);

  return turns;
}
