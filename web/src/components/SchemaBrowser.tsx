import type { Column, ObjectID, Schema, Table } from "../types";
import { typeString } from "../types";
import { TableGlyph } from "../App";

// SchemaBrowser renders the whole schema as table cards. All references in
// the model are by ID; names are resolved here, at display time.
export function SchemaBrowser({ schema }: { schema: Schema }) {
  const tableByID = new Map<ObjectID, Table>(schema.tables.map((t) => [t.id, t]));
  return (
    <div className="schema-grid">
      {schema.tables.map((t) => (
        <TableCard key={t.id} table={t} tableByID={tableByID} />
      ))}
    </div>
  );
}

function TableCard({ table, tableByID }: { table: Table; tableByID: Map<ObjectID, Table> }) {
  const colByID = new Map<ObjectID, Column>(table.columns.map((c) => [c.id, c]));
  const colNames = (ids?: ObjectID[]) => (ids ?? []).map((id) => colByID.get(id)?.name ?? "?").join(", ");

  const pkCols = new Set(
    (table.constraints ?? []).filter((c) => c.kind === "primary_key").flatMap((c) => c.column_ids ?? []),
  );
  const fkByCol = new Map<ObjectID, string>();
  for (const c of table.constraints ?? []) {
    if (c.kind !== "foreign_key") continue;
    const target = c.ref_table_id ? tableByID.get(c.ref_table_id) : undefined;
    for (const colID of c.column_ids ?? []) {
      fkByCol.set(colID, target ? target.name : "?");
    }
  }
  const uniqueCols = new Set(
    (table.constraints ?? [])
      .filter((c) => c.kind === "unique" && (c.column_ids ?? []).length === 1)
      .flatMap((c) => c.column_ids ?? []),
  );

  const columns = [...table.columns].sort((a, b) => a.position - b.position);
  const checks = (table.constraints ?? []).filter((c) => c.kind === "check");
  const multiUniques = (table.constraints ?? []).filter((c) => c.kind === "unique" && (c.column_ids ?? []).length > 1);

  return (
    <div className="card table-card">
      <header>
        <span className="tglyph"><TableGlyph /></span>
        <span className="tname">{table.name}</span>
        <span className="count">{columns.length} cols</span>
      </header>
      {columns.map((col) => (
        <div key={col.id} className="col-row">
          <span className="cname">{col.name}</span>
          <span className="cmeta">
            <span className="ctype">{typeString(col.type)}</span>
            {pkCols.has(col.id) && <span className="badge pk">PK</span>}
            {fkByCol.has(col.id) && <span className="badge fk">FK→{fkByCol.get(col.id)}</span>}
            {uniqueCols.has(col.id) && <span className="badge uq">UQ</span>}
            {!col.nullable && !pkCols.has(col.id) && <span className="cnull">not null</span>}
            {col.default && <span className="cdefault">= {col.default}</span>}
          </span>
        </div>
      ))}
      {(checks.length > 0 || multiUniques.length > 0 || (table.indexes ?? []).length > 0) && (
        <div className="extras">
          {multiUniques.map((c) => (
            <span key={c.id} className="badge uq">
              UQ ({colNames(c.column_ids)})
            </span>
          ))}
          {checks.map((c) => (
            <span key={c.id} className="badge ck" title={c.expr}>
              CHECK {c.expr && c.expr.length > 28 ? c.expr.slice(0, 28) + "…" : c.expr}
            </span>
          ))}
          {(table.indexes ?? []).map((ix) => (
            <span key={ix.id} className="badge idx" title={ix.name}>
              {ix.unique ? "UNIQUE " : ""}IDX ({ix.columns.map((ic) => (colByID.get(ic.column_id)?.name ?? "?") + (ic.desc ? " ↓" : "")).join(", ")})
            </span>
          ))}
        </div>
      )}
    </div>
  );
}
