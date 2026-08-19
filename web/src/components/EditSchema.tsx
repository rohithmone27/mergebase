import { useMemo, useState } from "react";
import { api, RequestError } from "../api";
import type { DataType, ObjectID, Op, Schema, Table } from "../types";
import { typeString } from "../types";
import { ErrorBanner } from "../pages/Home";

// EditSchema is the operation composer: pick an operation, fill its fields,
// queue it, repeat, commit once. Every operation the brief names is here.
// Queued operations are described in plain words so the user reviews intent,
// not JSON.

const OP_LABELS: Record<string, string> = {
  create_table: "Create table",
  drop_table: "Drop table",
  rename_table: "Rename table",
  add_column: "Add column",
  drop_column: "Drop column",
  rename_column: "Rename column",
  retype_column: "Change column type",
  set_nullable: "Set nullability",
  set_default: "Set default",
  add_constraint: "Add constraint",
  drop_constraint: "Drop constraint",
  add_index: "Add index",
  drop_index: "Drop index",
};

function parseTypeInput(s: string): DataType | null {
  const m = s.trim().match(/^([a-zA-Z ]+?)\s*(?:\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\))?$/);
  if (!m || !m[1].trim()) return null;
  const dt: DataType = { base: m[1].trim().toLowerCase() };
  const params = [m[2], m[3]].filter(Boolean).map(Number);
  if (params.length) dt.params = params;
  return dt;
}

interface ColumnRow {
  name: string;
  type: string;
  nullable: boolean;
  def: string;
}

export function EditSchema({
  branchId,
  schema,
  onClose,
  onCommitted,
}: {
  branchId: string;
  schema: Schema;
  onClose: () => void;
  onCommitted: () => void;
}) {
  const [queue, setQueue] = useState<{ op: Op; text: string }[]>([]);
  const [message, setMessage] = useState("");
  const [author, setAuthor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<RequestError | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [opKind, setOpKind] = useState("add_column");

  // Working copy of the schema so queued-but-uncommitted operations show up
  // in the pickers (e.g. add a table, then add a column to it).
  const working = useMemo(() => {
    // The server is the source of truth; this preview only tracks the
    // additions/renames pickers need. Failures surface on commit.
    const w: Schema = JSON.parse(JSON.stringify(schema));
    for (const { op } of queue) {
      applyPreview(w, op);
    }
    return w;
  }, [schema, queue]);

  const [tableID, setTableID] = useState<ObjectID>("");
  const table = working.tables.find((t) => t.id === tableID);

  function push(op: Op, text: string) {
    setQueue((q) => [...q, { op, text }]);
    setFormError(null);
  }

  async function commit() {
    setBusy(true);
    setError(null);
    try {
      await api.applyChanges(
        branchId,
        queue.map((q) => q.op),
        message.trim(),
        author.trim(),
      );
      onCommitted();
      onClose();
    } catch (e) {
      if (e instanceof RequestError) setError(e);
      else throw e;
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <h2>Edit schema</h2>
        <p className="sub">
          Queue one or more operations, then commit them together as one version. Renames keep the object's identity —
          diffs and merges will read them as renames, never as drop&nbsp;+&nbsp;add.
        </p>

        <label htmlFor="op-kind">Operation</label>
        <select id="op-kind" value={opKind} onChange={(e) => { setOpKind(e.target.value); setFormError(null); }}>
          {Object.entries(OP_LABELS).map(([k, v]) => (
            <option key={k} value={k}>{v}</option>
          ))}
        </select>

        {opKind !== "create_table" && (
          <>
            <label htmlFor="op-table">Table</label>
            <select id="op-table" value={tableID} onChange={(e) => setTableID(e.target.value)}>
              <option value="">— pick a table —</option>
              {working.tables.map((t) => (
                <option key={t.id} value={t.id}>{t.name}</option>
              ))}
            </select>
          </>
        )}

        <OpForm key={opKind + tableID} kind={opKind} table={table} schema={working} onQueue={push} onError={setFormError} />
        {formError && <div className="banner error" role="alert">{formError}</div>}

        {queue.length > 0 && (
          <>
            <label>Queued operations</label>
            <ol className="op-queue">
              {queue.map((q, i) => (
                <li key={i}>
                  <span>{q.text}</span>
                  <button className="btn quiet" onClick={() => setQueue(queue.filter((_, j) => j !== i))}>remove</button>
                </li>
              ))}
            </ol>
            <label htmlFor="op-msg">Commit message (optional — generated from the operations if empty)</label>
            <input id="op-msg" type="text" value={message} onChange={(e) => setMessage(e.target.value)} placeholder="e.g. Prepare billing columns" />
            <label htmlFor="op-author">Your name (optional)</label>
            <input id="op-author" type="text" value={author} onChange={(e) => setAuthor(e.target.value)} />
          </>
        )}

        {error && <ErrorBanner error={error} />}
        <div className="actions">
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn primary" disabled={busy || queue.length === 0} onClick={commit}>
            {busy ? "Committing…" : `Commit ${queue.length || ""} change${queue.length === 1 ? "" : "s"}`}
          </button>
        </div>
      </div>
    </div>
  );
}

// applyPreview keeps pickers usable for queued ops; it mirrors only the
// structural effects the pickers care about.
function applyPreview(w: Schema, op: Op) {
  const t = w.tables.find((x) => x.id === op.table_id);
  switch (op.op) {
    case "create_table":
      w.tables.push({
        id: "queued:" + (op.name ?? ""),
        name: op.name ?? "",
        columns: (op.columns ?? []).map((c, i) => ({
          id: "queued:" + (op.name ?? "") + ":" + c.name,
          name: c.name, type: c.type, nullable: c.nullable, default: c.default, position: i + 1,
        })),
      });
      break;
    case "drop_table":
      w.tables = w.tables.filter((x) => x.id !== op.table_id);
      break;
    case "rename_table":
      if (t) t.name = op.name ?? t.name;
      break;
    case "add_column":
      if (t && op.column)
        t.columns.push({
          id: "queued:" + t.id + ":" + op.column.name,
          name: op.column.name, type: op.column.type, nullable: op.column.nullable,
          default: op.column.default, position: t.columns.length + 1,
        });
      break;
    case "drop_column":
      if (t) t.columns = t.columns.filter((c) => c.id !== op.column_id);
      break;
    case "rename_column": {
      const c = t?.columns.find((c) => c.id === op.column_id);
      if (c) c.name = op.name ?? c.name;
      break;
    }
  }
}

function OpForm({
  kind,
  table,
  schema,
  onQueue,
  onError,
}: {
  kind: string;
  table?: Table;
  schema: Schema;
  onQueue: (op: Op, text: string) => void;
  onError: (msg: string | null) => void;
}) {
  const [name, setName] = useState("");
  const [colID, setColID] = useState("");
  const [typeStr, setTypeStr] = useState("");
  const [nullable, setNullable] = useState(true);
  const [def, setDef] = useState("");
  const [rows, setRows] = useState<ColumnRow[]>([{ name: "id", type: "bigint", nullable: false, def: "" }]);
  const [consKind, setConsKind] = useState("unique");
  const [consCols, setConsCols] = useState<string[]>([]);
  const [refTableID, setRefTableID] = useState("");
  const [refCols, setRefCols] = useState<string[]>([]);
  const [expr, setExpr] = useState("");
  const [consID, setConsID] = useState("");
  const [indexID, setIndexID] = useState("");
  const [unique, setUnique] = useState(false);

  const col = table?.columns.find((c) => c.id === colID);
  const refTable = schema.tables.find((t) => t.id === refTableID);

  const needTable = kind !== "create_table";
  if (needTable && !table) return <p className="sub">Pick a table above to continue.</p>;

  function queue() {
    onError(null);
    const t = table!;
    switch (kind) {
      case "create_table": {
        if (!name.trim()) return onError("The new table needs a name.");
        const columns = [];
        for (const r of rows) {
          if (!r.name.trim()) return onError("Every column needs a name.");
          const dt = parseTypeInput(r.type);
          if (!dt) return onError(`"${r.type}" is not a type — try text, varchar(255), numeric(10,2)…`);
          columns.push({ name: r.name.trim(), type: dt, nullable: r.nullable, default: r.def.trim() || undefined });
        }
        onQueue({ op: "create_table", name: name.trim(), columns }, `create table ${name.trim()} (${columns.length} columns)`);
        setName("");
        return;
      }
      case "drop_table":
        return onQueue({ op: "drop_table", table_id: t.id }, `drop table ${t.name}`);
      case "rename_table":
        if (!name.trim()) return onError("Enter the new table name.");
        onQueue({ op: "rename_table", table_id: t.id, name: name.trim() }, `rename table ${t.name} → ${name.trim()}`);
        return setName("");
      case "add_column": {
        if (!name.trim()) return onError("The new column needs a name.");
        const dt = parseTypeInput(typeStr);
        if (!dt) return onError(`"${typeStr}" is not a type — try text, varchar(255), numeric(10,2)…`);
        onQueue(
          { op: "add_column", table_id: t.id, column: { name: name.trim(), type: dt, nullable, default: def.trim() || undefined } },
          `add column ${t.name}.${name.trim()} ${typeString(dt)}`,
        );
        setName(""); setTypeStr(""); setDef("");
        return;
      }
      case "drop_column":
        if (!col) return onError("Pick the column to drop.");
        return onQueue({ op: "drop_column", table_id: t.id, column_id: col.id }, `drop column ${t.name}.${col.name}`);
      case "rename_column":
        if (!col) return onError("Pick the column to rename.");
        if (!name.trim()) return onError("Enter the new column name.");
        onQueue({ op: "rename_column", table_id: t.id, column_id: col.id, name: name.trim() },
          `rename ${t.name}.${col.name} → ${name.trim()}`);
        return setName("");
      case "retype_column": {
        if (!col) return onError("Pick the column.");
        const dt = parseTypeInput(typeStr);
        if (!dt) return onError(`"${typeStr}" is not a type — try text, varchar(255), numeric(10,2)…`);
        onQueue({ op: "retype_column", table_id: t.id, column_id: col.id, type: dt },
          `change type of ${t.name}.${col.name}: ${typeString(col.type)} → ${typeString(dt)}`);
        return setTypeStr("");
      }
      case "set_nullable":
        if (!col) return onError("Pick the column.");
        return onQueue({ op: "set_nullable", table_id: t.id, column_id: col.id, nullable },
          `set ${t.name}.${col.name} ${nullable ? "nullable" : "NOT NULL"}`);
      case "set_default":
        if (!col) return onError("Pick the column.");
        return onQueue({ op: "set_default", table_id: t.id, column_id: col.id, default: def.trim() },
          def.trim() ? `set default of ${t.name}.${col.name} to ${def.trim()}` : `clear default of ${t.name}.${col.name}`);
      case "add_constraint": {
        if (consKind === "check") {
          if (!expr.trim()) return onError("A CHECK constraint needs an expression.");
          return onQueue({ op: "add_constraint", table_id: t.id, constraint: { kind: "check", expr: expr.trim() } },
            `add CHECK (${expr.trim()}) on ${t.name}`);
        }
        if (consCols.length === 0) return onError("Pick at least one column.");
        if (consKind === "foreign_key") {
          if (!refTable) return onError("Pick the referenced table.");
          if (refCols.length !== consCols.length) return onError("Local and referenced column counts must match.");
          return onQueue({
            op: "add_constraint", table_id: t.id,
            constraint: { kind: "foreign_key", column_ids: consCols, ref_table_id: refTable.id, ref_column_ids: refCols },
          }, `add FOREIGN KEY on ${t.name} → ${refTable.name}`);
        }
        return onQueue(
          { op: "add_constraint", table_id: t.id, constraint: { kind: consKind as "primary_key" | "unique", column_ids: consCols } },
          `add ${consKind.replace("_", " ").toUpperCase()} on ${t.name}`,
        );
      }
      case "drop_constraint": {
        if (!consID) return onError("Pick the constraint to drop.");
        return onQueue({ op: "drop_constraint", table_id: t.id, constraint_id: consID }, `drop constraint on ${t.name}`);
      }
      case "add_index": {
        if (!name.trim()) return onError("The index needs a name.");
        if (consCols.length === 0) return onError("Pick at least one column.");
        onQueue({
          op: "add_index", table_id: t.id,
          index: { name: name.trim(), columns: consCols.map((c) => ({ column_id: c })), unique },
        }, `add ${unique ? "unique " : ""}index ${name.trim()} on ${t.name}`);
        return setName("");
      }
      case "drop_index":
        if (!indexID) return onError("Pick the index to drop.");
        return onQueue({ op: "drop_index", table_id: t.id, index_id: indexID }, `drop index on ${t.name}`);
    }
  }

  const colPicker = (
    <>
      <label>Column</label>
      <select value={colID} onChange={(e) => setColID(e.target.value)}>
        <option value="">— pick a column —</option>
        {table?.columns.map((c) => (
          <option key={c.id} value={c.id}>{c.name} ({typeString(c.type)})</option>
        ))}
      </select>
    </>
  );

  const multiColPicker = (cols: string[], set: (v: string[]) => void, from?: Table, label = "Columns (⌘/Ctrl-click for several)") => (
    <>
      <label>{label}</label>
      <select multiple size={Math.min(5, (from ?? table)?.columns.length ?? 3)} value={cols}
        onChange={(e) => set(Array.from(e.target.selectedOptions).map((o) => o.value))}>
        {(from ?? table)?.columns.map((c) => (
          <option key={c.id} value={c.id}>{c.name}</option>
        ))}
      </select>
    </>
  );

  return (
    <div className="op-form">
      {kind === "create_table" && (
        <>
          <label>Table name</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="invoices" />
          <label>Columns</label>
          {rows.map((r, i) => (
            <div key={i} className="col-editor">
              <input type="text" placeholder="name" value={r.name} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} />
              <input type="text" placeholder="type e.g. varchar(255)" value={r.type} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, type: e.target.value } : x)))} />
              <label className="inline"><input type="checkbox" checked={!r.nullable} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, nullable: !e.target.checked } : x)))} /> NOT NULL</label>
              <input type="text" placeholder="default (optional)" value={r.def} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, def: e.target.value } : x)))} />
              <button className="btn quiet" onClick={() => setRows(rows.filter((_, j) => j !== i))} disabled={rows.length === 1}>✕</button>
            </div>
          ))}
          <button className="btn" onClick={() => setRows([...rows, { name: "", type: "", nullable: true, def: "" }])}>+ column</button>
        </>
      )}

      {(kind === "rename_table") && (
        <>
          <label>New name</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="accounts" />
        </>
      )}

      {kind === "add_column" && (
        <>
          <label>Column name</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="currency" />
          <label>Type</label>
          <input type="text" value={typeStr} onChange={(e) => setTypeStr(e.target.value)} placeholder="varchar(3)" />
          <label className="inline"><input type="checkbox" checked={!nullable} onChange={(e) => setNullable(!e.target.checked)} /> NOT NULL</label>
          <label>Default (optional, raw SQL — e.g. 'INR' or now())</label>
          <input type="text" value={def} onChange={(e) => setDef(e.target.value)} />
        </>
      )}

      {(kind === "drop_column" || kind === "set_nullable") && colPicker}
      {kind === "set_nullable" && (
        <label className="inline"><input type="checkbox" checked={!nullable} onChange={(e) => setNullable(!e.target.checked)} /> NOT NULL</label>
      )}

      {kind === "rename_column" && (
        <>
          {colPicker}
          <label>New name</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="email_address" />
        </>
      )}

      {kind === "retype_column" && (
        <>
          {colPicker}
          <label>New type</label>
          <input type="text" value={typeStr} onChange={(e) => setTypeStr(e.target.value)} placeholder="text" />
        </>
      )}

      {kind === "set_default" && (
        <>
          {colPicker}
          <label>Default (raw SQL; leave empty to clear)</label>
          <input type="text" value={def} onChange={(e) => setDef(e.target.value)} placeholder="'pending'" />
        </>
      )}

      {kind === "add_constraint" && (
        <>
          <label>Constraint kind</label>
          <select value={consKind} onChange={(e) => setConsKind(e.target.value)}>
            <option value="unique">UNIQUE</option>
            <option value="primary_key">PRIMARY KEY</option>
            <option value="foreign_key">FOREIGN KEY</option>
            <option value="check">CHECK</option>
          </select>
          {consKind === "check" ? (
            <>
              <label>Expression</label>
              <input type="text" value={expr} onChange={(e) => setExpr(e.target.value)} placeholder="total >= 0" />
            </>
          ) : (
            multiColPicker(consCols, setConsCols)
          )}
          {consKind === "foreign_key" && (
            <>
              <label>References table</label>
              <select value={refTableID} onChange={(e) => { setRefTableID(e.target.value); setRefCols([]); }}>
                <option value="">— pick a table —</option>
                {schema.tables.map((t) => (
                  <option key={t.id} value={t.id}>{t.name}</option>
                ))}
              </select>
              {refTable && multiColPicker(refCols, setRefCols, refTable, "Referenced columns")}
            </>
          )}
        </>
      )}

      {kind === "drop_constraint" && (
        <>
          <label>Constraint</label>
          <select value={consID} onChange={(e) => setConsID(e.target.value)}>
            <option value="">— pick a constraint —</option>
            {(table?.constraints ?? []).map((c) => (
              <option key={c.id} value={c.id}>{c.kind.replace("_", " ")}{c.name ? ` (${c.name})` : ""}</option>
            ))}
          </select>
        </>
      )}

      {kind === "add_index" && (
        <>
          <label>Index name</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="idx_orders_currency" />
          {multiColPicker(consCols, setConsCols)}
          <label className="inline"><input type="checkbox" checked={unique} onChange={(e) => setUnique(e.target.checked)} /> UNIQUE</label>
        </>
      )}

      {kind === "drop_index" && (
        <>
          <label>Index</label>
          <select value={indexID} onChange={(e) => setIndexID(e.target.value)}>
            <option value="">— pick an index —</option>
            {(table?.indexes ?? []).map((ix) => (
              <option key={ix.id} value={ix.id}>{ix.name}</option>
            ))}
          </select>
        </>
      )}

      <div style={{ marginTop: "0.7rem" }}>
        <button className="btn" onClick={queue}>Add to queue</button>
      </div>
    </div>
  );
}
