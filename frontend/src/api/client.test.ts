import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  ApiError,
  createProject,
  deleteProject,
  getHealth,
  getUsageSummary,
  listMessages,
  listProjects,
  sendMessage,
  stopSession,
} from './client';
import type { Message, Project, UsageSummary } from '../types';

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    ...init,
  });
}

describe('api client', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    vi.stubGlobal('fetch', fetchMock);
    fetchMock.mockReset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('GET /api/projects parses an array', async () => {
    const projects: Project[] = [
      {
        id: 'p1',
        name: 'My App',
        path: '/code/app',
        createdAt: '2026-05-25T12:00:00Z',
      },
    ];
    fetchMock.mockResolvedValueOnce(jsonResponse(projects));

    const result = await listProjects();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, opts] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/projects');
    expect(opts.method).toBe('GET');
    expect(result).toEqual(projects);
  });

  it('POST /api/projects sends a JSON body', async () => {
    const created: Project = {
      id: 'p2',
      name: 'New',
      path: '/code/new',
      createdAt: '2026-05-25T12:00:00Z',
    };
    fetchMock.mockResolvedValueOnce(jsonResponse(created, { status: 201 }));

    const result = await createProject({ name: 'New', path: '/code/new' });

    const [url, opts] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/projects');
    expect(opts.method).toBe('POST');
    expect(opts.headers).toMatchObject({ 'Content-Type': 'application/json' });
    expect(JSON.parse(opts.body)).toEqual({ name: 'New', path: '/code/new' });
    expect(result).toEqual(created);
  });

  it('encodes path segments and posts messages', async () => {
    const msg: Message = {
      id: 'm1',
      sessionId: 's 1',
      role: 'user',
      content: 'hi',
      createdAt: '2026-05-25T12:00:30Z',
    };
    fetchMock.mockResolvedValueOnce(jsonResponse(msg, { status: 202 }));

    await sendMessage('s 1', { content: 'hi' });

    const [url, opts] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/sessions/s%201/messages');
    expect(opts.method).toBe('POST');
    expect(JSON.parse(opts.body)).toEqual({ content: 'hi' });
  });

  it('builds the messages list URL', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]));
    await listMessages('abc');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/sessions/abc/messages');
  });

  it('builds the stop URL with POST', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        id: 'abc',
        projectId: 'p',
        claudeSessionId: '',
        title: 't',
        status: 'idle',
        model: 'm',
        createdAt: 'x',
        updatedAt: 'y',
      }),
    );
    await stopSession('abc');
    const [url, opts] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/sessions/abc/stop');
    expect(opts.method).toBe('POST');
  });

  it('handles 204 (DELETE) without parsing a body', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await expect(deleteProject('p1')).resolves.toBeUndefined();
    const [url, opts] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/projects/p1');
    expect(opts.method).toBe('DELETE');
  });

  it('parses the usage summary', async () => {
    const summary: UsageSummary = {
      totalInputTokens: 12000,
      totalOutputTokens: 4300,
      totalCostUsd: 1.23,
      bySession: [
        { sessionId: 's1', inputTokens: 100, outputTokens: 50, costUsd: 0.01 },
      ],
    };
    fetchMock.mockResolvedValueOnce(jsonResponse(summary));
    const result = await getUsageSummary();
    expect(fetchMock.mock.calls[0][0]).toBe('/api/usage');
    expect(result).toEqual(summary);
  });

  it('throws ApiError with the server error message on non-2xx', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: 'project not found' }, { status: 404 }),
    );
    await expect(getHealth()).rejects.toMatchObject({
      name: 'ApiError',
      status: 404,
      message: 'project not found',
    });
  });

  it('wraps network failures as ApiError with status 0', async () => {
    fetchMock.mockRejectedValueOnce(new TypeError('Failed to fetch'));
    const err = await getHealth().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(0);
  });
});
