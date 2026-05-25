// SessionView — message transcript + live streaming + approval menu + composer.
// Live messages, deltas, status and permission events arrive over the WebSocket;
// sending/stopping/mode-changes/decisions use REST.

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  ApiError,
  getSession,
  listMessages,
  listPermissions,
  respondPermission,
  sendMessage,
  setSessionMode,
  stopSession,
} from '../api/client';
import type {
  Message,
  PermissionDecision,
  PermissionMode,
  PermissionRequest,
  Session,
} from '../types';
import { useWebSocket } from '../hooks/useWebSocket';
import { useSessionStream } from '../hooks/useSessionStream';
import { StatusBadge } from './StatusBadge';
import { ModeSelector } from './ModeSelector';
import { ApprovalMenu } from './ApprovalMenu';

export interface SessionViewProps {
  sessionId: string;
}

const DELTA_KIND_LABEL: Record<string, string> = {
  thinking: 'thinking',
  tool: 'tool',
  text: 'assistant',
};

export function SessionView({ sessionId }: SessionViewProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [permissions, setPermissions] = useState<PermissionRequest[]>([]);
  const [decidingId, setDecidingId] = useState<string | null>(null);
  const [modeBusy, setModeBusy] = useState(false);
  const { subscribe, status: wsStatus } = useWebSocket();
  const { segments, pushDelta, reset: resetStream } = useSessionStream(sessionId);
  const transcriptRef = useRef<HTMLDivElement | null>(null);

  // Initial load (session + messages). Permissions are loaded by the recovery
  // effect below (also runs on WS reconnect).
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setSession(null);
    setMessages([]);
    setPermissions([]);
    resetStream();
    Promise.all([
      getSession(sessionId, controller.signal),
      listMessages(sessionId, controller.signal),
    ])
      .then(([s, msgs]) => {
        setSession(s);
        setMessages(msgs);
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        setError(err instanceof Error ? err.message : 'failed to load session');
        setLoading(false);
      });
    return () => controller.abort();
  }, [sessionId, resetStream]);

  // Recover pending permission requests on mount and whenever the WS (re)connects.
  useEffect(() => {
    if (wsStatus !== 'open') return;
    const controller = new AbortController();
    listPermissions(sessionId, controller.signal)
      .then((reqs) => setPermissions(reqs))
      .catch(() => {
        // best-effort recovery; ignore failures
      });
    return () => controller.abort();
  }, [sessionId, wsStatus]);

  // Live updates.
  useEffect(() => {
    return subscribe((event) => {
      switch (event.type) {
        case 'session.status': {
          if (event.payload.id !== sessionId) return;
          const next = event.payload;
          setSession((prev) => ({ ...prev, ...next }));
          // Turn ended: clear the live accumulator.
          if (next.status !== 'processing') {
            resetStream();
          }
          break;
        }
        case 'session.delta': {
          if (event.payload.sessionId !== sessionId) return;
          pushDelta(event.payload);
          break;
        }
        case 'session.message': {
          if (event.payload.sessionId !== sessionId) return;
          const msg = event.payload;
          // A finalized turn replaces the live bubble.
          if (msg.role === 'assistant' || msg.role === 'thinking' || msg.role === 'tool') {
            resetStream();
          }
          setMessages((prev) =>
            prev.some((m) => m.id === msg.id) ? prev : [...prev, msg],
          );
          break;
        }
        case 'session.permission': {
          if (event.payload.sessionId !== sessionId) return;
          const req = event.payload;
          setPermissions((prev) =>
            prev.some((p) => p.id === req.id) ? prev : [...prev, req],
          );
          break;
        }
        case 'session.permission_resolved': {
          if (event.payload.sessionId !== sessionId) return;
          const { requestId } = event.payload;
          setPermissions((prev) => prev.filter((p) => p.id !== requestId));
          break;
        }
        default:
          break;
      }
    });
  }, [subscribe, sessionId, pushDelta, resetStream]);

  // Autoscroll on new messages / live segments.
  useEffect(() => {
    const el = transcriptRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, segments]);

  const submitDraft = useCallback(async () => {
    const content = draft.trim();
    if (!content || sending) return;
    setSending(true);
    setActionError(null);
    try {
      const created = await sendMessage(sessionId, { content });
      setMessages((prev) =>
        prev.some((m) => m.id === created.id) ? prev : [...prev, created],
      );
      setDraft('');
    } catch (err) {
      const msg =
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'failed to send message';
      setActionError(msg);
    } finally {
      setSending(false);
    }
  }, [draft, sending, sessionId]);

  const handleSend = useCallback(
    (e: React.FormEvent) => {
      e.preventDefault();
      void submitDraft();
    },
    [submitDraft],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      // Enter submits; Shift+Enter inserts a newline.
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        void submitDraft();
      }
    },
    [submitDraft],
  );

  const handleStop = useCallback(async () => {
    setActionError(null);
    try {
      const updated = await stopSession(sessionId);
      setSession((prev) => ({ ...prev, ...updated }));
    } catch (err) {
      const msg =
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'failed to stop session';
      setActionError(msg);
    }
  }, [sessionId]);

  const handleModeChange = useCallback(
    async (mode: PermissionMode) => {
      setActionError(null);
      setModeBusy(true);
      try {
        const updated = await setSessionMode(sessionId, mode);
        setSession((prev) => ({ ...prev, ...updated }));
      } catch (err) {
        const msg =
          err instanceof ApiError || err instanceof Error
            ? err.message
            : 'failed to change mode';
        setActionError(msg);
      } finally {
        setModeBusy(false);
      }
    },
    [sessionId],
  );

  const handleDecide = useCallback(
    async (requestId: string, decision: PermissionDecision) => {
      setActionError(null);
      setDecidingId(requestId);
      try {
        await respondPermission(sessionId, requestId, { decision });
        // Optimistically remove on success; the resolved event will also drop it.
        setPermissions((prev) => prev.filter((p) => p.id !== requestId));
      } catch (err) {
        const msg =
          err instanceof ApiError || err instanceof Error
            ? err.message
            : 'failed to respond to permission request';
        setActionError(msg);
      } finally {
        setDecidingId(null);
      }
    },
    [sessionId],
  );

  if (loading) {
    return (
      <section className="session-view">
        <p className="muted">Loading session…</p>
      </section>
    );
  }

  // Note: a load error no longer blocks the composer — it is surfaced as a banner
  // while the rest of the view (including the always-typeable composer) renders.
  const isProcessing = session?.status === 'processing';
  const isAwaitingApproval = session?.status === 'awaiting_approval';
  const isBusy = isProcessing || isAwaitingApproval;

  return (
    <section className="session-view">
      <header className="panel-header">
        <div>
          <h1>{session?.title || 'Session'}</h1>
          {session && <p className="muted">{session.model}</p>}
        </div>
        <div className="session-actions">
          {session && (
            <ModeSelector
              mode={session.permissionMode}
              onChange={(m) => void handleModeChange(m)}
              disabled={modeBusy}
            />
          )}
          {session && <StatusBadge status={session.status} />}
          <button onClick={handleStop} disabled={!isProcessing} className="danger">
            Stop
          </button>
        </div>
      </header>

      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {actionError && (
        <p className="error" role="alert">
          {actionError}
        </p>
      )}

      <div className="transcript" ref={transcriptRef}>
        {messages.length === 0 && segments.length === 0 && (
          <p className="muted">No messages yet.</p>
        )}
        {messages.map((m) => (
          <div key={m.id} className={`message message-${m.role}`}>
            <span className="message-role">{m.role}</span>
            <div className="message-content">{m.content}</div>
          </div>
        ))}
        {isProcessing && segments.length > 0 && (
          <div className="message message-live" data-testid="live-bubble">
            <span className="message-role">assistant</span>
            {segments.map((seg, i) => (
              <div
                key={i}
                className={`live-segment live-${seg.kind}`}
                data-kind={seg.kind}
                aria-label={DELTA_KIND_LABEL[seg.kind] ?? seg.kind}
              >
                {seg.kind === 'tool' ? `⚙ ${seg.text}` : seg.text}
              </div>
            ))}
          </div>
        )}
      </div>

      <ApprovalMenu
        requests={permissions}
        onDecide={(id, decision) => void handleDecide(id, decision)}
        pendingId={decidingId}
      />

      <form className="prompt" onSubmit={handleSend}>
        <textarea
          aria-label="Prompt"
          placeholder="Send a message…"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={handleKeyDown}
          rows={3}
        />
        <button type="submit" disabled={sending || !draft.trim()}>
          {sending ? 'Sending…' : 'Send'}
        </button>
      </form>
      {isBusy && (
        <p className="composer-hint muted">
          {isAwaitingApproval
            ? 'Awaiting approval — your message will be queued for the next turn.'
            : 'Claude is working — your message will be queued for the next turn.'}
        </p>
      )}
    </section>
  );
}

export default SessionView;
