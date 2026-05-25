// useWebSocket — connects to /ws, parses WsEvent messages, auto-reconnects with
// exponential backoff, and exposes the latest event plus a subscribe callback.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { WsEvent, WsEventType } from '../types';

export type WsStatus = 'connecting' | 'open' | 'closed';

export type WsEventHandler = (event: WsEvent) => void;

const VALID_TYPES: ReadonlySet<WsEventType> = new Set<WsEventType>([
  'session.status',
  'session.message',
  'session.usage',
  'project.created',
  'project.deleted',
]);

/** Parse raw WebSocket message data into a WsEvent, or null if invalid. */
export function parseWsEvent(data: unknown): WsEvent | null {
  if (typeof data !== 'string') return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(data);
  } catch {
    return null;
  }
  if (
    !parsed ||
    typeof parsed !== 'object' ||
    !('type' in parsed) ||
    !('payload' in parsed)
  ) {
    return null;
  }
  const candidate = parsed as { type: unknown; payload: unknown };
  if (
    typeof candidate.type !== 'string' ||
    !VALID_TYPES.has(candidate.type as WsEventType)
  ) {
    return null;
  }
  return parsed as WsEvent;
}

/** Build the WebSocket URL from window.location (handles ws/wss). */
export function buildWsUrl(path = '/ws'): string {
  if (typeof window === 'undefined' || !window.location) {
    return `ws://localhost:8080${path}`;
  }
  const { protocol, host } = window.location;
  const wsProtocol = protocol === 'https:' ? 'wss:' : 'ws:';
  return `${wsProtocol}//${host}${path}`;
}

const MAX_BACKOFF_MS = 15000;
const BASE_BACKOFF_MS = 500;

export interface UseWebSocketResult {
  status: WsStatus;
  lastEvent: WsEvent | null;
  subscribe: (handler: WsEventHandler) => () => void;
}

export function useWebSocket(path = '/ws'): UseWebSocketResult {
  const [status, setStatus] = useState<WsStatus>('connecting');
  const [lastEvent, setLastEvent] = useState<WsEvent | null>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const handlersRef = useRef<Set<WsEventHandler>>(new Set());
  const reconnectAttemptsRef = useRef(0);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const closedByUserRef = useRef(false);

  const subscribe = useCallback((handler: WsEventHandler) => {
    handlersRef.current.add(handler);
    return () => {
      handlersRef.current.delete(handler);
    };
  }, []);

  useEffect(() => {
    closedByUserRef.current = false;

    const connect = () => {
      if (closedByUserRef.current) return;
      setStatus('connecting');

      let ws: WebSocket;
      try {
        ws = new WebSocket(buildWsUrl(path));
      } catch {
        scheduleReconnect();
        return;
      }
      wsRef.current = ws;

      ws.onopen = () => {
        reconnectAttemptsRef.current = 0;
        setStatus('open');
      };

      ws.onmessage = (ev: MessageEvent) => {
        const event = parseWsEvent(ev.data);
        if (!event) return;
        setLastEvent(event);
        handlersRef.current.forEach((h) => {
          try {
            h(event);
          } catch {
            // a misbehaving subscriber must not break the socket
          }
        });
      };

      ws.onerror = () => {
        // onclose will follow and handle reconnection
      };

      ws.onclose = () => {
        setStatus('closed');
        if (!closedByUserRef.current) {
          scheduleReconnect();
        }
      };
    };

    const scheduleReconnect = () => {
      if (closedByUserRef.current) return;
      const attempt = reconnectAttemptsRef.current++;
      const delay = Math.min(BASE_BACKOFF_MS * 2 ** attempt, MAX_BACKOFF_MS);
      reconnectTimerRef.current = setTimeout(connect, delay);
    };

    connect();

    return () => {
      closedByUserRef.current = true;
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current);
      }
      const ws = wsRef.current;
      if (ws) {
        ws.onopen = null;
        ws.onmessage = null;
        ws.onerror = null;
        ws.onclose = null;
        ws.close();
      }
      wsRef.current = null;
    };
  }, [path]);

  return { status, lastEvent, subscribe };
}
