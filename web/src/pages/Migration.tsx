import { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { api, RequestError } from "../api";
import type { Branch, MigrationWarning } from "../types";
import { ErrorBanner } from "./Home";

// The migration script view. Generated, never executed: Mergebase hands the
// SQL over (view / copy / download) and never touches a real database.
export function MigrationPage() {
  const { projectId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const [branches, setBranches] = useState<Branch[]>([]);
  const [projectName, setProjectName] = useState("");
  const [sql, setSql] = useState<string | null>(null);
  const [warnings, setWarnings] = useState<MigrationWarning[]>([]);
  const [names, setNames] = useState({ from: "", to: "" });
  const [error, setError] = useState<RequestError | null>(null);
  const [copied, setCopied] = useState(false);

  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";

  useEffect(() => {
    api.getProject(projectId).then((res) => { setBranches(res.branches); setProjectName(res.project.name); })
      .catch((e) => e instanceof RequestError && setError(e));
  }, [projectId]);

  useEffect(() => {
    if (!from || !to) return;
    setSql(null);
    setError(null);
    api.migration(from, to)
      .then((res) => {
        setSql(res.sql);
        setWarnings(res.warnings);
        setNames({ from: res.from.name, to: res.to.name });
      })
      .catch((e) => e instanceof RequestError && setError(e));
  }, [from, to]);

  function copy() {
    if (!sql) return;
    navigator.clipboard.writeText(sql).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/">Projects</Link>
        <span className="sep">/</span>
        <Link to={`/projects/${projectId}`}>{projectName || "project"}</Link>
        <span className="sep">/</span>
        <span>migration</span>
      </nav>
      <div className="page-head">
        <h1>Migration script</h1>
        <span className="spacer" />
        {sql && (
          <>
            <button className="btn" onClick={copy}>{copied ? "Copied ✓" : "Copy SQL"}</button>
            <a className="btn" href={`/api/migration?from=${from}&to=${to}&format=sql`} download={`migration-${names.from}-to-${names.to}.sql`.replace(/\//g, "-")}>
              Download
            </a>
          </>
        )}
      </div>
      <p className="page-sub">
        Ordered DDL that carries <span className="mono">{names.from || "one version"}</span> to{" "}
        <span className="mono">{names.to || "another"}</span>. Statement order is dependency-safe — including circular
        foreign keys — and renames stay renames, so data survives. Mergebase generates this script; it never runs it.
      </p>

      <div className="refbar">
        <select value={from} onChange={(e) => setParams({ from: e.target.value, to })} aria-label="From">
          <option value="">from…</option>
          {branches.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
        </select>
        <span aria-hidden>→</span>
        <select value={to} onChange={(e) => setParams({ from, to: e.target.value })} aria-label="To">
          <option value="">to…</option>
          {branches.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
        </select>
      </div>

      {error && <ErrorBanner error={error} />}

      {warnings.length > 0 && (
        <div className="banner warn">
          <span>
            <b>{warnings.length} thing{warnings.length === 1 ? "" : "s"} to check before running this on real data:</b>{" "}
            {warnings.map((w) => w.message).join(" · ")}
          </span>
        </div>
      )}

      {sql !== null ? (
        <pre className="sqlview"><code>{sql}</code></pre>
      ) : from && to && !error ? (
        <div className="loading">Generating…</div>
      ) : null}
    </>
  );
}
