import { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { api, RequestError } from "../api";
import type { Branch, Change } from "../types";
import { ErrorBanner } from "./Home";
import { ChangeList } from "../components/ChangeList";

export function DiffPage() {
  const { projectId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const [branches, setBranches] = useState<Branch[]>([]);
  const [projectName, setProjectName] = useState("");
  const [changes, setChanges] = useState<Change[] | null>(null);
  const [unchanged, setUnchanged] = useState(0);
  const [names, setNames] = useState<{ from: string; to: string }>({ from: "", to: "" });
  const [error, setError] = useState<RequestError | null>(null);

  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";

  useEffect(() => {
    api.getProject(projectId).then((res) => {
      setBranches(res.branches);
      setProjectName(res.project.name);
      // Sensible default: main vs the first non-main branch.
      if (!params.get("from") && res.branches.length >= 2) {
        const main = res.branches.find((b) => b.name === "main") ?? res.branches[0];
        const other = res.branches.find((b) => b.id !== main.id)!;
        setParams({ from: main.id, to: other.id }, { replace: true });
      }
    }).catch((e) => e instanceof RequestError && setError(e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);

  useEffect(() => {
    if (!from || !to) return;
    setChanges(null);
    setError(null);
    api.diff(from, to)
      .then((res) => {
        setChanges(res.diff.changes);
        setUnchanged(res.diff.unchanged);
        setNames({ from: res.from.name, to: res.to.name });
      })
      .catch((e) => e instanceof RequestError && setError(e));
  }, [from, to]);

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/">Projects</Link>
        <span className="sep">/</span>
        <Link to={`/projects/${projectId}`}>{projectName || "project"}</Link>
        <span className="sep">/</span>
        <span>diff</span>
      </nav>
      <div className="page-head">
        <h1>Diff</h1>
        <span className="spacer" />
        {from && to && (
          <Link className="btn" to={`/projects/${projectId}/migration?from=${from}&to=${to}`}>
            Migration script for this diff
          </Link>
        )}
      </div>
      <p className="page-sub">
        A semantic comparison: what actually changed, by meaning — a rename reads as a rename, never as a drop and an add.
      </p>

      <div className="refbar">
        <select value={from} onChange={(e) => setParams({ from: e.target.value, to })} aria-label="From branch">
          <option value="">from…</option>
          {branches.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
        </select>
        <span aria-hidden>↔</span>
        <select value={to} onChange={(e) => setParams({ from, to: e.target.value })} aria-label="To branch">
          <option value="">to…</option>
          {branches.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
        </select>
      </div>

      {error && <ErrorBanner error={error} />}
      {!error && from && to && changes === null && <div className="loading">Comparing…</div>}

      {changes !== null && changes.length === 0 && (
        <div className="empty">
          <h3>No differences</h3>
          <p>{names.from} and {names.to} have identical schemas.</p>
        </div>
      )}

      {changes !== null && changes.length > 0 && (
        <>
          <p className="page-sub">
            <b>{changes.length}</b> change{changes.length === 1 ? "" : "s"} from <span className="mono">{names.from}</span> to{" "}
            <span className="mono">{names.to}</span> · {unchanged} objects untouched
          </p>
          <ChangeList changes={changes} />
        </>
      )}
    </>
  );
}
