// TypeScript types mirroring docs/API.md — the frozen API contract.
// All field names are camelCase; timestamps are RFC 3339 strings; IDs are UUID v4 strings.

export interface Project {
  id: string;
  name: string;
  path: string;
  createdAt: string;
}

export type SessionStatus =
  | 'idle'
  | 'processing'
  | 'awaiting_input'
  | 'awaiting_approval'
  | 'done'
  | 'error';

export type PermissionMode = 'default' | 'acceptEdits' | 'plan' | 'auto';

export interface Session {
  id: string;
  projectId: string;
  claudeSessionId: string;
  title: string;
  status: SessionStatus;
  model: string;
  permissionMode: PermissionMode;
  createdAt: string;
  updatedAt: string;
}

export type MessageRole = 'user' | 'assistant' | 'thinking' | 'system' | 'tool';

// Attachment — an image pasted onto a user message. Bytes are fetched lazily
// from GET /api/attachments/{id}; this is metadata only.
export interface Attachment {
  id: string;
  messageId: string;
  sessionId: string;
  name: string;
  mediaType: string;
  createdAt: string;
}

export interface Message {
  id: string;
  sessionId: string;
  role: MessageRole;
  content: string;
  createdAt: string;
  attachments?: Attachment[];
}

export interface UsageRecord {
  id: string;
  sessionId: string;
  model: string;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  createdAt: string;
}

export interface SessionUsage {
  sessionId: string;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
}

export interface UsageSummary {
  totalInputTokens: number;
  totalOutputTokens: number;
  totalCostUsd: number;
  bySession: SessionUsage[];
}

export interface Health {
  status: string;
  version: string;
}

// PermissionRequest — emitted when a session pauses for a tool-permission decision.
export type PermissionDecision = 'allow' | 'allow_always' | 'deny';

export interface PermissionRequest {
  id: string;
  sessionId: string;
  toolName: string;
  input: Record<string, unknown>;
  summary: string;
  suggestions: string[];
  createdAt: string;
}

// SessionDelta — a streaming chunk for the in-progress assistant turn.
export type DeltaKind = 'text' | 'thinking' | 'tool';

export interface SessionDelta {
  sessionId: string;
  kind: DeltaKind;
  text: string;
}

export interface PermissionResolved {
  sessionId: string;
  requestId: string;
  decision: PermissionDecision;
}

// WebSocket events.
export type WsEventType =
  | 'session.status'
  | 'session.message'
  | 'session.delta'
  | 'session.permission'
  | 'session.permission_resolved'
  | 'session.usage'
  | 'project.created'
  | 'project.deleted';

interface WsEventBase<T extends WsEventType, P> {
  type: T;
  ts: string;
  payload: P;
}

export type WsEvent =
  | WsEventBase<'session.status', Session>
  | WsEventBase<'session.message', Message>
  | WsEventBase<'session.delta', SessionDelta>
  | WsEventBase<'session.permission', PermissionRequest>
  | WsEventBase<'session.permission_resolved', PermissionResolved>
  | WsEventBase<'session.usage', UsageRecord>
  | WsEventBase<'project.created', Project>
  | WsEventBase<'project.deleted', Project>;

// Request body types.
export interface CreateProjectInput {
  name: string;
  path: string;
}

export interface CreateSessionInput {
  title?: string;
  model?: string;
  permissionMode?: PermissionMode;
}

// ImageInput — a pasted image to send with a message. data is base64 (no data:
// URL prefix); mediaType is the image MIME type; name is the display label
// (image-1.png, …) used both in the transcript and in the reference text.
export interface ImageInput {
  name: string;
  mediaType: string;
  data: string;
}

export interface SendMessageInput {
  content: string;
  images?: ImageInput[];
}

export interface RespondPermissionInput {
  decision: PermissionDecision;
  message?: string;
}
