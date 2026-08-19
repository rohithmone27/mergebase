// Package validate checks whole-schema coherence. It exists because a
// conflict-free merge can still produce a broken schema: each branch valid
// alone, the combination invalid — an FK pointing at a table the other side
// dropped, an index on a column that no longer exists, two renames colliding
// into one name. Conflict detection finds where branches disagree; this pass
// finds where their agreement is still broken. A merge only commits if this
// pass returns no problems.
package validate

import (
	"fmt"

	"mergebase/internal/schema"
)

// Problem is one coherence violation, written for humans.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Table   string `json:"table,omitempty"`
	Object  string `json:"object,omitempty"`
}

// Check walks the whole schema graph and returns every violation found.
// An empty result means the schema is coherent.
func Check(s *schema.Schema) []Problem {
	problems := []Problem{}
	add := func(code, table, object, format string, args ...any) {
		problems = append(problems, Problem{
			Code: code, Table: table, Object: object, Message: fmt.Sprintf(format, args...),
		})
	}

	tableNames := map[string]bool{}
	indexNames := map[string]string{} // index name → table
	for _, t := range s.Tables {
		if tableNames[t.Name] {
			add("duplicate_table_name", t.Name, "", "two tables are named %q", t.Name)
		}
		tableNames[t.Name] = true

		if len(t.Columns) == 0 {
			add("table_no_columns", t.Name, "", "table %q has no columns", t.Name)
		}

		colNames := map[string]bool{}
		for _, c := range t.Columns {
			if colNames[c.Name] {
				add("duplicate_column_name", t.Name, c.Name, "table %q has two columns named %q", t.Name, c.Name)
			}
			colNames[c.Name] = true
		}

		pks := 0
		for _, c := range t.Constraints {
			label := constraintLabel(c)
			for _, id := range c.ColumnIDs {
				if t.ColumnByID(id) == nil {
					add("constraint_missing_column", t.Name, label,
						"%s on %q references a column that no longer exists", label, t.Name)
				}
			}
			switch c.Kind {
			case schema.PrimaryKey:
				pks++
				for _, id := range c.ColumnIDs {
					if col := t.ColumnByID(id); col != nil && col.Nullable {
						add("pk_nullable_column", t.Name, col.Name,
							"primary key column %q on %q is nullable", col.Name, t.Name)
					}
				}
				if len(c.ColumnIDs) == 0 {
					add("constraint_no_columns", t.Name, label, "primary key on %q has no columns", t.Name)
				}
			case schema.Unique:
				if len(c.ColumnIDs) == 0 {
					add("constraint_no_columns", t.Name, label, "unique constraint on %q has no columns", t.Name)
				}
			case schema.ForeignKey:
				target := s.TableByID(c.RefTableID)
				if target == nil {
					add("fk_missing_table", t.Name, label,
						"foreign key on %q points at a table that no longer exists", t.Name)
					continue
				}
				if len(c.ColumnIDs) != len(c.RefColumnIDs) || len(c.ColumnIDs) == 0 {
					add("fk_column_count_mismatch", t.Name, label,
						"foreign key on %q has mismatched column lists (%d local, %d referenced)",
						t.Name, len(c.ColumnIDs), len(c.RefColumnIDs))
				}
				for _, id := range c.RefColumnIDs {
					if target.ColumnByID(id) == nil {
						add("fk_missing_column", t.Name, label,
							"foreign key on %q references a column that no longer exists on %q", t.Name, target.Name)
					}
				}
			case schema.Check:
				if c.Expr == "" {
					add("check_no_expression", t.Name, label, "check constraint on %q has no expression", t.Name)
				}
			}
		}
		if pks > 1 {
			add("multiple_primary_keys", t.Name, "", "table %q has %d primary keys", t.Name, pks)
		}

		for _, ix := range t.Indexes {
			if other, dup := indexNames[ix.Name]; dup {
				add("duplicate_index_name", t.Name, ix.Name,
					"two indexes are named %q (on %q and %q)", ix.Name, other, t.Name)
			}
			indexNames[ix.Name] = t.Name
			if len(ix.Columns) == 0 {
				add("index_no_columns", t.Name, ix.Name, "index %q has no columns", ix.Name)
			}
			for _, ic := range ix.Columns {
				if t.ColumnByID(ic.ColumnID) == nil {
					add("index_missing_column", t.Name, ix.Name,
						"index %q references a column that no longer exists on %q", ix.Name, t.Name)
				}
			}
		}
	}
	return problems
}

func constraintLabel(c schema.Constraint) string {
	if c.Name != "" {
		return c.Name
	}
	return string(c.Kind)
}
