import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, RequestError } from "../api";
import type { Branch, CommitMeta, Schema, Unsupported } from "../types";
import { ErrorBanner } from "./Home";
import { SchemaBrowser } from "../components/SchemaBrowser";

export function BranchPage() {
  const { branchId = "" } = useParams();
  const [branch, setBranch] = useState<Branch | null>(null);
  const [schema, setSchema] = useState<Schema | null>(null);
  const [unsupported, setUnsupported] = useState<Unsupported[]>([]);
  const [commits, setCommits] = useState<CommitMeta[]>([]);
  const [error, setError] = useState<RequestError | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const [schemaRes, commitsRes] = await Promise.all([api.branchSchema(branchId), api.branchCommits(branchId)]);
        setBranch(schemaRes.branch);
        setSchema(schemaRes.schema);
        setUnsupported(schemaRes.unsupported ?? []);
        setCommits(commitsRes.commits);
      } catch (e) {
        if (e instanceof RequestError) setError(e);
      }
    })();
  }, [branchId]);

  if (error) return <ErrorBanner error={error} />;
  if (!branch || !schema) return <div className="loading">Loading branch…</div>;

  return (
    <>
      <div className="page-head">
        <h1>
          <Link to={`/projects/${branch.project_id}`}>← project</Link>{" "}
          <span className="mono">{branch.name}</span>
        </h1>
        <span className="spacer" />
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
          {commits.map((c) => (
            <div key={c.id} className="commit">
              <div className="msg">
                {c.parent2_id && <span className="merge-tag">MERGE </span>}
                {c.message}
              </div>
              <div className="who">
                {c.author || "unknown"} · {new Date(c.created_at).toLocaleString()} · {c.tables} tables
              </div>
            </div>
          ))}
        </aside>
      </div>
    </>
  );
}
