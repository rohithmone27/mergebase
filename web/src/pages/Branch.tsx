import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, RequestError } from "../api";
import type { Branch, CommitMeta, Schema, Unsupported } from "../types";
import { ErrorBanner } from "./Home";
import { SchemaBrowser } from "../components/SchemaBrowser";
import { EditSchema } from "../components/EditSchema";
import { ImportDDL } from "../components/ImportDDL";
import { BranchGlyph, useWorkspace } from "../App";

export function BranchPage() {
  const { branchId = "" } = useParams();
  const [branch, setBranch] = useState<Branch | null>(null);
  const [schema, setSchema] = useState<Schema | null>(null);
  const [unsupported, setUnsupported] = useState<Unsupported[]>([]);
  const [commits, setCommits] = useState<CommitMeta[]>([]);
  const [error, setError] = useState<RequestError | null>(null);
  const [showEdit, setShowEdit] = useState(false);
  const [showImport, setShowImport] = useState(false);
  const [projectName, setProjectName] = useState("");

  const { set: setWs } = useWorkspace();
  const load = useCallback(async () => {
    try {
      const [schemaRes, commitsRes] = await Promise.all([api.branchSchema(branchId), api.branchCommits(branchId)]);
      setBranch(schemaRes.branch);
      setSchema(schemaRes.schema);
      setUnsupported(schemaRes.unsupported ?? []);
      setCommits(commitsRes.commits);
      api.getProject(schemaRes.branch.project_id).then((p) => {
        setProjectName(p.project.name);
        setWs({ projectId: schemaRes.branch.project_id, projectName: p.project.name, branchId: schemaRes.branch.id });
      }).catch(() => {});
    } catch (e) {
      if (e instanceof RequestError) setError(e);
    }
  }, [branchId]);

  useEffect(() => {
    load();
  }, [load]);

  if (error) return <ErrorBanner error={error} />;
  if (!branch || !schema) return <div className="loading">Loading branch…</div>;

  const lastCommit = commits[0];
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/">Projects</Link>
        <span className="sep">/</span>
        <Link to={`/projects/${branch.project_id}`}>{projectName || "project"}</Link>
        <span className="sep">/</span>
        <span>{branch.name}</span>
      </nav>
      <div className="page-head">
        <h1>
          <span style={{ color: "var(--accent)" }}><BranchGlyph size={20} /></span>
          <span className="mono">{branch.name}</span>
        </h1>
        <span className="spacer" />
        <a className="btn" href={`/api/branches/${branch.id}/export?format=sql`}
          download={`${branch.name.replace(/\//g, "-")}.sql`}>
          Export SQL
        </a>
        <button className="btn" onClick={() => setShowImport(true)}>Import DDL</button>
        <button className="btn" onClick={() => setShowEdit(true)}>Edit schema</button>
        <Link className="btn" to={`/projects/${branch.project_id}/diff?from=${branch.id}`}>Diff</Link>
        <Link className="btn primary" to={`/projects/${branch.project_id}/merge?source=${branch.id}`}>Merge…</Link>
      </div>
      <div className="meta-chips">
        <span>head {branch.head_commit_id.slice(0, 8)}</span>
        <span>{schema.tables.length} tables</span>
        {lastCommit && <span>last change {new Date(lastCommit.created_at).toLocaleDateString()}</span>}
      </div>

      {unsupported.length > 0 && (
        <div className="banner warn">
          <span>
            This snapshot imported with {unsupported.length} construct{unsupported.length > 1 ? "s" : ""} Mergebase
            does not model ({unsupported.map((u) => u.construct).join(", ")}) — recorded, not dropped.
          </span>
        </div>
      )}

      <div className="split">
        <div>
          {schema.tables.length === 0 ? (
            <div className="empty">
              <h3>Empty schema</h3>
              <p>This branch has no tables yet.</p>
            </div>
          ) : (
            <SchemaBrowser schema={schema} />
          )}
        </div>
        <aside className="card panel">
          <h2>History</h2>
          <div className="rail">
            {commits.map((c) => (
              <div key={c.id} className={"commit" + (c.parent2_id ? " merge" : "")}>
                <div className="msg">
                  {c.parent2_id && <span className="merge-tag">MERGE</span>}
                  {c.message}
                </div>
                <div className="who">
                  {c.author || "someone"} · {new Date(c.created_at).toLocaleDateString()} · {c.tables} tables
                </div>
              </div>
            ))}
          </div>
        </aside>
      </div>

      {showEdit && <EditSchema branchId={branch.id} schema={schema} onClose={() => setShowEdit(false)} onCommitted={load} />}
      {showImport && <ImportDDL branchId={branch.id} onClose={() => setShowImport(false)} onCommitted={load} />}
    </>
  );
}
