// Typed fetch wrapper for the ccdash REST API (served under /api).
// Uses relative URLs so the Vite dev-server proxy can forward to the Go backend.

import type {
  CreateProjectInput,
  CreateSessionInput,
  Health,
  Message,
  PermissionMode,
  PermissionRequest,
  Project,
  RespondPermissionInput,
  SendMessageInput,
  Session,
  UsageRecord,
  UsageSummary,
} from '../types';

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

const BASE = '/api';

interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal } = opts;

  let res: Response;
  try {
    res = await fetch(`${BASE}${path}`, {
      method,
      signal,
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (err) {
    // Network failure (backend absent, etc.) — surface as an ApiError.
    const message = err instanceof Error ? err.message : 'network error';
    throw new ApiError(0, message);
  }

  if (!res.ok) {
    let message = `request failed with status ${res.status}`;
    try {
      const data = (await res.json()) as { error?: string };
      if (data && typeof data.error === 'string') {
        message = data.error;
      }
    } catch {
      // ignore non-JSON error bodies
    }
    throw new ApiError(res.status, message);
  }

  if (res.status === 204) {
    return undefined as T;
  }

  return (await res.json()) as T;
}

// --- Health ---
export function getHealth(signal?: AbortSignal): Promise<Health> {
  return request<Health>('/health', { signal });
}

// --- Projects ---
export function listProjects(signal?: AbortSignal): Promise<Project[]> {
  return request<Project[]>('/projects', { signal });
}

export function createProject(input: CreateProjectInput): Promise<Project> {
  return request<Project>('/projects', { method: 'POST', body: input });
}

export function getProject(id: string, signal?: AbortSignal): Promise<Project> {
  return request<Project>(`/projects/${encodeURIComponent(id)}`, { signal });
}

export function deleteProject(id: string): Promise<void> {
  return request<void>(`/projects/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

// --- Sessions ---
export function listProjectSessions(
  projectId: string,
  signal?: AbortSignal,
): Promise<Session[]> {
  return request<Session[]>(
    `/projects/${encodeURIComponent(projectId)}/sessions`,
    { signal },
  );
}

export function createSession(
  projectId: string,
  input: CreateSessionInput = {},
): Promise<Session> {
  return request<Session>(`/projects/${encodeURIComponent(projectId)}/sessions`, {
    method: 'POST',
    body: input,
  });
}

export function listSessions(signal?: AbortSignal): Promise<Session[]> {
  return request<Session[]>('/sessions', { signal });
}

export function getSession(id: string, signal?: AbortSignal): Promise<Session> {
  return request<Session>(`/sessions/${encodeURIComponent(id)}`, { signal });
}

export function listMessages(
  sessionId: string,
  signal?: AbortSignal,
): Promise<Message[]> {
  return request<Message[]>(
    `/sessions/${encodeURIComponent(sessionId)}/messages`,
    { signal },
  );
}

export function sendMessage(
  sessionId: string,
  input: SendMessageInput,
): Promise<Message> {
  return request<Message>(`/sessions/${encodeURIComponent(sessionId)}/messages`, {
    method: 'POST',
    body: input,
  });
}

export function stopSession(sessionId: string): Promise<Session> {
  return request<Session>(`/sessions/${encodeURIComponent(sessionId)}/stop`, {
    method: 'POST',
  });
}

export function setSessionMode(
  sessionId: string,
  permissionMode: PermissionMode,
): Promise<Session> {
  return request<Session>(`/sessions/${encodeURIComponent(sessionId)}/mode`, {
    method: 'PATCH',
    body: { permissionMode },
  });
}

export function renameSession(sessionId: string, title: string): Promise<Session> {
  return request<Session>(`/sessions/${encodeURIComponent(sessionId)}/title`, {
    method: 'PATCH',
    body: { title },
  });
}

// --- Permissions ---
export function listPermissions(
  sessionId: string,
  signal?: AbortSignal,
): Promise<PermissionRequest[]> {
  return request<PermissionRequest[]>(
    `/sessions/${encodeURIComponent(sessionId)}/permissions`,
    { signal },
  );
}

export function respondPermission(
  sessionId: string,
  requestId: string,
  input: RespondPermissionInput,
): Promise<{ ok: boolean }> {
  return request<{ ok: boolean }>(
    `/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(requestId)}`,
    { method: 'POST', body: input },
  );
}

export function getSessionUsage(
  sessionId: string,
  signal?: AbortSignal,
): Promise<UsageRecord[]> {
  return request<UsageRecord[]>(
    `/sessions/${encodeURIComponent(sessionId)}/usage`,
    { signal },
  );
}

export function getUsageSummary(signal?: AbortSignal): Promise<UsageSummary> {
  return request<UsageSummary>('/usage', { signal });
}
