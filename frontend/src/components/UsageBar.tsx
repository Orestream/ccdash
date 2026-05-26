// UsageBar — Claude subscription rate-limit usage (the CLI's /usage view):
// session + weekly limit windows, polled and refreshed as sessions consume usage.

import { useCallback, useEffect, useState } from 'react';
import { getUsageLimits } from '../api/client';
import type { Utilization, UsageWindow } from '../types';
import { useWebSocket } from '../hooks/useWebSocket';

const POLL_MS = 60_000;

// Relative reset label, e.g. "resets 3h" / "resets 2d".
function formatReset(iso?: string): string {
  if (!iso) return '';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '';
  const ms = t - Date.now();
  if (ms <= 0) return 'resets now';
  const mins = Math.round(ms / 60_000);
  if (mins < 60) return `resets ${mins}m`;
  const hrs = Math.round(mins / 60);
  if (hrs < 48) return `resets ${hrs}h`;
  return `resets ${Math.round(hrs / 24)}d`;
}

function Meter({ label, window }: { label: string; window: UsageWindow }) {
  const pct = Math.max(0, Math.min(100, window.usedPercent));
  const reset = formatReset(window.resetsAt);
  const title = window.resetsAt
    ? `${pct.toFixed(1)}% used · resets ${new Date(window.resetsAt).toLocaleString()}`
    : `${pct.toFixed(1)}% used`;
  return (
    <span className="usage-meter" title={title} data-testid="usage-meter">
      <span className="usage-meter-head">
        <span className="usage-label">{label}</span>
        <span className="usage-value">{pct.toFixed(0)}%</span>
      </span>
      <span className="usage-track">
        <span
          className="usage-fill"
          style={{ width: `${pct}%` }}
          data-warn={pct >= 80}
        />
      </span>
      {reset && <span className="usage-reset muted">{reset}</span>}
    </span>
  );
}

export function UsageBar() {
  const [util, setUtil] = useState<Utilization | null>(null);
  const [error, setError] = useState<string | null>(null);
  const { subscribe, status } = useWebSocket();

  const load = useCallback((signal?: AbortSignal) => {
    getUsageLimits(signal)
      .then((data) => {
        setUtil(data);
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
    const id = window.setInterval(() => load(), POLL_MS);
    return () => {
      controller.abort();
      window.clearInterval(id);
    };
  }, [load]);

  // A running session consumes the subscription limits — refresh when it reports.
  useEffect(() => {
    return subscribe((event) => {
      if (event.type === 'session.usage') {
        load();
      }
    });
  }, [subscribe, load]);

  const hasWindows = util && (util.session || util.week || util.weekOpus);

  return (
    <header className="usage-bar">
      <div className="usage-title">Usage</div>
      <div className="usage-stats">
        {error && <span className="muted">{error}</span>}
        {!error && util && (
          <>
            {util.session && <Meter label="Session" window={util.session} />}
            {util.week && <Meter label="Week" window={util.week} />}
            {util.weekOpus && <Meter label="Week · Opus" window={util.weekOpus} />}
            {!hasWindows && <span className="muted">No limit data</span>}
          </>
        )}
        {!error && !util && <span className="muted">Loading…</span>}
      </div>
      <div className={`ws-indicator ws-${status}`} title={`WebSocket: ${status}`}>
        <span className="ws-dot" /> {status}
      </div>
    </header>
  );
}

export default UsageBar;
