import type { Change } from "../types";

// ChangeList renders semantic changes grouped per table — every row is one
// meaningful change written as a sentence, with add/remove/modify coloring.
export function ChangeList({ changes }: { changes: Change[] }) {
  const byTable = new Map<string, Change[]>();
  for (const c of changes) {
    const list = byTable.get(c.table) ?? [];
    list.push(c);
    byTable.set(c.table, list);
  }
  return (
    <div className="change-groups">
      {Array.from(byTable.entries()).map(([table, list]) => (
        <div key={table} className="card change-group">
          <header className="mono">{table}</header>
          {list.map((c, i) => (
            <div key={i} className={"change-row " + sign(c.kind)}>
              <span className="gutter">{glyph(c.kind)}</span>
              <span className="ctext">
                {c.text}
                {c.from && c.to && c.kind !== "column_renamed" && c.kind !== "table_renamed" ? (
                  <span className="fromto mono"> {c.from} → {c.to}</span>
                ) : null}
              </span>
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

function sign(kind: string): string {
  if (kind.endsWith("added")) return "add";
  if (kind.endsWith("dropped")) return "del";
  return "mod";
}

function glyph(kind: string): string {
  if (kind.endsWith("added")) return "+";
  if (kind.endsWith("dropped")) return "−";
  return "~";
}
