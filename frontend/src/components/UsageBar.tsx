// UsageBar — total tokens + cost from getUsageSummary, refreshed on usage events.

import { useCallback, useEffect, useState } from 'react';
import { getUsageSummary } from '../api/client';
import type { UsageSummary } from '../types';
import { useWebSocket } from '../hooks/useWebSocket';

function formatTokens(n: number): string {
  return n.toLocaleString('en-US');
}

function formatCost(n: number): string {
  return `$${n.toFixed(4)}`;
}

export function UsageBar() {
  const [summary, setSummary] = useState<UsageSummary | null>(null);
  const [error, setError] = useState<string | null>(null);
  const { subscribe, status } = useWebSocket();

  const load = useCallback((signal?: AbortSignal) => {
    getUsageSummary(signal)
      .then((data) => {
        setSummary(data);
        setError(null);
      })
      .catch((err: unknown) => {
        if (signal?.aborted) return;
        setError(err instanceof Error ? err.message : 'usage unavailable');
      });
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    return () => controller.abort();
  }, [load]);

  // Refresh totals whenever a usage event arrives.
  useEffect(() => {
    return subscribe((event) => {
      if (event.type === 'session.usage') {
        load();
      }
    });
  }, [subscribe, load]);

  return (
    <header className="usage-bar">
      <div className="usage-title">Usage</div>
      <div className="usage-stats">
        {error && <span className="muted">{error}</span>}
        {!error && summary && (
          <>
            <span className="usage-stat">
              <span className="usage-label">Input</span>
              <span className="usage-value">
                {formatTokens(summary.totalInputTokens)}
              </span>
            </span>
            <span className="usage-stat">
              <span className="usage-label">Output</span>
              <span className="usage-value">
                {formatTokens(summary.totalOutputTokens)}
              </span>
            </span>
            <span className="usage-stat">
              <span className="usage-label">Cost</span>
              <span className="usage-value">{formatCost(summary.totalCostUsd)}</span>
            </span>
          </>
        )}
        {!error && !summary && <span className="muted">Loading…</span>}
      </div>
      <div className={`ws-indicator ws-${status}`} title={`WebSocket: ${status}`}>
        <span className="ws-dot" /> {status}
      </div>
    </header>
  );
}

export default UsageBar;
