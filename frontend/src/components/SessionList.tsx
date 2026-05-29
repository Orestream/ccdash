// SessionList — sessions for the selected project, each row showing a StatusBadge.

import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  ApiError,
  createSession,
  deleteSession,
  getProject,
  updateProject,
} from '../api/client';
import type { Project, ProjectGitMode } from '../types';
import { useSessions } from '../hooks/useSessions';
import { StatusBadge } from './StatusBadge';
import { GitModeSelector } from './GitModeSelector';

export interface SessionListProps {
  projectId: string;
}

export function SessionList({ projectId }: SessionListProps) {
  const { sessions, loading, error, reload } = useSessions(projectId);
  const [project, setProject] = useState<Project | null>(null);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [gitModeBusy, setGitModeBusy] = useState(false);
  const [gitModeError, setGitModeError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    setProject(null);
    getProject(projectId, controller.signal)
      .then(setProject)
      .catch(() => {
        /* header is non-critical; the list shows its own error */
      });
    return () => controller.abort();
  }, [projectId]);

  const handleCreate = async () => {
    if (creating) return;
    setCreating(true);
    setCreateError(null);
    try {
      await createSession(projectId);
      reload();
    } catch (err) {
      const msg =
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'failed to create session';
      setCreateError(msg);
    } finally {
      setCreating(false);
    }
  };

  const handleGitModeChange = async (mode: ProjectGitMode) => {
    if (!project || project.gitMode === mode || gitModeBusy) return;
    setGitModeBusy(true);
    setGitModeError(null);
    try {
      const updated = await updateProject(project.id, { gitMode: mode });
      setProject(updated);
    } catch (err) {
      const msg =
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'failed to update project';
      setGitModeError(msg);
    } finally {
      setGitModeBusy(false);
    }
  };

  const handleDelete = async (id: string, hasBranch: boolean) => {
    // Default keeps the branch around so it can still be reviewed/merged. The
    // confirm offers a one-click "also delete the branch" path for git
    // projects; non-git sessions skip the branch question entirely.
    const message = hasBranch
      ? 'Delete this session and remove its worktree?\n\nClick Cancel to keep it, OK to delete (the branch is kept).'
      : 'Delete this session?';
    if (!window.confirm(message)) return;
    setDeletingId(id);
    setCreateError(null);
    try {
      await deleteSession(id);
      reload();
    } catch (err) {
      const msg =
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'failed to delete session';
      setCreateError(msg);
    } finally {
      setDeletingId(null);
    }
  };

  return (
    <section className="session-list">
      <header className="panel-header">
        <div>
          <h1>{project ? project.name : 'Sessions'}</h1>
          {project && <p className="muted">{project.path}</p>}
          {project && (
            <div className="project-settings">
              <span className="project-settings-label">Git mode</span>
              <GitModeSelector
                mode={project.gitMode}
                onChange={(m) => void handleGitModeChange(m)}
                disabled={gitModeBusy}
              />
            </div>
          )}
        </div>
        <button onClick={handleCreate} disabled={creating}>
          {creating ? 'Creating…' : 'New session'}
        </button>
      </header>

      {gitModeError && (
        <p className="error" role="alert">
          {gitModeError}
        </p>
      )}
      {createError && (
        <p className="error" role="alert">
          {createError}
        </p>
      )}

      {loading && <p className="muted">Loading sessions…</p>}
      {error && !loading && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {!loading && !error && sessions.length === 0 && (
        <p className="muted">No sessions in this project yet.</p>
      )}

      <ul className="rows">
        {sessions.map((s) => (
          <li key={s.id} className="session-row-wrapper">
            <Link to={`/sessions/${s.id}`} className="session-row">
              <span className="session-title">{s.title || 'Untitled session'}</span>
              {s.branch && <span className="session-branch">{s.branch}</span>}
              <span className="session-model">{s.model}</span>
              <StatusBadge status={s.status} />
            </Link>
            <button
              type="button"
              className="session-delete"
              aria-label={`Delete session ${s.title || s.id}`}
              title="Delete session"
              disabled={deletingId === s.id}
              onClick={() => void handleDelete(s.id, Boolean(s.branch))}
            >
              ✕
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

export default SessionList;
