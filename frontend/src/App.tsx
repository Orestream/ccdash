// App — dashboard layout: top UsageBar, left Sidebar, center routed content.

import { BrowserRouter, Route, Routes, useParams } from 'react-router-dom';
import { Sidebar } from './components/Sidebar';
import { SessionList } from './components/SessionList';
import { SessionView } from './components/SessionView';
import { UsageBar } from './components/UsageBar';

function Overview() {
  return (
    <section className="overview">
      <h1>Welcome to ccdash</h1>
      <p className="muted">
        Select a project from the sidebar to view its sessions, or create a new
        project to get started.
      </p>
    </section>
  );
}

function ProjectRoute() {
  const { projectId } = useParams<{ projectId: string }>();
  if (!projectId) return <Overview />;
  return <SessionList projectId={projectId} />;
}

function SessionRoute() {
  const { sessionId } = useParams<{ sessionId: string }>();
  if (!sessionId) return <Overview />;
  return <SessionView sessionId={sessionId} />;
}

export function App() {
  return (
    <BrowserRouter>
      <div className="app">
        <UsageBar />
        <div className="app-body">
          <Sidebar />
          <main className="content">
            <Routes>
              <Route path="/" element={<Overview />} />
              <Route path="/projects/:projectId" element={<ProjectRoute />} />
              <Route path="/sessions/:sessionId" element={<SessionRoute />} />
              <Route path="*" element={<Overview />} />
            </Routes>
          </main>
        </div>
      </div>
    </BrowserRouter>
  );
}

export default App;
