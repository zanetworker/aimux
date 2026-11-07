import { useEffect, useState, useRef } from 'react';
import type { Agent } from '../types';

// hooksHint is the server's muted line while the permission hooks are missing ("" once installed).
export function useAgentStream(): { agents: Agent[]; loading: boolean; hooksHint: string } {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [hooksHint, setHooksHint] = useState('');
  const [loading, setLoading] = useState(true);
  const esRef = useRef<EventSource | null>(null);

  useEffect(() => {
    const es = new EventSource('/api/events');
    esRef.current = es;

    es.addEventListener('agents', (e) => {
      try {
        const data = JSON.parse(e.data);
        setAgents(data.agents || []);
        setHooksHint(typeof data.hooksHint === 'string' ? data.hooksHint : '');
        setLoading(false);
      } catch {
        // ignore parse errors
      }
    });

    es.onerror = () => {
      es.close();
      setTimeout(() => {
        esRef.current = new EventSource('/api/events');
      }, 3000);
    };

    return () => es.close();
  }, []);

  return { agents, loading, hooksHint };
}
