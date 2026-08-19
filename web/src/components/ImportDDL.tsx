import { useState } from "react";
import { api, RequestError } from "../api";
import type { Proposal } from "../types";
import { ErrorBanner } from "../pages/Home";

// ImportDDL re-imports pasted SQL onto a branch. If the matcher finds
// plausible renames it asks — rename (keeps identity and, on a real
// database, the data) or drop + add — never guessing silently. High-
// confidence proposals preselect "rename"; the user always confirms.
export function ImportDDL({
  branchId,
  onClose,
  onCommitted,
}: {
  branchId: string;
  onClose: () => void;
  onCommitted: () => void;
}) {
  const [ddl, setDdl] = useState("");
  const [author, setAuthor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<RequestError | null>(null);
  const [proposals, setProposals] = useState<Proposal[] | null>(null);
  const [answers, setAnswers] = useState<Record<string, boolean>>({});

  async function submit(decisions: { old_id: string; rename: boolean }[]) {
    setBusy(true);
    setError(null);
    try {
      const res = await api.importDDL(branchId, ddl, decisions, author.trim());
      if (res.needs_confirmation && res.proposals) {
        setProposals(res.proposals);
        const pre: Record<string, boolean> = {};
        for (const p of res.proposals) pre[p.old_id] = p.confidence >= 0.7;
        setAnswers(pre);
      } else {
        onCommitted();
        onClose();
      }
    } catch (e) {
      if (e instanceof RequestError) setError(e);
      else throw e;
    } finally {
      setBusy(false);
    }
  }

  if (proposals) {
    return (
      <div className="modal-backdrop" onClick={onClose}>
        <div className="modal" onClick={(e) => e.stopPropagation()}>
          <h2>Possible renames detected</h2>
          <p className="sub">
            These objects look renamed rather than dropped and re-created. Confirming a rename keeps the object's
            identity and history — and on a real database, its data. Mergebase never decides this silently.
          </p>
          {proposals.map((p) => (
            <div key={p.old_id} className="proposal">
              <div className="proposal-head">
                <span className="mono">{p.kind === "column" ? `${p.table}.` : ""}{p.old_name}</span>
                <span aria-hidden>→</span>
                <span className="mono">{p.kind === "column" ? `${p.table}.` : ""}{p.new_name}</span>
                <span className="badge idx">{Math.round(p.confidence * 100)}% match</span>
              </div>
              <div className="proposal-choice">
                <label className="inline">
                  <input type="radio" name={p.old_id} checked={answers[p.old_id] === true}
                    onChange={() => setAnswers({ ...answers, [p.old_id]: true })} />
                  Rename (keep identity)
                </label>
                <label className="inline">
                  <input type="radio" name={p.old_id} checked={answers[p.old_id] === false}
                    onChange={() => setAnswers({ ...answers, [p.old_id]: false })} />
                  Drop + add (new object)
                </label>
              </div>
            </div>
          ))}
          {error && <ErrorBanner error={error} />}
          <div className="actions">
            <button className="btn" onClick={() => setProposals(null)}>Back</button>
            <button className="btn primary" disabled={busy}
              onClick={() => submit(proposals.map((p) => ({ old_id: p.old_id, rename: answers[p.old_id] ?? false })))}>
              {busy ? "Importing…" : "Confirm and import"}
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Import DDL onto this branch</h2>
        <p className="sub">
          Paste the full schema as PostgreSQL DDL. It replaces this branch's schema as a new commit; objects are
          matched against the current version so identity (and rename history) survives.
        </p>
        <label htmlFor="imp-ddl">PostgreSQL DDL</label>
        <textarea id="imp-ddl" value={ddl} onChange={(e) => setDdl(e.target.value)} spellCheck={false} placeholder="CREATE TABLE …" />
        <label htmlFor="imp-author">Your name (optional)</label>
        <input id="imp-author" type="text" value={author} onChange={(e) => setAuthor(e.target.value)} />
        {error && <ErrorBanner error={error} />}
        <div className="actions">
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn primary" disabled={busy || !ddl.trim()} onClick={() => submit([])}>
            {busy ? "Importing…" : "Import"}
          </button>
        </div>
      </div>
    </div>
  );
}
