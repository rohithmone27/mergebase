// Package diff computes the semantic difference between two schema
// snapshots. Objects are matched by stable ID, so a rename reads as a
// rename, and a rename plus a retype of the same column reads as one object
// with two changed properties — never as a drop and an add.
//
// Output is deterministic: changes sort by table name, then a fixed kind
// order, then object name. The merge engine does not consume these changes;
// it re-derives per-property comparisons from the three snapshots directly —
// this package is the human-facing account of "what diverged".
package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rohithmone27/mergebase/internal/schema"
)

type ChangeKind string

const (
	TableAdded    ChangeKind = "table_added"
	TableDropped  ChangeKind = "table_dropped"
	TableRenamed  ChangeKind = "table_renamed"
	ColumnAdded   ChangeKind = "column_added"
	ColumnDropped ChangeKind = "column_dropped"
	ColumnRenamed ChangeKind = "column_renamed"
	ColumnRetyped ChangeKind = "column_retyped"
	NullChanged   ChangeKind = "nullability_changed"
	DefChanged    ChangeKind = "default_changed"
	ConsAdded     ChangeKind = "constraint_added"
	ConsDropped   ChangeKind = "constraint_dropped"
	ConsModified  ChangeKind = "constraint_modified"
	IndexAdded    ChangeKind = "index_added"
	IndexDropped  ChangeKind = "index_dropped"
	IndexModified ChangeKind = "index_modified"
)

// Change is one semantic difference, written for humans but structured for
// the UI: Kind + IDs for grouping, From/To for detail, Text as the sentence.
type Change struct {
	Kind     ChangeKind      `json:"kind"`
	Table    string          `json:"table"`
	TableID  schema.ObjectID `json:"table_id"`
	Object   string          `json:"object,omitempty"`
	ObjectID schema.ObjectID `json:"object_id,omitempty"`
	From     string          `json:"from,omitempty"`
	To       string          `json:"to,omitempty"`
	Text     string          `json:"text"`
}

// Diff is the full comparison result.
type Diff struct {
	Changes   []Change `json:"changes"`
	Unchanged int      `json:"unchanged"` // objects present and identical on both sides
}

// Compute returns the semantic changes from `from` to `to`.
func Compute(from, to *schema.Schema) *Diff {
	d := &Diff{Changes: []Change{}}

	fromTables := byID(from.Tables, func(t schema.Table) schema.ObjectID { return t.ID })
	toTables := byID(to.Tables, func(t schema.Table) schema.ObjectID { return t.ID })

	for _, t := range to.Tables {
		if _, ok := fromTables[t.ID]; !ok {
			d.add(Change{Kind: TableAdded, Table: t.Name, TableID: t.ID,
				Text: fmt.Sprintf("added table %s (%d columns)", t.Name, len(t.Columns))})
		}
	}
	for _, t := range from.Tables {
		if _, ok := toTables[t.ID]; !ok {
			d.add(Change{Kind: TableDropped, Table: t.Name, TableID: t.ID,
				Text: fmt.Sprintf("dropped table %s", t.Name)})
		}
	}
	for _, ft := range from.Tables {
		tt, ok := toTables[ft.ID]
		if !ok {
			continue
		}
		d.table(ft, tt, to)
	}

	sort.SliceStable(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if ka, kb := kindOrder(a.Kind), kindOrder(b.Kind); ka != kb {
			return ka < kb
		}
		return a.Object < b.Object
	})
	return d
}

func (d *Diff) add(c Change) { d.Changes = append(d.Changes, c) }

func (d *Diff) table(from, to schema.Table, toSchema *schema.Schema) {
	changedBefore := len(d.Changes)
	if from.Name != to.Name {
		d.add(Change{Kind: TableRenamed, Table: to.Name, TableID: to.ID, From: from.Name, To: to.Name,
			Text: fmt.Sprintf("renamed table %s → %s", from.Name, to.Name)})
	}

	// Columns by ID.
	fromCols := byID(from.Columns, func(c schema.Column) schema.ObjectID { return c.ID })
	toCols := byID(to.Columns, func(c schema.Column) schema.ObjectID { return c.ID })
	for _, c := range to.Columns {
		if _, ok := fromCols[c.ID]; !ok {
			d.add(Change{Kind: ColumnAdded, Table: to.Name, TableID: to.ID, Object: c.Name, ObjectID: c.ID,
				To:   describeColumn(c),
				Text: fmt.Sprintf("added column %s.%s %s", to.Name, c.Name, describeColumn(c))})
		}
	}
	for _, c := range from.Columns {
		if _, ok := toCols[c.ID]; !ok {
			d.add(Change{Kind: ColumnDropped, Table: to.Name, TableID: to.ID, Object: c.Name, ObjectID: c.ID,
				From: describeColumn(c),
				Text: fmt.Sprintf("dropped column %s.%s", to.Name, c.Name)})
		}
	}
	unchangedCols := 0
	for _, fc := range from.Columns {
		tc, ok := toCols[fc.ID]
		if !ok {
			continue
		}
		before := len(d.Changes)
		if fc.Name != tc.Name {
			d.add(Change{Kind: ColumnRenamed, Table: to.Name, TableID: to.ID, Object: tc.Name, ObjectID: tc.ID,
				From: fc.Name, To: tc.Name,
				Text: fmt.Sprintf("renamed %s.%s → %s", to.Name, fc.Name, tc.Name)})
		}
		if !fc.Type.Equal(tc.Type) {
			d.add(Change{Kind: ColumnRetyped, Table: to.Name, TableID: to.ID, Object: tc.Name, ObjectID: tc.ID,
				From: fc.Type.String(), To: tc.Type.String(),
				Text: fmt.Sprintf("changed type of %s.%s: %s → %s", to.Name, tc.Name, fc.Type, tc.Type)})
		}
		if fc.Nullable != tc.Nullable {
			d.add(Change{Kind: NullChanged, Table: to.Name, TableID: to.ID, Object: tc.Name, ObjectID: tc.ID,
				From: nullability(fc.Nullable), To: nullability(tc.Nullable),
				Text: fmt.Sprintf("%s.%s is now %s", to.Name, tc.Name, nullability(tc.Nullable))})
		}
		if fc.Default != tc.Default {
			d.add(Change{Kind: DefChanged, Table: to.Name, TableID: to.ID, Object: tc.Name, ObjectID: tc.ID,
				From: orNone(fc.Default), To: orNone(tc.Default),
				Text: fmt.Sprintf("changed default of %s.%s: %s → %s", to.Name, tc.Name, orNone(fc.Default), orNone(tc.Default))})
		}
		if len(d.Changes) == before {
			unchangedCols++
		}
	}
	d.Unchanged += unchangedCols

	// Constraints by ID; definition compared as a rendered string.
	fromCons := byID(from.Constraints, func(c schema.Constraint) schema.ObjectID { return c.ID })
	toCons := byID(to.Constraints, func(c schema.Constraint) schema.ObjectID { return c.ID })
	for _, c := range to.Constraints {
		def := DescribeConstraint(toSchema, &to, c)
		if _, ok := fromCons[c.ID]; !ok {
			d.add(Change{Kind: ConsAdded, Table: to.Name, TableID: to.ID, Object: def, ObjectID: c.ID,
				To: def, Text: fmt.Sprintf("added %s on %s", def, to.Name)})
		}
	}
	for _, c := range from.Constraints {
		if _, ok := toCons[c.ID]; !ok {
			def := DescribeConstraint(toSchema, &to, c)
			d.add(Change{Kind: ConsDropped, Table: to.Name, TableID: to.ID, Object: def, ObjectID: c.ID,
				From: def, Text: fmt.Sprintf("dropped %s on %s", def, to.Name)})
		}
	}
	for _, fc := range from.Constraints {
		tc, ok := toCons[fc.ID]
		if !ok {
			continue
		}
		fromDef, toDef := DescribeConstraint(toSchema, &to, fc), DescribeConstraint(toSchema, &to, tc)
		if fromDef != toDef {
			d.add(Change{Kind: ConsModified, Table: to.Name, TableID: to.ID, Object: toDef, ObjectID: tc.ID,
				From: fromDef, To: toDef,
				Text: fmt.Sprintf("changed constraint on %s: %s → %s", to.Name, fromDef, toDef)})
		} else {
			d.Unchanged++
		}
	}

	// Indexes by ID.
	fromIdx := byID(from.Indexes, func(i schema.Index) schema.ObjectID { return i.ID })
	toIdx := byID(to.Indexes, func(i schema.Index) schema.ObjectID { return i.ID })
	for _, ix := range to.Indexes {
		if _, ok := fromIdx[ix.ID]; !ok {
			d.add(Change{Kind: IndexAdded, Table: to.Name, TableID: to.ID, Object: ix.Name, ObjectID: ix.ID,
				To: DescribeIndex(&to, ix), Text: fmt.Sprintf("added index %s %s", ix.Name, DescribeIndex(&to, ix))})
		}
	}
	for _, ix := range from.Indexes {
		if _, ok := toIdx[ix.ID]; !ok {
			d.add(Change{Kind: IndexDropped, Table: to.Name, TableID: to.ID, Object: ix.Name, ObjectID: ix.ID,
				From: DescribeIndex(&to, ix), Text: fmt.Sprintf("dropped index %s", ix.Name)})
		}
	}
	for _, fx := range from.Indexes {
		tx, ok := toIdx[fx.ID]
		if !ok {
			continue
		}
		fromDef, toDef := DescribeIndex(&to, fx), DescribeIndex(&to, tx)
		if fromDef != toDef || fx.Name != tx.Name {
			d.add(Change{Kind: IndexModified, Table: to.Name, TableID: to.ID, Object: tx.Name, ObjectID: tx.ID,
				From: fx.Name + " " + fromDef, To: tx.Name + " " + toDef,
				Text: fmt.Sprintf("changed index %s: %s → %s", tx.Name, fromDef, toDef)})
		} else {
			d.Unchanged++
		}
	}

	if len(d.Changes) == changedBefore {
		d.Unchanged++ // the table itself
	}
}

// DescribeConstraint renders a constraint with IDs resolved to names against
// the given schema/table. The rendering doubles as the definition string
// compared for constraint modification.
func DescribeConstraint(s *schema.Schema, t *schema.Table, c schema.Constraint) string {
	cols := columnNames(t, c.ColumnIDs)
	switch c.Kind {
	case schema.PrimaryKey:
		return "PRIMARY KEY (" + cols + ")"
	case schema.Unique:
		return "UNIQUE (" + cols + ")"
	case schema.Check:
		return "CHECK (" + c.Expr + ")"
	case schema.ForeignKey:
		target := s.TableByID(c.RefTableID)
		targetName, refCols := "?", "?"
		if target != nil {
			targetName = target.Name
			refCols = columnNames(target, c.RefColumnIDs)
		}
		out := fmt.Sprintf("FOREIGN KEY (%s) → %s(%s)", cols, targetName, refCols)
		if c.OnDelete != "" && c.OnDelete != schema.NoAction {
			out += " ON DELETE " + strings.ToUpper(strings.ReplaceAll(string(c.OnDelete), "_", " "))
		}
		if c.OnUpdate != "" && c.OnUpdate != schema.NoAction {
			out += " ON UPDATE " + strings.ToUpper(strings.ReplaceAll(string(c.OnUpdate), "_", " "))
		}
		return out
	default:
		return string(c.Kind)
	}
}

// DescribeIndex renders an index definition with IDs resolved to names.
func DescribeIndex(t *schema.Table, ix schema.Index) string {
	parts := make([]string, 0, len(ix.Columns))
	for _, ic := range ix.Columns {
		name := "?"
		if c := t.ColumnByID(ic.ColumnID); c != nil {
			name = c.Name
		}
		if ic.Desc {
			name += " DESC"
		}
		parts = append(parts, name)
	}
	out := "(" + strings.Join(parts, ", ") + ")"
	if ix.Unique {
		out = "UNIQUE " + out
	}
	if ix.Method != "" {
		out += " USING " + ix.Method
	}
	return out
}

func describeColumn(c schema.Column) string {
	out := c.Type.String()
	if !c.Nullable {
		out += " NOT NULL"
	}
	if c.Default != "" {
		out += " DEFAULT " + c.Default
	}
	return out
}

func columnNames(t *schema.Table, ids []schema.ObjectID) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if c := t.ColumnByID(id); c != nil {
			parts = append(parts, c.Name)
		} else {
			parts = append(parts, "?")
		}
	}
	return strings.Join(parts, ", ")
}

func nullability(nullable bool) string {
	if nullable {
		return "nullable"
	}
	return "NOT NULL"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func kindOrder(k ChangeKind) int {
	order := []ChangeKind{
		TableRenamed, TableAdded, TableDropped,
		ColumnAdded, ColumnDropped, ColumnRenamed, ColumnRetyped, NullChanged, DefChanged,
		ConsAdded, ConsDropped, ConsModified,
		IndexAdded, IndexDropped, IndexModified,
	}
	for i, o := range order {
		if o == k {
			return i
		}
	}
	return len(order)
}

func byID[T any](items []T, id func(T) schema.ObjectID) map[schema.ObjectID]T {
	out := make(map[schema.ObjectID]T, len(items))
	for _, it := range items {
		out[id(it)] = it
	}
	return out
}
