// Sidebar — compact project list. Each project is a row (path hidden; shown on
// the project page) with a quick-add button to launch a session, and its three
// most-recently-used sessions listed beneath it as sub-menu items.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { NavLink, useNavigate } from 'react-router-dom';
import { ApiError, createProject, createSession, listProjects } from '../api/client';
import type { Project, Session } from '../types';
import { useSessions } from '../hooks/useSessions';
import { useWebSocket } from '../hooks/useWebSocket';

const RECENT_SESSION_LIMIT = 3;

export function Sidebar() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [name, setName] = useState('');
  const [path, setPath] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [addingTo, setAddingTo] = useState<string | null>(null);
  const { subscribe } = useWebSocket();
  const { sessions, reload: reloadSessions } = useSessions();
  const navigate = useNavigate();

  const load = useCallback((signal?: AbortSignal) => {
    setLoading(true);
    listProjects(signal)
      .then((data) => {
        setProjects(data);
        setError(null);
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (signal?.aborted) return;
        setError(err instanceof Error ? err.message : 'failed to load projects');
        setLoading(false);
      });
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    return () => controller.abort();
  }, [load]);

  useEffect(() => {
    return subscribe((event) => {
      if (event.type === 'project.created') {
        setProjects((prev) =>
          prev.some((p) => p.id === event.payload.id)
            ? prev
            : [event.payload, ...prev],
        );
      } else if (event.type === 'project.deleted') {
        setProjects((prev) => prev.filter((p) => p.id !== event.payload.id));
      }
    });
  }, [subscribe]);

  // Three most-recently-updated sessions per project, keyed by project id.
  const recentByProject = useMemo(() => {
    const map = new Map<string, Session[]>();
    for (const s of sessions) {
      const arr = map.get(s.projectId);
      if (arr) arr.push(s);
      else map.set(s.projectId, [s]);
    }
    for (const arr of map.values()) {
      arr.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
      arr.splice(RECENT_SESSION_LIMIT);
    }
    return map;
  }, [sessions]);

  const handleQuickAdd = async (projectId: string) => {
    if (addingTo) return;
    setAddingTo(projectId);
    try {
      const created = await createSession(projectId);
      reloadSessions();
      navigate(`/sessions/${created.id}`);
    } catch {
      // Fall back to the project page, where the error surfaces with detail.
      navigate(`/projects/${projectId}`);
    } finally {
      setAddingTo(null);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !path.trim() || submitting) return;
    setSubmitting(true);
    setFormError(null);
    try {
      const created = await createProject({ name: name.trim(), path: path.trim() });
      setProjects((prev) =>
        prev.some((p) => p.id === created.id) ? prev : [created, ...prev],
      );
      setName('');
      setPath('');
    } catch (err) {
      const msg =
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'failed to create project';
      setFormError(msg);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <aside className="sidebar">
      <div className="sidebar-header">
        <NavLink to="/" className="brand">
          ccdash
        </NavLink>
      </div>

      <nav className="project-list">
        <h2 className="section-title">Projects</h2>
        {loading && <p className="muted">Loading…</p>}
        {error && !loading && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {!loading && !error && projects.length === 0 && (
          <p className="muted">No projects yet.</p>
        )}
        <ul>
          {projects.map((p) => {
            const recent = recentByProject.get(p.id) ?? [];
            return (
              <li key={p.id} className="project-item">
                <div className="project-row">
                  <NavLink
                    to={`/projects/${p.id}`}
                    className={({ isActive }) =>
                      isActive ? 'project-link active' : 'project-link'
                    }
                    title={p.path}
                  >
                    <span className="project-name">{p.name}</span>
                  </NavLink>
                  <button
                    type="button"
                    className="quick-add"
                    aria-label={`New session in ${p.name}`}
                    title="New session"
                    disabled={addingTo === p.id}
                    onClick={() => handleQuickAdd(p.id)}
                  >
                    +
                  </button>
                </div>
                {recent.length > 0 && (
                  <ul className="session-sublist">
                    {recent.map((s) => (
                      <li key={s.id}>
                        <NavLink
                          to={`/sessions/${s.id}`}
                          className={({ isActive }) =>
                            isActive ? 'session-sublink active' : 'session-sublink'
                          }
                        >
                          <span
                            className={`session-dot status-${s.status}`}
                            aria-hidden="true"
                          />
                          <span className="session-subname">
                            {s.title || 'Untitled session'}
                          </span>
                        </NavLink>
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            );
          })}
        </ul>
      </nav>

      <form className="new-project" onSubmit={handleSubmit}>
        <h2 className="section-title">New project</h2>
        <input
          aria-label="Project name"
          placeholder="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <input
          aria-label="Project path"
          placeholder="/path/to/repo"
          value={path}
          onChange={(e) => setPath(e.target.value)}
        />
        {formError && (
          <p className="error" role="alert">
            {formError}
          </p>
        )}
        <button type="submit" disabled={submitting || !name.trim() || !path.trim()}>
          {submitting ? 'Creating…' : 'Create project'}
        </button>
      </form>
    </aside>
  );
}

export default Sidebar;
