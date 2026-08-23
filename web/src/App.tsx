import { createContext, useContext, useEffect, useMemo, useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { api } from "./api";
import type { Branch, Project } from "./types";

// ---- workspace context: pages tell the shell where the user is ----

export interface Workspace {
  projectId?: string;
  projectName?: string;
  branchId?: string;
  // bump to make the sidebar refetch (e.g. after creating a branch)
  version?: number;
}

const WorkspaceCtx = createContext<{ ws: Workspace; set: (w: Workspace) => void }>({
  ws: {},
  set: () => {},
});

export function useWorkspace() {
  return useContext(WorkspaceCtx);
}

// ---- glyphs ----

export function BranchGlyph({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor"
      strokeWidth="1.7" strokeLinecap="round" aria-hidden>
      <circle cx="4.2" cy="3.6" r="1.8" />
      <circle cx="4.2" cy="12.4" r="1.8" />
      <circle cx="11.8" cy="3.6" r="1.8" />
      <path d="M4.2 5.4v5.2M11.8 5.4c0 3-3 3.4-5.6 3.6" />
    </svg>
  );
}

export function TableGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden>
      <rect x="1.5" y="2" width="11" height="10" rx="1.5" />
      <path d="M1.5 5.5h11M5.5 5.5V12" />
    </svg>
  );
}

// ---- shell ----

export function Layout() {
  const [ws, setWsState] = useState<Workspace>({});
  const value = useMemo(
    () => ({
      ws,
      set: (w: Workspace) =>
        setWsState((prev) => {
          if (
            prev.projectId === w.projectId &&
            prev.projectName === w.projectName &&
            prev.branchId === w.branchId &&
            (w.version === undefined || prev.version === w.version)
          ) {
            return prev;
          }
          return { ...w, version: w.version ?? prev.version };
        }),
    }),
    [ws],
  );

  return (
    <WorkspaceCtx.Provider value={value}>
      <div className="shell">
        <Sidebar />
        <main className="content">
          <Outlet />
        </main>
      </div>
    </WorkspaceCtx.Provider>
  );
}

function Sidebar() {
  const { ws } = useWorkspace();
  const location = useLocation();
  const [projects, setProjects] = useState<Project[]>([]);
  const [branches, setBranches] = useState<Branch[]>([]);
  const [resetting, setResetting] = useState(false);

  useEffect(() => {
    api.listProjects().then((r) => setProjects(r.projects)).catch(() => {});
  }, [ws.projectId, ws.version, location.pathname]);

  useEffect(() => {
    if (!ws.projectId) {
      setBranches([]);
      return;
    }
    api.getProject(ws.projectId).then((r) => setBranches(r.branches)).catch(() => setBranches([]));
  }, [ws.projectId, ws.version, location.pathname]);

  async function resetDemo() {
    if (!window.confirm("Reset the demo workspace? All projects and branches revert to the seeded state.")) return;
    setResetting(true);
    try {
      await api.demoReset();
      window.location.href = "/";
    } finally {
      setResetting(false);
    }
  }

  return (
    <aside className="sidebar">
      <Link to="/" className="logo">
        <span className="logo-mark"><BranchGlyph size={17} /></span>
        mergebase
      </Link>

      <div className="side-section">
        <div className="side-label">Projects</div>
        {projects.map((p) => (
          <NavLink key={p.id} to={`/projects/${p.id}`}
            className={({ isActive }) => "side-item" + (isActive || ws.projectId === p.id ? " active" : "")}>
            <TableGlyph />
            <span className="trunc">{p.name}</span>
          </NavLink>
        ))}
        <Link to="/" className="side-item quiet">
          <span className="plus">+</span> All projects
        </Link>
      </div>

      {ws.projectId && branches.length > 0 && (
        <>
          <div className="side-section">
            <div className="side-label">Branches</div>
            {branches.map((b) => (
              <NavLink key={b.id} to={`/branches/${b.id}`}
                className={({ isActive }) => "side-item mono" + (isActive || ws.branchId === b.id ? " active" : "")}>
                <BranchGlyph size={13} />
                <span className="trunc">{b.name}</span>
                {b.name === "main" && <span className="side-tag">default</span>}
              </NavLink>
            ))}
          </div>
          <div className="side-section">
            <div className="side-label">Compare</div>
            <NavLink to={`/projects/${ws.projectId}/diff`} className={({ isActive }) => "side-item" + (isActive ? " active" : "")}>
              <span className="glyph-txt">±</span> Diff
            </NavLink>
            <NavLink to={`/projects/${ws.projectId}/merge`} className={({ isActive }) => "side-item" + (isActive ? " active" : "")}>
              <span className="glyph-txt">⑂</span> Merge
            </NavLink>
            <NavLink to={`/projects/${ws.projectId}/migration`} className={({ isActive }) => "side-item" + (isActive ? " active" : "")}>
              <span className="glyph-txt">≡</span> Migration
            </NavLink>
          </div>
        </>
      )}

      <div className="side-foot">
        <div className="demo-note">
          <span className="dot" aria-hidden />
          Demo workspace — data may reset. Explore freely.
        </div>
        <button className="btn ghost small" onClick={resetDemo} disabled={resetting}>
          {resetting ? "Resetting…" : "Reset demo"}
        </button>
      </div>
    </aside>
  );
}
