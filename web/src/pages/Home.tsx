import { FormEvent, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, RequestError } from "../api";
import type { Project, Unsupported } from "../types";
import { BranchGlyph } from "../App";

const SAMPLE_DDL = `CREATE TABLE customers (
    id         BIGSERIAL PRIMARY KEY,
    email      VARCHAR(255) NOT NULL UNIQUE,
    full_name  TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE subscriptions (
    id          BIGSERIAL PRIMARY KEY,
    customer_id BIGINT NOT NULL REFERENCES customers (id),
    plan        VARCHAR(40) NOT NULL,
    started_at  TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX idx_subscriptions_plan ON subscriptions (plan);`;

export function Home() {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState<RequestError | null>(null);
  const [showCreate, setShowCreate] = useState(false);

  async function load() {
    try {
      const res = await api.listProjects();
      setProjects(res.projects);
    } catch (e) {
      if (e instanceof RequestError) setError(e);
    }
  }
  useEffect(() => {
    load();
  }, []);

  return (
    <>
      <div className="hero">
        <h1>
          Branch a schema. <span className="dim">Merge it back safely.</span>
        </h1>
        <p>
          A project versions one database schema the way Git versions code: branch it, evolve branches independently,
          see exactly what diverged, and merge back with conflicts surfaced — never guessed.
        </p>
      </div>
      <div className="page-head">
        <h1 style={{ fontSize: "1.1rem" }}>Projects</h1>
        <span className="spacer" />
        <button className="btn primary" onClick={() => setShowCreate(true)}>
          New project
        </button>
      </div>

      {error && <ErrorBanner error={error} />}
      {projects === null && !error && <div className="loading">Loading projects…</div>}

      {projects !== null && projects.length === 0 && (
        <div className="empty">
          <h3>No projects yet</h3>
          <p>
            Create one from pasted PostgreSQL DDL — or hit <b>Reset demo</b> above to restore the seeded workspace.
          </p>
        </div>
      )}

      {projects !== null && projects.length > 0 && (
        <div className="project-grid">
          {projects.map((p) => (
            <Link key={p.id} to={`/projects/${p.id}`} className="card project-card">
              <div className="glyph"><BranchGlyph size={20} /></div>
              <h3>{p.name}</h3>
              <div className="meta">created {new Date(p.created_at).toLocaleDateString()}</div>
              <span className="go" aria-hidden>→</span>
            </Link>
          ))}
        </div>
      )}

      {showCreate && <CreateProjectModal onClose={() => setShowCreate(false)} onCreated={load} />}
    </>
  );
}

function CreateProjectModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [ddl, setDdl] = useState("");
  const [author, setAuthor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<RequestError | null>(null);
  const [warnings, setWarnings] = useState<Unsupported[] | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await api.createProject(name.trim(), ddl, author.trim());
      if (res.unsupported.length > 0) {
        // Surface the fidelity report before leaving the modal — the user
        // should know what the import could not carry over.
        setWarnings(res.unsupported);
        onCreated();
      } else {
        onCreated();
        onClose();
      }
    } catch (err) {
      if (err instanceof RequestError) setError(err);
      else throw err;
    } finally {
      setBusy(false);
    }
  }

  if (warnings) {
    return (
      <div className="modal-backdrop" onClick={onClose}>
        <div className="modal" onClick={(e) => e.stopPropagation()}>
          <h2>Imported with notes</h2>
          <p className="sub">
            The schema imported, but these constructs are outside what Mergebase models. They were recorded, not
            silently dropped — exports will state this too.
          </p>
          <ul>
            {warnings.map((w, i) => (
              <li key={i}>
                <b>{w.construct}</b>
                {w.detail ? <span className="mono"> — {w.detail}</span> : null}
              </li>
            ))}
          </ul>
          <div className="actions">
            <button className="btn primary" onClick={onClose}>
              Got it
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>New project</h2>
        <p className="sub">Paste PostgreSQL DDL to import a schema, or leave it empty to start from nothing.</p>
        <form onSubmit={submit}>
          <label htmlFor="np-name">Project name</label>
          <input id="np-name" type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="Payments Platform" autoFocus />

          <label htmlFor="np-author">Your name (used as commit author)</label>
          <input id="np-author" type="text" value={author} onChange={(e) => setAuthor(e.target.value)} placeholder="optional" />

          <label htmlFor="np-ddl">
            PostgreSQL DDL{" "}
            <button type="button" className="btn quiet" style={{ padding: "0 0.4rem" }} onClick={() => setDdl(SAMPLE_DDL)}>
              use a sample
            </button>
          </label>
          <textarea id="np-ddl" value={ddl} onChange={(e) => setDdl(e.target.value)} placeholder="CREATE TABLE …" spellCheck={false} />

          {error && <ErrorBanner error={error} />}

          <div className="actions">
            <button type="button" className="btn" onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className="btn primary" disabled={busy || !name.trim()}>
              {busy ? "Importing…" : "Create project"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

export function ErrorBanner({ error }: { error: RequestError }) {
  return (
    <div className="banner error" role="alert">
      <span>{error.message}</span>
      {error.hint && <span className="hint">{error.hint}</span>}
    </div>
  );
}
