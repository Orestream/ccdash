// Sidebar — project list plus a "new project" form (name, path).

import { useCallback, useEffect, useState } from 'react';
import { NavLink } from 'react-router-dom';
import { ApiError, createProject, listProjects } from '../api/client';
import type { Project } from '../types';
import { useWebSocket } from '../hooks/useWebSocket';

export function Sidebar() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [name, setName] = useState('');
  const [path, setPath] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const { subscribe } = useWebSocket();

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
          {projects.map((p) => (
            <li key={p.id}>
              <NavLink
                to={`/projects/${p.id}`}
                className={({ isActive }) =>
                  isActive ? 'project-link active' : 'project-link'
                }
              >
                <span className="project-name">{p.name}</span>
                <span className="project-path">{p.path}</span>
              </NavLink>
            </li>
          ))}
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
