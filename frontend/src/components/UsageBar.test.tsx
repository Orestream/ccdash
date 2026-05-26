import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { UsageBar } from './UsageBar';
import type { Utilization } from '../types';
import * as client from '../api/client';

// Minimal WebSocket stub so useWebSocket can run without a real socket.
class StubWebSocket {
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  onclose: ((ev: unknown) => void) | null = null;
  constructor() {
    setTimeout(() => this.onopen?.(undefined), 0);
  }
  close() {
    this.onclose?.(undefined);
  }
}

describe('UsageBar', () => {
  beforeEach(() => {
    vi.stubGlobal('WebSocket', StubWebSocket as unknown as typeof WebSocket);
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('renders session and weekly limit percentages', async () => {
    const util: Utilization = {
      session: { usedPercent: 3, resetsAt: '2099-01-01T00:00:00Z' },
      week: { usedPercent: 9, resetsAt: '2099-01-02T00:00:00Z' },
      fetchedAt: '2026-05-26T12:00:00Z',
    };
    vi.spyOn(client, 'getUsageLimits').mockResolvedValue(util);

    render(<UsageBar />);

    expect(await screen.findByText('Session')).toBeInTheDocument();
    expect(screen.getByText('Week')).toBeInTheDocument();
    expect(screen.getByText('3%')).toBeInTheDocument();
    expect(screen.getByText('9%')).toBeInTheDocument();
    // Opus window absent → not rendered.
    expect(screen.queryByText('Week · Opus')).not.toBeInTheDocument();
  });

  it('shows an error message when limits are unavailable', async () => {
    vi.spyOn(client, 'getUsageLimits').mockRejectedValue(
      new Error('token expired'),
    );

    render(<UsageBar />);

    await waitFor(() =>
      expect(screen.getByText('token expired')).toBeInTheDocument(),
    );
  });
});
