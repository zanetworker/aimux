import { useEffect, useState } from 'react';
import type { Turn } from '../types';

export function useTraceStream(sessionId: string | null, sessionFile?: string): Turn[] {
  const [turns, setTurns] = useState<Turn[]>([]);

  useEffect(() => {
    if (!sessionId) {
      setTurns([]);
      return;
    }

    let cancelled = false;

    async function fetchTrace() {
      try {
        const url = sessionFile
          ? `/api/trace?file=${encodeURIComponent(sessionFile)}`
          : `/api/agents/${sessionId}/trace`;
        const resp = await fetch(url);
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
    const interval = setInterval(fetchTrace, 5000);

    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, [sessionId, sessionFile]);

  return turns;
}
