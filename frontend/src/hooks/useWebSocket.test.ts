import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { buildWsUrl, parseWsEvent, useWebSocket } from './useWebSocket';
import type { Session } from '../types';

const sessionPayload: Session = {
  id: 's1',
  projectId: 'p1',
  claudeSessionId: '',
  title: 'Add auth',
  status: 'processing',
  model: 'claude-opus-4-7',
  permissionMode: 'default',
  worktreePath: '',
  branch: '',
  baseCommit: '',
  previewState: '',
  createdAt: '2026-05-25T12:00:00Z',
  updatedAt: '2026-05-25T12:01:00Z',
};

describe('parseWsEvent', () => {
  it('parses a valid session.status event', () => {
    const raw = JSON.stringify({
      type: 'session.status',
      ts: '2026-05-25T12:00:00Z',
      payload: sessionPayload,
    });
    const event = parseWsEvent(raw);
    expect(event).not.toBeNull();
    expect(event?.type).toBe('session.status');
    if (event?.type === 'session.status') {
      expect(event.payload.status).toBe('processing');
    }
  });

  it('rejects invalid JSON', () => {
    expect(parseWsEvent('not json')).toBeNull();
  });

  it('rejects unknown event types', () => {
    const raw = JSON.stringify({ type: 'bogus', ts: 'x', payload: {} });
    expect(parseWsEvent(raw)).toBeNull();
  });

  it('rejects objects missing payload', () => {
    const raw = JSON.stringify({ type: 'session.status', ts: 'x' });
    expect(parseWsEvent(raw)).toBeNull();
  });
});

describe('buildWsUrl', () => {
  it('derives a ws:// URL from the current location', () => {
    expect(buildWsUrl('/ws')).toMatch(/^ws:\/\/[^/]+\/ws$/);
  });
});

// --- Mock WebSocket ---
class MockWebSocket {
  static instances: MockWebSocket[] = [];
  static OPEN = 1;
  static CLOSED = 3;

  url: string;
  readyState = 0;
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  onclose: ((ev: unknown) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  open() {
    this.readyState = MockWebSocket.OPEN;
    this.onopen?.(undefined);
  }

  emit(data: string) {
    this.onmessage?.({ data } as MessageEvent);
  }

  close() {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.(undefined);
  }
}

describe('useWebSocket', () => {
  beforeEach(() => {
    MockWebSocket.instances = [];
    vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('parses an incoming event and notifies subscribers', async () => {
    const { result } = renderHook(() => useWebSocket('/ws'));

    expect(MockWebSocket.instances).toHaveLength(1);
    const socket = MockWebSocket.instances[0];

    const received: unknown[] = [];
    act(() => {
      result.current.subscribe((e) => received.push(e));
    });

    act(() => socket.open());
    await waitFor(() => expect(result.current.status).toBe('open'));

    act(() => {
      socket.emit(
        JSON.stringify({
          type: 'session.status',
          ts: '2026-05-25T12:00:00Z',
          payload: sessionPayload,
        }),
      );
    });

    await waitFor(() =>
      expect(result.current.lastEvent?.type).toBe('session.status'),
    );
    expect(received).toHaveLength(1);
    if (result.current.lastEvent?.type === 'session.status') {
      expect(result.current.lastEvent.payload.id).toBe('s1');
    }
  });

  it('ignores malformed messages', async () => {
    const { result } = renderHook(() => useWebSocket('/ws'));
    const socket = MockWebSocket.instances[0];
    act(() => socket.open());

    act(() => socket.emit('garbage'));
    expect(result.current.lastEvent).toBeNull();
  });
});
