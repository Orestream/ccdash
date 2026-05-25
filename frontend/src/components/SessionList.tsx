// SessionList — sessions for the selected project, each row showing a StatusBadge.

import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, createSession, getProject } from '../api/client';
import type { Project } from '../types';
import { useSessions } from '../hooks/useSessions';
import { StatusBadge } from './StatusBadge';

export interface SessionListProps {
  projectId: string;
}

export function SessionList({ projectId }: SessionListProps) {
  const { sessions, loading, error, reload } = useSessions(projectId);
  const [project, setProject] = useState<Project | null>(null);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

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

  return (
    <section className="session-list">
      <header className="panel-header">
        <div>
          <h1>{project ? project.name : 'Sessions'}</h1>
          {project && <p className="muted">{project.path}</p>}
        </div>
        <button onClick={handleCreate} disabled={creating}>
          {creating ? 'Creating…' : 'New session'}
        </button>
      </header>

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
          <li key={s.id}>
            <Link to={`/sessions/${s.id}`} className="session-row">
              <span className="session-title">{s.title || 'Untitled session'}</span>
              <span className="session-model">{s.model}</span>
              <StatusBadge status={s.status} />
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}

export default SessionList;
