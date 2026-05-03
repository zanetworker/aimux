import { useEffect, useState, useRef } from 'react';
import type { Agent } from '../types';

export function useAgentStream(): Agent[] {
  const [agents, setAgents] = useState<Agent[]>([]);
  const esRef = useRef<EventSource | null>(null);

  useEffect(() => {
    const es = new EventSource('/api/events');
    esRef.current = es;

    es.addEventListener('agents', (e) => {
      try {
        const data = JSON.parse(e.data);
        setAgents(data.agents || []);
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

  return agents;
}
