import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import type { Project, Session } from '../types';
import * as client from '../api/client';

const projects: Project[] = [
  {
    id: 'p1',
    name: 'smoke',
    path: '/tmp',
    gitMode: 'worktree',
    createdAt: '2026-05-25T12:00:00Z',
  },
];

function makeSession(
  id: string,
  title: string,
  updatedAt: string,
  projectId = 'p1',
): Session {
  return {
    id,
    projectId,
    claudeSessionId: '',
    title,
    status: 'idle',
    model: 'claude-opus-4-7',
    permissionMode: 'default',
    worktreePath: '',
    branch: '',
    baseCommit: '',
    previewState: '',
    createdAt: '2026-05-25T12:00:00Z',
    updatedAt,
  };
}

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

describe('Sidebar', () => {
  beforeEach(() => {
    vi.stubGlobal('WebSocket', StubWebSocket as unknown as typeof WebSocket);
    vi.spyOn(client, 'listProjects').mockResolvedValue(projects);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('hides the project path from the row but exposes it on hover', async () => {
    vi.spyOn(client, 'listSessions').mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    const link = await screen.findByRole('link', { name: 'smoke' });
    // Path is not rendered as visible text…
    expect(screen.queryByText('/tmp')).toBeNull();
    // …but is available as the link's title (shown on press/hover).
    expect(link).toHaveAttribute('title', '/tmp');
  });

  it('lists the three most-recent sessions under the project', async () => {
    vi.spyOn(client, 'listSessions').mockResolvedValue([
      makeSession('s1', 'oldest', '2026-05-25T12:00:00Z'),
      makeSession('s2', 'middle', '2026-05-25T12:05:00Z'),
      makeSession('s3', 'newest', '2026-05-25T12:10:00Z'),
      makeSession('s4', 'ancient', '2026-05-25T11:00:00Z'),
    ]);
    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    // The three newest by updatedAt, newest first; the oldest is dropped.
    const newest = await screen.findByText('newest');
    const sublist = newest.closest('ul')!;
    const labels = within(sublist)
      .getAllByRole('link')
      .map((l) => l.textContent);
    expect(labels).toEqual(['newest', 'middle', 'oldest']);
    expect(screen.queryByText('ancient')).toBeNull();
  });

  it('sorts projects by most recent session activity', async () => {
    const multi: Project[] = [
      {
        id: 'p1',
        name: 'alpha',
        path: '/tmp/a',
        gitMode: 'worktree',
        createdAt: '2026-05-25T08:00:00Z',
      },
      {
        id: 'p2',
        name: 'beta',
        path: '/tmp/b',
        gitMode: 'worktree',
        createdAt: '2026-05-25T09:00:00Z',
      },
      {
        id: 'p3',
        name: 'gamma',
        path: '/tmp/c',
        gitMode: 'worktree',
        createdAt: '2026-05-25T10:00:00Z',
      },
    ];
    vi.spyOn(client, 'listProjects').mockResolvedValue(multi);
    vi.spyOn(client, 'listSessions').mockResolvedValue([
      // alpha's newest session was 5 minutes ago — most recent activity.
      makeSession('s1', 'a-recent', '2026-05-25T13:00:00Z', 'p1'),
      // beta's newest session was an hour ago.
      makeSession('s2', 'b-older', '2026-05-25T12:00:00Z', 'p2'),
      // gamma has no sessions, so it sorts by its createdAt (oldest of the three).
    ]);

    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    await screen.findByRole('link', { name: 'alpha' });
    const names = screen
      .getAllByRole('link')
      .map((l) => l.textContent)
      .filter((t) => t === 'alpha' || t === 'beta' || t === 'gamma');
    expect(names).toEqual(['alpha', 'beta', 'gamma']);
  });

  it('new-project form defaults to worktree git-mode and submits it', async () => {
    vi.spyOn(client, 'listSessions').mockResolvedValue([]);
    const created: Project = {
      id: 'p9',
      name: 'My New',
      path: '/repo/new',
      gitMode: 'worktree',
      createdAt: '2026-05-25T13:00:00Z',
    };
    const spy = vi.spyOn(client, 'createProject').mockResolvedValue(created);

    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    // Worktree is the default — its radio button starts active.
    const worktreeBtn = await screen.findByRole('radio', { name: 'Worktree' });
    expect(worktreeBtn).toHaveAttribute('aria-checked', 'true');

    await userEvent.type(screen.getByLabelText('Project name'), 'My New');
    await userEvent.type(screen.getByLabelText('Project path'), '/repo/new');
    await userEvent.click(screen.getByRole('button', { name: 'Create project' }));

    await waitFor(() =>
      expect(spy).toHaveBeenCalledWith({
        name: 'My New',
        path: '/repo/new',
        gitMode: 'worktree',
      }),
    );
  });

  it('allows switching git-mode to Direct before submitting', async () => {
    vi.spyOn(client, 'listSessions').mockResolvedValue([]);
    const created: Project = {
      id: 'p9',
      name: 'D',
      path: '/repo/d',
      gitMode: 'default',
      createdAt: '2026-05-25T13:00:00Z',
    };
    const spy = vi.spyOn(client, 'createProject').mockResolvedValue(created);

    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    await userEvent.type(await screen.findByLabelText('Project name'), 'D');
    await userEvent.type(screen.getByLabelText('Project path'), '/repo/d');
    await userEvent.click(screen.getByRole('radio', { name: 'Direct' }));
    await userEvent.click(screen.getByRole('button', { name: 'Create project' }));

    await waitFor(() =>
      expect(spy).toHaveBeenCalledWith({
        name: 'D',
        path: '/repo/d',
        gitMode: 'default',
      }),
    );
  });

  it('quick-add creates a session and navigates to it', async () => {
    vi.spyOn(client, 'listSessions').mockResolvedValue([]);
    const created = makeSession('new1', 'fresh', '2026-05-25T13:00:00Z');
    const createSpy = vi.spyOn(client, 'createSession').mockResolvedValue(created);

    render(
      <MemoryRouter initialEntries={['/']}>
        <Sidebar />
      </MemoryRouter>,
    );

    const addBtn = await screen.findByRole('button', {
      name: 'New session in smoke',
    });
    await userEvent.click(addBtn);

    await waitFor(() => expect(createSpy).toHaveBeenCalledWith('p1'));
  });
});
