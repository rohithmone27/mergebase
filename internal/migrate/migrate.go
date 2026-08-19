// Package migrate emits the ordered PostgreSQL DDL that carries one schema
// version to another. Ordering is a fixed phase sequence, not a dependency
// sort: a topological sort with inline FKs breaks on circular foreign keys
// (two tables referencing each other cannot both be created first, in any
// order), while the phase order below is cycle-proof by construction and
// deterministic:
//
//	1 drop indexes  2 drop FKs  3 drop other constraints  4 drop columns
//	5 drop tables   6 rename tables  7 rename columns
//	8 create tables (no FKs)  9 add columns  10 alter columns
//	11 add non-FK constraints  12 add FKs  13 create indexes
//
// Renames emit ALTER … RENAME — the payoff of identity tracking, since a
// drop-and-add would destroy the column's data. Rename collisions (two
// renames swapping names, or landing on a vacated name) are broken with
// temporary names.
//
// The guarantee is schema-level: the script transforms the old schema
// *shape* into the new one. A structurally correct script can still be
// rejected by a database holding data — those cases are surfaced as
// explicit warnings instead of pretended away:
// retype without USING, SET NOT NULL over existing nulls, and
// ADD COLUMN NOT NULL without a default.
package migrate

import (
	"fmt"
	"slices"
	"strings"

	"mergebase/internal/schema"
)

// Statement is one emitted DDL statement with its phase, for display grouping.
type Statement struct {
	Phase string `json:"phase"`
	SQL   string `json:"sql"`
}

// Warning flags a statement that is structurally correct but data-dependent.
type Warning struct {
	Code    string `json:"code"` // retype_needs_using | set_not_null | add_not_null_no_default
	Message string `json:"message"`
	SQL     string `json:"sql"`
}

// Script is the ordered migration.
type Script struct {
	Statements []Statement `json:"statements"`
	Warnings   []Warning   `json:"warnings"`
}

// SQL renders the script as runnable text, phase-grouped, with warnings as
// comments directly above the statements they concern.
func (s *Script) SQL() string {
	if len(s.Statements) == 0 {
		return "-- No changes: the schemas are identical.\n"
	}
	warnBySQL := map[string][]string{}
	for _, w := range s.Warnings {
		warnBySQL[w.SQL] = append(warnBySQL[w.SQL], w.Message)
	}
	var b strings.Builder
	phase := ""
	for _, st := range s.Statements {
		if st.Phase != phase {
			phase = st.Phase
			fmt.Fprintf(&b, "\n-- %s\n", phase)
		}
		for _, w := range warnBySQL[st.SQL] {
			fmt.Fprintf(&b, "-- WARNING: %s\n", w)
		}
		b.WriteString(st.SQL)
		b.WriteString("\n")
	}
	return strings.TrimLeft(b.String(), "\n")
}

// Generate computes the migration from one snapshot to another.
func Generate(from, to *schema.Schema) *Script {
	g := &gen{from: from, to: to, script: &Script{Statements: []Statement{}, Warnings: []Warning{}}}
	g.run()
	return g.script
}

type gen struct {
	from, to *schema.Schema
	script   *Script
}

func (g *gen) emit(phase, format string, args ...any) string {
	sql := fmt.Sprintf(format, args...)
	g.script.Statements = append(g.script.Statements, Statement{Phase: phase, SQL: sql})
	return sql
}

func (g *gen) warn(code, sql, format string, args ...any) {
	g.script.Warnings = append(g.script.Warnings, Warning{Code: code, SQL: sql, Message: fmt.Sprintf(format, args...)})
}

func (g *gen) run() {
	// Working name maps track what each table/column is called at the point
	// in the script we are emitting — drops and renames use the FROM names,
	// later phases the TO names.
	fromTables := map[schema.ObjectID]*schema.Table{}
	for i := range g.from.Tables {
		fromTables[g.from.Tables[i].ID] = &g.from.Tables[i]
	}
	toTables := map[schema.ObjectID]*schema.Table{}
	for i := range g.to.Tables {
		toTables[g.to.Tables[i].ID] = &g.to.Tables[i]
	}

	// --- phase 1–3: drops of indexes and constraints (modified = drop + re-add) ---
	for _, ft := range g.from.Tables {
		tt := toTables[ft.ID]
		for _, ix := range ft.Indexes {
			if tt == nil || tt.IndexByID(ix.ID) == nil || !indexEqual(ix, *tt.IndexByID(ix.ID)) {
				if tt == nil {
					continue // the whole table drops; its indexes go with it
				}
				g.emit("drop indexes", "DROP INDEX %s;", ident(ix.Name))
			}
		}
		dropCons := func(kinds ...schema.ConstraintKind) {
			for _, c := range ft.Constraints {
				if !kindIn(c.Kind, kinds) {
					continue
				}
				if tt == nil {
					continue // dropped with the table
				}
				surviving := tt.ConstraintByID(c.ID)
				if surviving != nil && constraintEqual(c, *surviving) {
					continue
				}
				g.emit(phaseForDrop(c.Kind), "ALTER TABLE %s DROP CONSTRAINT %s;",
					ident(ft.Name), ident(constraintName(ft.Name, &ft, c)))
			}
		}
		dropCons(schema.ForeignKey)
		dropCons(schema.PrimaryKey, schema.Unique, schema.Check)
	}

	// --- phase 4: drop columns (on surviving tables) ---
	for _, ft := range g.from.Tables {
		tt := toTables[ft.ID]
		if tt == nil {
			continue
		}
		for _, c := range ft.Columns {
			if tt.ColumnByID(c.ID) == nil {
				g.emit("drop columns", "ALTER TABLE %s DROP COLUMN %s;", ident(ft.Name), ident(c.Name))
			}
		}
	}

	// --- phase 5: drop tables ---
	for _, ft := range g.from.Tables {
		if toTables[ft.ID] == nil {
			g.emit("drop tables", "DROP TABLE %s;", ident(ft.Name))
		}
	}

	// --- phase 6: rename tables (collision-safe) ---
	currentTableNames := map[string]schema.ObjectID{}
	for _, ft := range g.from.Tables {
		if toTables[ft.ID] != nil {
			currentTableNames[ft.Name] = ft.ID
		}
	}
	tableRenames := map[schema.ObjectID][2]string{} // id → {current, target}
	for id, ft := range fromTables {
		if tt := toTables[id]; tt != nil && ft.Name != tt.Name {
			tableRenames[id] = [2]string{ft.Name, tt.Name}
		}
	}
	g.applyRenames(tableRenames, currentTableNames, func(oldName, newName string) {
		g.emit("rename tables", "ALTER TABLE %s RENAME TO %s;", ident(oldName), ident(newName))
	})

	// --- phase 7: rename columns (collision-safe, per table) ---
	for _, ft := range g.from.Tables {
		tt := toTables[ft.ID]
		if tt == nil {
			continue
		}
		currentCols := map[string]schema.ObjectID{}
		for _, c := range ft.Columns {
			if tt.ColumnByID(c.ID) != nil {
				currentCols[c.Name] = c.ID
			}
		}
		colRenames := map[schema.ObjectID][2]string{}
		for _, fc := range ft.Columns {
			if tc := tt.ColumnByID(fc.ID); tc != nil && fc.Name != tc.Name {
				colRenames[fc.ID] = [2]string{fc.Name, tc.Name}
			}
		}
		tableName := tt.Name // renamed already in phase 6
		g.applyRenames(colRenames, currentCols, func(oldName, newName string) {
			g.emit("rename columns", "ALTER TABLE %s RENAME COLUMN %s TO %s;",
				ident(tableName), ident(oldName), ident(newName))
		})
	}

	// --- phase 8: create tables, WITHOUT foreign keys ---
	for _, tt := range g.to.Tables {
		if fromTables[tt.ID] == nil {
			g.createTable(tt)
		}
	}

	// --- phase 9: add columns ---
	for _, tt := range g.to.Tables {
		ft := fromTables[tt.ID]
		if ft == nil {
			continue
		}
		for _, c := range tt.Columns {
			if ft.ColumnByID(c.ID) != nil {
				continue
			}
			sql := g.emit("add columns", "ALTER TABLE %s ADD COLUMN %s;", ident(tt.Name), columnDef(c))
			if !c.Nullable && c.Default == "" {
				g.warn("add_not_null_no_default", sql,
					"adding NOT NULL column %q without a default fails on a non-empty table — add a DEFAULT or backfill first", c.Name)
			}
		}
	}

	// --- phase 10: alter columns (type, default, nullability) ---
	for _, tt := range g.to.Tables {
		ft := fromTables[tt.ID]
		if ft == nil {
			continue
		}
		for _, tc := range tt.Columns {
			fc := ft.ColumnByID(tc.ID)
			if fc == nil {
				continue
			}
			if !fc.Type.Equal(tc.Type) {
				sql := g.emit("alter columns", "ALTER TABLE %s ALTER COLUMN %s TYPE %s;",
					ident(tt.Name), ident(tc.Name), tc.Type.String())
				g.warn("retype_needs_using", sql,
					"changing %q from %s to %s may need a USING clause when the cast is not implicit", tc.Name, fc.Type, tc.Type)
			}
			if fc.Default != tc.Default {
				if tc.Default == "" {
					g.emit("alter columns", "ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", ident(tt.Name), ident(tc.Name))
				} else {
					g.emit("alter columns", "ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", ident(tt.Name), ident(tc.Name), tc.Default)
				}
			}
			if fc.Nullable != tc.Nullable {
				if tc.Nullable {
					g.emit("alter columns", "ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", ident(tt.Name), ident(tc.Name))
				} else {
					sql := g.emit("alter columns", "ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", ident(tt.Name), ident(tc.Name))
					g.warn("set_not_null", sql,
						"SET NOT NULL on %q fails if the table already holds NULLs in that column", tc.Name)
				}
			}
		}
	}

	// --- phase 11 + 12: add constraints, non-FK first, then FKs (all tables) ---
	addCons := func(phase string, kinds ...schema.ConstraintKind) {
		for _, tt := range g.to.Tables {
			ft := fromTables[tt.ID]
			for _, c := range tt.Constraints {
				if !kindIn(c.Kind, kinds) {
					continue
				}
				isNew := ft == nil || ft.ConstraintByID(c.ID) == nil
				changed := !isNew && !constraintEqual(*ft.ConstraintByID(c.ID), c)
				// New tables get non-FK constraints inline in CREATE TABLE;
				// only their FKs are deferred here.
				if ft == nil && c.Kind != schema.ForeignKey {
					continue
				}
				if isNew || changed {
					g.emit(phase, "ALTER TABLE %s ADD %s;", ident(tt.Name), constraintSQL(g.to, &tt, c))
				}
			}
		}
	}
	addCons("add constraints", schema.PrimaryKey, schema.Unique, schema.Check)
	addCons("add foreign keys", schema.ForeignKey)

	// --- phase 13: create indexes ---
	for _, tt := range g.to.Tables {
		ft := fromTables[tt.ID]
		for _, ix := range tt.Indexes {
			isNew := ft == nil || ft.IndexByID(ix.ID) == nil
			changed := !isNew && !indexEqual(*ft.IndexByID(ix.ID), ix)
			if isNew || changed {
				g.emit("create indexes", "%s;", indexSQL(&tt, ix))
			}
		}
	}
}

// applyRenames orders renames so no step lands on an occupied name; cycles
// (a↔b swaps) break via a temporary name.
func (g *gen) applyRenames(renames map[schema.ObjectID][2]string, occupied map[string]schema.ObjectID, emit func(oldName, newName string)) {
	pending := map[schema.ObjectID][2]string{}
	var order []schema.ObjectID // deterministic iteration
	for id, r := range renames {
		pending[id] = r
		order = append(order, id)
	}
	sortIDs(order)

	tmpCounter := 0
	for len(pending) > 0 {
		progressed := false
		for _, id := range order {
			r, ok := pending[id]
			if !ok {
				continue
			}
			holder, taken := occupied[r[1]]
			if taken && holder != id {
				continue // target name still occupied by another object
			}
			emit(r[0], r[1])
			delete(occupied, r[0])
			occupied[r[1]] = id
			delete(pending, id)
			progressed = true
		}
		if !progressed {
			// Cycle: move one object aside to a temp name.
			var pick schema.ObjectID
			for _, id := range order {
				if _, ok := pending[id]; ok {
					pick = id
					break
				}
			}
			r := pending[pick]
			tmpCounter++
			tmp := fmt.Sprintf("__mergebase_tmp_%d", tmpCounter)
			emit(r[0], tmp)
			delete(occupied, r[0])
			occupied[tmp] = pick
			pending[pick] = [2]string{tmp, r[1]}
		}
	}
}

func (g *gen) createTable(t schema.Table) {
	var lines []string
	for _, c := range t.Columns {
		lines = append(lines, "    "+columnDef(c))
	}
	for _, c := range t.Constraints {
		if c.Kind == schema.ForeignKey {
			continue // deferred to the FK phase so circular references work
		}
		lines = append(lines, "    "+constraintSQL(g.to, &t, c))
	}
	g.emit("create tables", "CREATE TABLE %s (\n%s\n);", ident(t.Name), strings.Join(lines, ",\n"))
}

// ---- SQL rendering helpers ----

func ident(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func identList(t *schema.Table, ids []schema.ObjectID) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if c := t.ColumnByID(id); c != nil {
			parts = append(parts, ident(c.Name))
		} else {
			parts = append(parts, "?")
		}
	}
	return strings.Join(parts, ", ")
}

func columnDef(c schema.Column) string {
	out := ident(c.Name) + " " + c.Type.String()
	if !c.Nullable {
		out += " NOT NULL"
	}
	if c.Default != "" {
		out += " DEFAULT " + c.Default
	}
	return out
}

func constraintSQL(s *schema.Schema, t *schema.Table, c schema.Constraint) string {
	name := c.Name
	if name == "" {
		name = constraintName(t.Name, t, c)
	}
	prefix := "CONSTRAINT " + ident(name) + " "
	switch c.Kind {
	case schema.PrimaryKey:
		return prefix + "PRIMARY KEY (" + identList(t, c.ColumnIDs) + ")"
	case schema.Unique:
		return prefix + "UNIQUE (" + identList(t, c.ColumnIDs) + ")"
	case schema.Check:
		return prefix + "CHECK (" + c.Expr + ")"
	case schema.ForeignKey:
		target := s.TableByID(c.RefTableID)
		targetName, refCols := "?", "?"
		if target != nil {
			targetName = ident(target.Name)
			refCols = identList(target, c.RefColumnIDs)
		}
		out := prefix + "FOREIGN KEY (" + identList(t, c.ColumnIDs) + ") REFERENCES " + targetName + " (" + refCols + ")"
		out += refAction(" ON DELETE", c.OnDelete)
		out += refAction(" ON UPDATE", c.OnUpdate)
		return out
	}
	return prefix + string(c.Kind)
}

func refAction(prefix string, a schema.ReferentialAction) string {
	if a == "" || a == schema.NoAction {
		return ""
	}
	return prefix + " " + strings.ToUpper(strings.ReplaceAll(string(a), "_", " "))
}

func indexSQL(t *schema.Table, ix schema.Index) string {
	out := "CREATE "
	if ix.Unique {
		out += "UNIQUE "
	}
	out += "INDEX " + ident(ix.Name) + " ON " + ident(t.Name)
	if ix.Method != "" {
		out += " USING " + ix.Method
	}
	cols := make([]string, 0, len(ix.Columns))
	for _, ic := range ix.Columns {
		name := "?"
		if c := t.ColumnByID(ic.ColumnID); c != nil {
			name = ident(c.Name)
		}
		if ic.Desc {
			name += " DESC"
		}
		cols = append(cols, name)
	}
	return out + " (" + strings.Join(cols, ", ") + ")"
}

// constraintName supplies PostgreSQL's default constraint name when the
// model has none recorded — the honest best answer for DROP CONSTRAINT on
// a constraint that was created unnamed.
func constraintName(tableName string, t *schema.Table, c schema.Constraint) string {
	if c.Name != "" {
		return c.Name
	}
	firstCol := ""
	if len(c.ColumnIDs) > 0 {
		if col := t.ColumnByID(c.ColumnIDs[0]); col != nil {
			firstCol = col.Name
		}
	}
	switch c.Kind {
	case schema.PrimaryKey:
		return tableName + "_pkey"
	case schema.Unique:
		return tableName + "_" + firstCol + "_key"
	case schema.ForeignKey:
		return tableName + "_" + firstCol + "_fkey"
	default:
		return tableName + "_" + firstCol + "_check"
	}
}

func phaseForDrop(k schema.ConstraintKind) string {
	if k == schema.ForeignKey {
		return "drop foreign keys"
	}
	return "drop constraints"
}

func kindIn(k schema.ConstraintKind, kinds []schema.ConstraintKind) bool {
	for _, kk := range kinds {
		if k == kk {
			return true
		}
	}
	return false
}

func constraintEqual(a, b schema.Constraint) bool {
	if a.Kind != b.Kind || a.Expr != b.Expr || a.OnDelete != b.OnDelete || a.OnUpdate != b.OnUpdate ||
		a.RefTableID != b.RefTableID || len(a.ColumnIDs) != len(b.ColumnIDs) || len(a.RefColumnIDs) != len(b.RefColumnIDs) {
		return false
	}
	for i := range a.ColumnIDs {
		if a.ColumnIDs[i] != b.ColumnIDs[i] {
			return false
		}
	}
	for i := range a.RefColumnIDs {
		if a.RefColumnIDs[i] != b.RefColumnIDs[i] {
			return false
		}
	}
	return true
}

func indexEqual(a, b schema.Index) bool {
	if a.Name != b.Name || a.Unique != b.Unique || a.Method != b.Method || len(a.Columns) != len(b.Columns) {
		return false
	}
	for i := range a.Columns {
		if a.Columns[i] != b.Columns[i] {
			return false
		}
	}
	return true
}

func sortIDs(ids []schema.ObjectID) { slices.Sort(ids) }
