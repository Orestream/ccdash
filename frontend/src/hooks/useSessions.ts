// useSessions — loads sessions via REST and merges live session.status updates
// (and newly created/deleted projects' sessions) from the WebSocket.

import { useCallback, useEffect, useState } from 'react';
import { listProjectSessions, listSessions } from '../api/client';
import type { Session } from '../types';
import { useWebSocket, type WsEventHandler } from './useWebSocket';

export interface UseSessionsResult {
  sessions: Session[];
  loading: boolean;
  error: string | null;
  reload: () => void;
}

/**
 * Load sessions, optionally scoped to a project. Live `session.status` events
 * from the WebSocket are merged in (upserted by id, newest first).
 */
export function useSessions(projectId?: string): UseSessionsResult {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { subscribe } = useWebSocket();

  const load = useCallback(
    (signal?: AbortSignal) => {
      setLoading(true);
      setError(null);
      const fetcher = projectId
        ? listProjectSessions(projectId, signal)
        : listSessions(signal);
      fetcher
        .then((data) => {
          setSessions(data);
          setLoading(false);
        })
        .catch((err: unknown) => {
          if (signal?.aborted) return;
          setError(err instanceof Error ? err.message : 'failed to load sessions');
          setLoading(false);
        });
    },
    [projectId],
  );

  const reload = useCallback(() => load(), [load]);

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    return () => controller.abort();
  }, [load]);

  useEffect(() => {
    const handler: WsEventHandler = (event) => {
      if (event.type !== 'session.status') return;
      const updated = event.payload;
      // If scoped to a project, ignore updates for other projects.
      if (projectId && updated.projectId !== projectId) return;

      setSessions((prev) => {
        const idx = prev.findIndex((s) => s.id === updated.id);
        if (idx === -1) {
          return [updated, ...prev];
        }
        const next = prev.slice();
        next[idx] = { ...next[idx], ...updated };
        return next;
      });
    };
    return subscribe(handler);
  }, [subscribe, projectId]);

  return { sessions, loading, error, reload };
}
