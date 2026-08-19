import { useCallback, useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { api, RequestError } from "../api";
import type { Branch, Change, Conflict, Problem, Resolution } from "../types";
import { ErrorBanner } from "./Home";
import { ChangeList } from "../components/ChangeList";

// The merge screen: pick source → target, preview the three-way merge,
// resolve each conflict (ours / theirs / provide-a-value), see the
// validation verdict, and land the merge commit. What the preview shows is
// exactly what commits — both calls share one code path on the server.
export function MergePage() {
  const { projectId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const [branches, setBranches] = useState<Branch[]>([]);
  const [error, setError] = useState<RequestError | null>(null);

  const [conflicts, setConflicts] = useState<Conflict[]>([]);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [changes, setChanges] = useState<Change[]>([]);
  const [previewed, setPreviewed] = useState(false);
  const [resolutions, setResolutions] = useState<Record<string, Resolution>>({});
  const [author, setAuthor] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<{ commitId: string; message: string; changes: Change[]; oldHead: string } | null>(null);

  const source = params.get("source") ?? "";
  const target = params.get("target") ?? "";
  const sourceName = branches.find((b) => b.id === source)?.name ?? "source";
  const targetName = branches.find((b) => b.id === target)?.name ?? "target";

  useEffect(() => {
    api.getProject(projectId).then((res) => {
      setBranches(res.branches);
      if (!params.get("target") && res.branches.length >= 2) {
        const main = res.branches.find((b) => b.name === "main") ?? res.branches[0];
        const other = res.branches.find((b) => b.id !== main.id)!;
        setParams({ source: params.get("source") || other.id, target: main.id }, { replace: true });
      }
    }).catch((e) => e instanceof RequestError && setError(e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);

  const preview = useCallback(async (res: Resolution[]) => {
    setError(null);
    setBusy(true);
    try {
      const out = await api.mergePreview(source, target, res);
      setConflicts(out.conflicts ?? []);
      setProblems(out.problems ?? []);
      setChanges(out.changes ?? []);
      setPreviewed(true);
    } catch (e) {
      if (e instanceof RequestError) setError(e);
      else throw e;
    } finally {
      setBusy(false);
    }
  }, [source, target]);

  useEffect(() => {
    setPreviewed(false);
    setResolutions({});
    setDone(null);
    if (source && target && source !== target) preview([]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source, target]);

  const unresolved = conflicts.filter((c) => !resolutions[c.id]);
  const allResolved = conflicts.length > 0 && unresolved.length === 0;

  async function execute() {
    setBusy(true);
    setError(null);
    const oldHead = branches.find((b) => b.id === target)?.head_commit_id ?? "";
    try {
      const res = await api.merge(source, target, Object.values(resolutions), author.trim());
      setDone({ commitId: res.commit_id, message: res.message, changes: res.changes ?? [], oldHead });
    } catch (e) {
      if (e instanceof RequestError) setError(e);
      else throw e;
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <>
        <div className="page-head">
          <h1>✓ Merged</h1>
        </div>
        <div className="banner ok">
          <span><b>{done.message}</b> — commit <span className="mono">{done.commitId.slice(0, 8)}</span>. Validation passed:
            every foreign key resolves, no orphaned constraints or indexes, no duplicate names.</span>
        </div>
        <p className="page-sub">{done.changes.length} change{done.changes.length === 1 ? "" : "s"} landed on <span className="mono">{targetName}</span>:</p>
        <ChangeList changes={done.changes} />
        <div className="actions" style={{ justifyContent: "flex-start", marginTop: "1.2rem" }}>
          <Link className="btn primary" to={`/projects/${projectId}/migration?from=${done.oldHead}&to=${done.commitId}`}>
            View the migration script
          </Link>
          <Link className="btn" to={`/branches/${target}`}>Open {targetName}</Link>
        </div>
      </>
    );
  }

  return (
    <>
      <div className="page-head">
        <h1><Link to={`/projects/${projectId}`}>← project</Link> Merge</h1>
      </div>
      <p className="page-sub">
        A three-way merge against the branches' common ancestor: changes that don't overlap combine automatically;
        real collisions stop here and ask you. Nothing commits until the combined schema passes validation.
      </p>

      <div className="refbar">
        <select value={source} onChange={(e) => setParams({ source: e.target.value, target })} aria-label="Source branch">
          <option value="">merge…</option>
          {branches.filter((b) => b.id !== target).map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
        </select>
        <span aria-hidden>into</span>
        <select value={target} onChange={(e) => setParams({ source, target: e.target.value })} aria-label="Target branch">
          <option value="">…</option>
          {branches.filter((b) => b.id !== source).map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
        </select>
      </div>

      {error && <ErrorBanner error={error} />}
      {busy && !previewed && <div className="loading">Computing the merge…</div>}

      {previewed && (
        <>
          {conflicts.length === 0 && problems.length === 0 && (
            <div className="banner ok">
              <span>✓ Clean merge — {changes.length} change{changes.length === 1 ? "" : "s"} combine automatically, nothing overlaps.</span>
            </div>
          )}

          {conflicts.length > 0 && (
            <>
              <div className="banner warn">
                <span>
                  <b>{conflicts.length} conflict{conflicts.length === 1 ? "" : "s"}</b> — both branches changed the same
                  thing differently. Pick a side, or provide a new value, for each.
                </span>
              </div>
              {conflicts.map((c) => (
                <ConflictCard key={c.id} conflict={c} oursName={targetName} theirsName={sourceName}
                  value={resolutions[c.id]}
                  onChange={(r) => setResolutions({ ...resolutions, [c.id]: r })} />
              ))}
              <div className="actions" style={{ justifyContent: "flex-start" }}>
                <button className="btn" disabled={!allResolved || busy} onClick={() => preview(Object.values(resolutions))}>
                  Re-check with resolutions
                </button>
              </div>
            </>
          )}

          {problems.length > 0 && (
            <div className="banner error">
              <span>
                <b>The combined schema would be broken</b> — each branch is valid alone, but together:{" "}
                {problems.map((p) => p.message).join("; ")}. Nothing will be committed.
              </span>
            </div>
          )}

          {changes.length > 0 && conflicts.length === 0 && (
            <>
              <p className="page-sub">What lands on <span className="mono">{targetName}</span>:</p>
              <ChangeList changes={changes} />
            </>
          )}

          {(conflicts.length === 0 || allResolved) && problems.length === 0 && (
            <div className="mergebar card">
              <label htmlFor="mg-author" style={{ margin: 0 }}>Your name (optional)</label>
              <input id="mg-author" type="text" value={author} onChange={(e) => setAuthor(e.target.value)} style={{ maxWidth: "16rem" }} />
              <span className="spacer" />
              <button className="btn primary" disabled={busy || changes.length === 0 && conflicts.length === 0 && !allResolved} onClick={execute}>
                {busy ? "Merging…" : `Merge ${sourceName} into ${targetName}`}
              </button>
            </div>
          )}
        </>
      )}
    </>
  );
}

function ConflictCard({
  conflict: c,
  oursName,
  theirsName,
  value,
  onChange,
}: {
  conflict: Conflict;
  oursName: string;
  theirsName: string;
  value?: Resolution;
  onChange: (r: Resolution) => void;
}) {
  const [custom, setCustom] = useState("");
  const pick = (choice: "ours" | "theirs" | "custom", customValue?: string) =>
    onChange({ conflict_id: c.id, choice, custom: customValue });

  return (
    <div className="card conflict-card">
      <header>
        <span className="badge ck">{c.class.replace(/_/g, " ")}</span>
        <span className="mono">{c.table}{c.object ? "." + c.object : ""}</span>
        <span className="cprop">{c.property}</span>
      </header>
      <p className="cdesc">{c.description}</p>
      <div className="threeway">
        <div className="side base">
          <div className="side-label">base (common ancestor)</div>
          <div className="side-value mono">{c.base || "—"}</div>
        </div>
        <button className={"side pickable" + (value?.choice === "ours" ? " picked" : "")} onClick={() => pick("ours")}>
          <div className="side-label">{oursName} (ours)</div>
          <div className="side-value mono">{c.ours || "—"}</div>
        </button>
        <button className={"side pickable" + (value?.choice === "theirs" ? " picked" : "")} onClick={() => pick("theirs")}>
          <div className="side-label">{theirsName} (theirs)</div>
          <div className="side-value mono">{c.theirs || "—"}</div>
        </button>
      </div>
      {c.allow_custom && (
        <div className="customrow">
          <label className="inline">
            <input type="radio" checked={value?.choice === "custom"} onChange={() => custom.trim() && pick("custom", custom.trim())} />
            Neither — provide a {c.custom_kind ?? "value"}:
          </label>
          <input type="text" value={custom} placeholder={placeholderFor(c.custom_kind)}
            onChange={(e) => {
              setCustom(e.target.value);
              if (e.target.value.trim()) pick("custom", e.target.value.trim());
            }} />
        </div>
      )}
      {value && <div className="picked-note">✓ resolved: {value.choice === "custom" ? `"${value.custom}"` : value.choice === "ours" ? oursName : theirsName}</div>}
    </div>
  );
}

function placeholderFor(kind?: string): string {
  switch (kind) {
    case "type": return "e.g. varchar(1000)";
    case "default": return "e.g. 'pending'";
    default: return "e.g. a new name";
  }
}
