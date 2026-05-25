// SessionView — message transcript + prompt input + Stop button.
// Live messages and status arrive over the WebSocket; sending/stopping use REST.

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  ApiError,
  getSession,
  listMessages,
  sendMessage,
  stopSession,
} from '../api/client';
import type { Message, Session } from '../types';
import { useWebSocket } from '../hooks/useWebSocket';
import { StatusBadge } from './StatusBadge';

export interface SessionViewProps {
  sessionId: string;
}

export function SessionView({ sessionId }: SessionViewProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const { subscribe } = useWebSocket();
  const transcriptRef = useRef<HTMLDivElement | null>(null);

  // Initial load (session + messages).
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setSession(null);
    setMessages([]);
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
  }, [sessionId]);

  // Live updates.
  useEffect(() => {
    return subscribe((event) => {
      if (event.type === 'session.status' && event.payload.id === sessionId) {
        setSession((prev) => ({ ...prev, ...event.payload }));
      } else if (
        event.type === 'session.message' &&
        event.payload.sessionId === sessionId
      ) {
        const msg = event.payload;
        setMessages((prev) => {
          const idx = prev.findIndex((m) => m.id === msg.id);
          if (idx === -1) return [...prev, msg];
          // streamed delta: append to existing content
          const next = prev.slice();
          next[idx] = { ...next[idx], content: next[idx].content + msg.content };
          return next;
        });
      }
    });
  }, [subscribe, sessionId]);

  // Autoscroll on new messages.
  useEffect(() => {
    const el = transcriptRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages]);

  const handleSend = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault();
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
    },
    [draft, sending, sessionId],
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

  if (loading) {
    return (
      <section className="session-view">
        <p className="muted">Loading session…</p>
      </section>
    );
  }

  if (error) {
    return (
      <section className="session-view">
        <p className="error" role="alert">
          {error}
        </p>
      </section>
    );
  }

  const isProcessing = session?.status === 'processing';

  return (
    <section className="session-view">
      <header className="panel-header">
        <div>
          <h1>{session?.title || 'Session'}</h1>
          {session && <p className="muted">{session.model}</p>}
        </div>
        <div className="session-actions">
          {session && <StatusBadge status={session.status} />}
          <button onClick={handleStop} disabled={!isProcessing} className="danger">
            Stop
          </button>
        </div>
      </header>

      {actionError && (
        <p className="error" role="alert">
          {actionError}
        </p>
      )}

      <div className="transcript" ref={transcriptRef}>
        {messages.length === 0 && <p className="muted">No messages yet.</p>}
        {messages.map((m) => (
          <div key={m.id} className={`message message-${m.role}`}>
            <span className="message-role">{m.role}</span>
            <div className="message-content">{m.content}</div>
          </div>
        ))}
      </div>

      <form className="prompt" onSubmit={handleSend}>
        <textarea
          aria-label="Prompt"
          placeholder="Send a message…"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          rows={3}
        />
        <button type="submit" disabled={sending || !draft.trim()}>
          {sending ? 'Sending…' : 'Send'}
        </button>
      </form>
    </section>
  );
}

export default SessionView;
