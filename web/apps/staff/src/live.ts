import { useEffect } from 'react';
import { API_BASE } from '@oneclub/api-client';
import { useAuth } from '@oneclub/shell';

/** Re-renders when the server pushes one of the topics on an SSE stream
 * (Technical Doc §3.4); the hub names each SSE event after its topic. */
export function useLive(path: string, topics: string[], onEvent: () => void) {
  const { propertyId } = useAuth();
  const key = topics.join(',');
  useEffect(() => {
    if (!propertyId || typeof EventSource === 'undefined') return;
    const es = new EventSource(`${API_BASE}${path}?propertyId=${propertyId}`, { withCredentials: true });
    const h = () => onEvent();
    for (const t of key.split(',')) es.addEventListener(t, h);
    return () => es.close();
  }, [path, key, propertyId, onEvent]);
}

/** P1's golf stream carries every golf.* topic, P2's included. */
export const GOLF_STREAM = '/api/v1/golf/tee-sheet/stream';
