import { FormEvent, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, RequestError } from "../api";
import type { Branch, Project } from "../types";
import { ErrorBanner } from "./Home";

export function ProjectPage() {
  const { projectId = "" } = useParams();
  const [project, setProject] = useState<Project | null>(null);
  const [branches, setBranches] = useState<Branch[]>([]);
  const [error, setError] = useState<RequestError | null>(null);
  const [showCreate, setShowCreate] = useState(false);

  async function load() {
    try {
      const res = await api.getProject(projectId);
      setProject(res.project);
      setBranches(res.branches);
    } catch (e) {
      if (e instanceof RequestError) setError(e);
    }
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);

  if (error) return <ErrorBanner error={error} />;
  if (!project) return <div className="loading">Loading project…</div>;

  return (
    <>
      <div className="page-head">
        <h1>{project.name}</h1>
        <span className="spacer" />
        <button className="btn primary" onClick={() => setShowCreate(true)}>
          New branch
        </button>
      </div>
      <p className="page-sub">
        Each branch evolves the schema independently. Open one to browse and edit its schema; diff and merge live in
        the branch view.
      </p>

      <div className="card">
        {branches.map((b) => (
          <div key={b.id} className="branch-row">
            <Link to={`/branches/${b.id}`} className="name mono">
              {b.name}
            </Link>
            <span className="meta">head {b.head_commit_id.slice(0, 8)}</span>
            <span className="spacer" />
            <Link to={`/branches/${b.id}`} className="btn">
              Open
            </Link>
          </div>
        ))}
      </div>

      {showCreate && (
        <CreateBranchModal
          projectId={project.id}
          branches={branches}
          onClose={() => setShowCreate(false)}
          onCreated={load}
        />
      )}
    </>
  );
}

function CreateBranchModal({
  projectId,
  branches,
  onClose,
  onCreated,
}: {
  projectId: string;
  branches: Branch[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const main = branches.find((b) => b.name === "main") ?? branches[0];
  const [name, setName] = useState("");
  const [from, setFrom] = useState(main?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<RequestError | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createBranch(projectId, name.trim(), from);
      onCreated();
      onClose();
    } catch (err) {
      if (err instanceof RequestError) setError(err);
      else throw err;
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>New branch</h2>
        <p className="sub">The branch starts at the source branch's current head and evolves independently from there.</p>
        <form onSubmit={submit}>
          <label htmlFor="nb-name">Branch name</label>
          <input id="nb-name" type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="feature/billing" autoFocus />

          <label htmlFor="nb-from">Start from</label>
          <select id="nb-from" value={from} onChange={(e) => setFrom(e.target.value)}>
            {branches.map((b) => (
              <option key={b.id} value={b.id}>
                {b.name}
              </option>
            ))}
          </select>

          {error && <ErrorBanner error={error} />}

          <div className="actions">
            <button type="button" className="btn" onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className="btn primary" disabled={busy || !name.trim() || !from}>
              {busy ? "Creating…" : "Create branch"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
