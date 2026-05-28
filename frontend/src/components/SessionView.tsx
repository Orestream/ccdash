// SessionView — message transcript + live streaming + approval menu + composer.
// Live messages, deltas, status and permission events arrive over the WebSocket;
// sending/stopping/mode-changes/decisions use REST.

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  ApiError,
  attachmentUrl,
  getSession,
  listMessages,
  listPermissions,
  renameSession,
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
import { parseToolContent } from './toolContent';
import {
  imagesFromClipboard,
  renumber,
  toImageInput,
  type PendingImage,
} from './pastedImages';

export interface SessionViewProps {
  sessionId: string;
}

const DELTA_KIND_LABEL: Record<string, string> = {
  thinking: 'thinking',
  tool: 'tool',
  text: 'assistant',
};

// How close to the bottom (px) still counts as "stuck to the bottom".
const SCROLL_SNAP_THRESHOLD = 48;

// Per-session composer drafts. Module-level so switching chats — or navigating
// away and back to a chat — restores its in-flight text. Lives only for the
// page's lifetime; reloads start fresh.
const draftCache = new Map<string, string>();

export function SessionView({ sessionId }: SessionViewProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState<string>(() => draftCache.get(sessionId) ?? '');
  const draftRef = useRef(draft);
  const prevSessionIdRef = useRef(sessionId);
  const [pendingImages, setPendingImages] = useState<PendingImage[]>([]);
  const [sending, setSending] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [permissions, setPermissions] = useState<PermissionRequest[]>([]);
  const [decidingId, setDecidingId] = useState<string | null>(null);
  const [modeBusy, setModeBusy] = useState(false);
  const [editingTitle, setEditingTitle] = useState(false);
  const [titleDraft, setTitleDraft] = useState('');
  const [renaming, setRenaming] = useState(false);
  const { subscribe, status: wsStatus } = useWebSocket();
  const { segments, pushDelta, reset: resetStream } = useSessionStream(sessionId);
  const transcriptRef = useRef<HTMLDivElement | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  // Whether the transcript is pinned to the bottom (auto-follow new messages).
  // Mirrored in a ref so the message-arrival effect reads the latest value
  // without re-subscribing.
  const atBottomRef = useRef(true);
  const [showScrollButton, setShowScrollButton] = useState(false);
  // Guards the title editor's exit so Enter/Escape and the unmount blur don't
  // double-fire (see finishEditTitle).
  const editingRef = useRef(false);

  // Mirror the live draft into a ref so the session-switch effect can persist
  // the latest value without re-running on every keystroke.
  useEffect(() => {
    draftRef.current = draft;
  }, [draft]);

  // On sessionId change, stash the outgoing chat's draft and load the incoming
  // chat's draft. The lazy initializer above handles the first render.
  useEffect(() => {
    const prev = prevSessionIdRef.current;
    if (prev === sessionId) return;
    if (draftRef.current) draftCache.set(prev, draftRef.current);
    else draftCache.delete(prev);
    const next = draftCache.get(sessionId) ?? '';
    setDraft(next);
    draftRef.current = next;
    prevSessionIdRef.current = sessionId;
  }, [sessionId]);

  // Save the draft when the view unmounts (e.g. navigating to the overview)
  // so it's still there if the user returns to this chat.
  useEffect(() => {
    return () => {
      const id = prevSessionIdRef.current;
      if (draftRef.current) draftCache.set(id, draftRef.current);
      else draftCache.delete(id);
    };
  }, []);

  // Initial load (session + messages). Permissions are loaded by the recovery
  // effect below (also runs on WS reconnect).
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setSession(null);
    setMessages([]);
    setPermissions([]);
    setPendingImages([]);
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

  const scrollToBottom = useCallback(() => {
    const el = transcriptRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    atBottomRef.current = true;
    setShowScrollButton(false);
  }, []);

  // Release / re-engage the bottom snap as the user scrolls.
  const handleScroll = useCallback(() => {
    const el = transcriptRef.current;
    if (!el) return;
    const atBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight <= SCROLL_SNAP_THRESHOLD;
    atBottomRef.current = atBottom;
    setShowScrollButton(!atBottom);
  }, []);

  // Follow new messages / live segments only while snapped to the bottom; when
  // the user has scrolled up, surface the jump-to-bottom button instead.
  useEffect(() => {
    if (atBottomRef.current) {
      scrollToBottom();
    } else {
      setShowScrollButton(true);
    }
  }, [messages, segments, scrollToBottom]);

  // Grow the composer from a single line as text wraps (capped via CSS max-height).
  // With border-box, scrollHeight omits the border, so add it back to avoid a
  // perpetual 1px scrollbar.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    const border = el.offsetHeight - el.clientHeight;
    el.style.height = `${el.scrollHeight + border}px`;
  }, [draft]);

  const submitDraft = useCallback(async () => {
    const content = draft.trim();
    // A message may be text-only, images-only, or both.
    if ((!content && pendingImages.length === 0) || sending) return;
    setSending(true);
    setActionError(null);
    try {
      const images =
        pendingImages.length > 0 ? toImageInput(pendingImages) : undefined;
      const created = await sendMessage(sessionId, { content, images });
      setMessages((prev) =>
        prev.some((m) => m.id === created.id) ? prev : [...prev, created],
      );
      setDraft('');
      setPendingImages([]);
      // Sending always re-engages the bottom snap so the user sees their turn.
      atBottomRef.current = true;
    } catch (err) {
      const msg =
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'failed to send message';
      setActionError(msg);
    } finally {
      setSending(false);
    }
  }, [draft, pendingImages, sending, sessionId]);

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

  // Ctrl/Cmd+V of an image (or screenshot) attaches it as image-N.<ext>.
  const handlePaste = useCallback((e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const files = imagesFromClipboard(e.clipboardData?.items);
    if (files.length === 0) return;
    e.preventDefault(); // keep the binary out of the text field
    files.forEach((file) => {
      const reader = new FileReader();
      reader.onload = () => {
        const dataUrl = reader.result as string;
        setPendingImages((prev) =>
          renumber([...prev, { mediaType: file.type, dataUrl }]),
        );
      };
      reader.readAsDataURL(file);
    });
  }, []);

  const removePendingImage = useCallback((index: number) => {
    setPendingImages((prev) => renumber(prev.filter((_, i) => i !== index)));
  }, []);

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

  const beginEditTitle = useCallback(() => {
    editingRef.current = true;
    setTitleDraft(session?.title ?? '');
    setEditingTitle(true);
  }, [session?.title]);

  // Single exit path for the editor. The ref guard makes Enter/Escape and the
  // follow-up blur (fired as the input unmounts) idempotent: only the first
  // call wins, so we never double-save or save a cancelled edit.
  const finishEditTitle = useCallback(
    async (commit: boolean) => {
      if (!editingRef.current) return;
      editingRef.current = false;
      setEditingTitle(false);
      const next = titleDraft.trim();
      if (!commit || !next || next === (session?.title ?? '')) return;
      setRenaming(true);
      setActionError(null);
      try {
        const updated = await renameSession(sessionId, next);
        setSession((prev) => ({ ...prev, ...updated }));
      } catch (err) {
        const msg =
          err instanceof ApiError || err instanceof Error
            ? err.message
            : 'failed to rename session';
        setActionError(msg);
      } finally {
        setRenaming(false);
      }
    },
    [titleDraft, session?.title, sessionId],
  );

  const handleTitleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLInputElement>) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        void finishEditTitle(true);
      } else if (e.key === 'Escape') {
        e.preventDefault();
        void finishEditTitle(false);
      }
    },
    [finishEditTitle],
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
          {editingTitle ? (
            <input
              className="title-edit"
              aria-label="Session title"
              value={titleDraft}
              autoFocus
              disabled={renaming}
              onChange={(e) => setTitleDraft(e.target.value)}
              onKeyDown={handleTitleKeyDown}
              onBlur={() => void finishEditTitle(true)}
            />
          ) : (
            <h1
              className="session-heading"
              title="Click to rename"
              onClick={beginEditTitle}
            >
              {session?.title || 'Session'}
            </h1>
          )}
          {session && <p className="muted">{session.model}</p>}
          {session?.branch && (
            <button
              type="button"
              className="branch-badge"
              title={`Worktree: ${session.worktreePath}\nClick to copy path`}
              onClick={() => {
                if (session.worktreePath) {
                  void navigator.clipboard?.writeText(session.worktreePath);
                }
              }}
            >
              {session.branch}
            </button>
          )}
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

      <div className="transcript-wrap">
        <div className="transcript" ref={transcriptRef} onScroll={handleScroll}>
          {messages.length === 0 && segments.length === 0 && (
            <p className="muted">No messages yet.</p>
          )}
          {messages.map((m) =>
            m.role === 'tool' ? (
              <ToolMessage key={m.id} content={m.content} />
            ) : (
              <div key={m.id} className={`message message-${m.role}`}>
                <span className="message-role">{m.role}</span>
                {m.content && <div className="message-content">{m.content}</div>}
                {m.attachments && m.attachments.length > 0 && (
                  <div className="message-attachments">
                    {m.attachments.map((a) => (
                      <a
                        key={a.id}
                        href={attachmentUrl(a.id)}
                        target="_blank"
                        rel="noreferrer"
                        title={a.name}
                      >
                        <img src={attachmentUrl(a.id)} alt={a.name} />
                      </a>
                    ))}
                  </div>
                )}
              </div>
            ),
          )}
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
        {showScrollButton && (
          <button
            type="button"
            className="scroll-to-bottom"
            aria-label="Scroll to latest"
            title="Scroll to latest"
            onClick={scrollToBottom}
          >
            ↓
          </button>
        )}
      </div>

      <ApprovalMenu
        requests={permissions}
        onDecide={(id, decision) => void handleDecide(id, decision)}
        pendingId={decidingId}
      />

      {pendingImages.length > 0 && (
        <div className="pending-images" data-testid="pending-images">
          {pendingImages.map((img, i) => (
            <div key={i} className="pending-image">
              <img src={img.dataUrl} alt={img.name} />
              <span className="pending-image-name">{img.name}</span>
              <button
                type="button"
                className="pending-image-remove"
                aria-label={`Remove ${img.name}`}
                onClick={() => removePendingImage(i)}
              >
                ×
              </button>
            </div>
          ))}
        </div>
      )}

      <form className="prompt" onSubmit={handleSend}>
        <textarea
          ref={textareaRef}
          aria-label="Prompt"
          placeholder="Send a message…  (paste an image to attach)"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          rows={1}
        />
        <button
          type="submit"
          disabled={sending || (!draft.trim() && pendingImages.length === 0)}
        >
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

// ToolMessage renders a `tool` transcript entry as "<Tool> <detail>", where the
// detail (file basename or command) sits to the right in a smaller mono font.
function ToolMessage({ content }: { content: string }) {
  const { name, detail, full } = parseToolContent(content);
  return (
    <div className="message message-tool">
      <div className="tool-header">
        <span className="message-role tool-name">{name}</span>
        {detail && (
          <span className="tool-detail" title={full}>
            {detail}
          </span>
        )}
      </div>
    </div>
  );
}

export default SessionView;
