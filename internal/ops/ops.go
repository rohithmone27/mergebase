// Package ops defines the edit operations the app captures and applies to
// schema snapshots. Operation capture is one half of the identity story:
// edits made here preserve ObjectIDs exactly (a rename is "same ID, new
// name"), so diff and merge never have to guess what happened.
//
// Apply is pure: it clones the input snapshot, applies each operation in
// order, and returns the result. Structural rules (no duplicate names, no
// dangling references) are enforced per operation with actionable errors.
package ops

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/rohithmone27/mergebase/internal/schema"
)

// Kind discriminates the operation union.
type Kind string

const (
	CreateTable    Kind = "create_table"
	DropTable      Kind = "drop_table"
	RenameTable    Kind = "rename_table"
	AddColumn      Kind = "add_column"
	DropColumn     Kind = "drop_column"
	RenameColumn   Kind = "rename_column"
	RetypeColumn   Kind = "retype_column"
	SetNullable    Kind = "set_nullable"
	SetDefault     Kind = "set_default"
	AddConstraint  Kind = "add_constraint"
	DropConstraint Kind = "drop_constraint"
	AddIndex       Kind = "add_index"
	DropIndex      Kind = "drop_index"
)

// ColumnSpec describes a new column; the ID is assigned on apply.
type ColumnSpec struct {
	Name     string          `json:"name"`
	Type     schema.DataType `json:"type"`
	Nullable bool            `json:"nullable"`
	Default  string          `json:"default,omitempty"`
}

// ConstraintSpec describes a new constraint; the ID is assigned on apply.
type ConstraintSpec struct {
	Name         string                   `json:"name,omitempty"`
	Kind         schema.ConstraintKind    `json:"kind"`
	ColumnIDs    []schema.ObjectID        `json:"column_ids,omitempty"`
	RefTableID   schema.ObjectID          `json:"ref_table_id,omitempty"`
	RefColumnIDs []schema.ObjectID        `json:"ref_column_ids,omitempty"`
	OnDelete     schema.ReferentialAction `json:"on_delete,omitempty"`
	OnUpdate     schema.ReferentialAction `json:"on_update,omitempty"`
	Expr         string                   `json:"expr,omitempty"`
}

// IndexSpec describes a new index; the ID is assigned on apply.
type IndexSpec struct {
	Name    string               `json:"name"`
	Columns []schema.IndexColumn `json:"columns"`
	Unique  bool                 `json:"unique,omitempty"`
	Method  string               `json:"method,omitempty"`
}

// Op is one edit. Exactly the fields for its Kind are set.
type Op struct {
	Op Kind `json:"op"`

	TableID      schema.ObjectID `json:"table_id,omitempty"`
	ColumnID     schema.ObjectID `json:"column_id,omitempty"`
	ConstraintID schema.ObjectID `json:"constraint_id,omitempty"`
	IndexID      schema.ObjectID `json:"index_id,omitempty"`

	Name     string           `json:"name,omitempty"`     // create_table, rename_*
	Column   *ColumnSpec      `json:"column,omitempty"`   // add_column
	Columns  []ColumnSpec     `json:"columns,omitempty"`  // create_table
	Type     *schema.DataType `json:"type,omitempty"`     // retype_column
	Nullable *bool            `json:"nullable,omitempty"` // set_nullable
	Default  *string          `json:"default,omitempty"`  // set_default ("" clears)

	Constraint *ConstraintSpec `json:"constraint,omitempty"` // add_constraint
	Index      *IndexSpec      `json:"index,omitempty"`      // add_index
}

// Error is an apply failure with the index of the offending operation.
type Error struct {
	Index int
	Op    Kind
	Err   error
}

func (e *Error) Error() string { return fmt.Sprintf("operation %d (%s): %v", e.Index+1, e.Op, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Apply returns a new snapshot with ops applied in order. The input is
// never mutated.
func Apply(base *schema.Schema, operations []Op) (*schema.Schema, error) {
	s := base.Clone()
	for i, op := range operations {
		if err := applyOne(s, op); err != nil {
			return nil, &Error{Index: i, Op: op.Op, Err: err}
		}
	}
	return s, nil
}

func applyOne(s *schema.Schema, op Op) error {
	switch op.Op {
	case CreateTable:
		return applyCreateTable(s, op)
	case DropTable:
		t, err := needTable(s, op.TableID)
		if err != nil {
			return err
		}
		for i := range s.Tables {
			if s.Tables[i].ID == t.ID {
				s.Tables = append(s.Tables[:i], s.Tables[i+1:]...)
				break
			}
		}
		return nil
	case RenameTable:
		t, err := needTable(s, op.TableID)
		if err != nil {
			return err
		}
		if op.Name == "" {
			return fmt.Errorf("new table name must not be empty")
		}
		if existing := s.TableByName(op.Name); existing != nil && existing.ID != t.ID {
			return fmt.Errorf("a table named %q already exists", op.Name)
		}
		t.Name = op.Name
		return nil
	case AddColumn:
		t, err := needTable(s, op.TableID)
		if err != nil {
			return err
		}
		if op.Column == nil {
			return fmt.Errorf("add_column needs a column spec")
		}
		return addColumn(t, *op.Column)
	case DropColumn:
		t, c, err := needColumn(s, op.TableID, op.ColumnID)
		if err != nil {
			return err
		}
		if refs := columnReferences(t, c.ID); refs != "" {
			return fmt.Errorf("column %q is still referenced by %s — drop those first", c.Name, refs)
		}
		for i := range t.Columns {
			if t.Columns[i].ID == c.ID {
				t.Columns = append(t.Columns[:i], t.Columns[i+1:]...)
				break
			}
		}
		renumber(t)
		return nil
	case RenameColumn:
		t, c, err := needColumn(s, op.TableID, op.ColumnID)
		if err != nil {
			return err
		}
		if op.Name == "" {
			return fmt.Errorf("new column name must not be empty")
		}
		if existing := t.ColumnByName(op.Name); existing != nil && existing.ID != c.ID {
			return fmt.Errorf("a column named %q already exists on %q", op.Name, t.Name)
		}
		c.Name = op.Name
		return nil
	case RetypeColumn:
		_, c, err := needColumn(s, op.TableID, op.ColumnID)
		if err != nil {
			return err
		}
		if op.Type == nil || op.Type.Base == "" {
			return fmt.Errorf("retype_column needs a type")
		}
		c.Type = *op.Type
		return nil
	case SetNullable:
		t, c, err := needColumn(s, op.TableID, op.ColumnID)
		if err != nil {
			return err
		}
		if op.Nullable == nil {
			return fmt.Errorf("set_nullable needs a nullable value")
		}
		if *op.Nullable && isPKMember(t, c.ID) {
			return fmt.Errorf("column %q is part of the primary key and cannot be nullable", c.Name)
		}
		c.Nullable = *op.Nullable
		return nil
	case SetDefault:
		_, c, err := needColumn(s, op.TableID, op.ColumnID)
		if err != nil {
			return err
		}
		if op.Default == nil {
			return fmt.Errorf("set_default needs a default value (\"\" clears it)")
		}
		c.Default = *op.Default
		return nil
	case AddConstraint:
		return applyAddConstraint(s, op)
	case DropConstraint:
		t, err := needTable(s, op.TableID)
		if err != nil {
			return err
		}
		for i := range t.Constraints {
			if t.Constraints[i].ID == op.ConstraintID {
				t.Constraints = append(t.Constraints[:i], t.Constraints[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("constraint not found on table %q", t.Name)
	case AddIndex:
		return applyAddIndex(s, op)
	case DropIndex:
		t, err := needTable(s, op.TableID)
		if err != nil {
			return err
		}
		for i := range t.Indexes {
			if t.Indexes[i].ID == op.IndexID {
				t.Indexes = append(t.Indexes[:i], t.Indexes[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("index not found on table %q", t.Name)
	default:
		return fmt.Errorf("unknown operation %q", op.Op)
	}
}

func applyCreateTable(s *schema.Schema, op Op) error {
	if op.Name == "" {
		return fmt.Errorf("create_table needs a name")
	}
	if s.TableByName(op.Name) != nil {
		return fmt.Errorf("a table named %q already exists", op.Name)
	}
	if len(op.Columns) == 0 {
		return fmt.Errorf("create_table needs at least one column")
	}
	t := schema.Table{ID: schema.NewObjectID(), Name: op.Name}
	for _, spec := range op.Columns {
		if err := addColumn(&t, spec); err != nil {
			return err
		}
	}
	s.Tables = append(s.Tables, t)
	return nil
}

func applyAddConstraint(s *schema.Schema, op Op) error {
	t, err := needTable(s, op.TableID)
	if err != nil {
		return err
	}
	spec := op.Constraint
	if spec == nil {
		return fmt.Errorf("add_constraint needs a constraint spec")
	}
	for _, id := range spec.ColumnIDs {
		if t.ColumnByID(id) == nil {
			return fmt.Errorf("constraint references a column that does not exist on %q", t.Name)
		}
	}
	switch spec.Kind {
	case schema.PrimaryKey:
		if t.PrimaryKey() != nil {
			return fmt.Errorf("table %q already has a primary key", t.Name)
		}
		if len(spec.ColumnIDs) == 0 {
			return fmt.Errorf("a primary key needs at least one column")
		}
		for _, id := range spec.ColumnIDs {
			t.ColumnByID(id).Nullable = false
		}
	case schema.Unique:
		if len(spec.ColumnIDs) == 0 {
			return fmt.Errorf("a unique constraint needs at least one column")
		}
	case schema.ForeignKey:
		target := s.TableByID(spec.RefTableID)
		if target == nil {
			return fmt.Errorf("foreign key references a table that does not exist")
		}
		if len(spec.ColumnIDs) == 0 || len(spec.ColumnIDs) != len(spec.RefColumnIDs) {
			return fmt.Errorf("foreign key needs matching local and referenced column lists")
		}
		for _, id := range spec.RefColumnIDs {
			if target.ColumnByID(id) == nil {
				return fmt.Errorf("foreign key references a column that does not exist on %q", target.Name)
			}
		}
	case schema.Check:
		if spec.Expr == "" {
			return fmt.Errorf("a check constraint needs an expression")
		}
	default:
		return fmt.Errorf("unknown constraint kind %q", spec.Kind)
	}
	t.Constraints = append(t.Constraints, schema.Constraint{
		ID: schema.NewObjectID(), Name: spec.Name, Kind: spec.Kind,
		ColumnIDs: spec.ColumnIDs, RefTableID: spec.RefTableID, RefColumnIDs: spec.RefColumnIDs,
		OnDelete: spec.OnDelete, OnUpdate: spec.OnUpdate, Expr: spec.Expr,
	})
	return nil
}

func applyAddIndex(s *schema.Schema, op Op) error {
	t, err := needTable(s, op.TableID)
	if err != nil {
		return err
	}
	spec := op.Index
	if spec == nil {
		return fmt.Errorf("add_index needs an index spec")
	}
	if spec.Name == "" {
		return fmt.Errorf("an index needs a name")
	}
	if len(spec.Columns) == 0 {
		return fmt.Errorf("an index needs at least one column")
	}
	for _, table := range s.Tables {
		for _, ix := range table.Indexes {
			if ix.Name == spec.Name {
				return fmt.Errorf("an index named %q already exists (on table %q)", spec.Name, table.Name)
			}
		}
	}
	for _, ic := range spec.Columns {
		if t.ColumnByID(ic.ColumnID) == nil {
			return fmt.Errorf("index references a column that does not exist on %q", t.Name)
		}
	}
	t.Indexes = append(t.Indexes, schema.Index{
		ID: schema.NewObjectID(), Name: spec.Name, Columns: spec.Columns,
		Unique: spec.Unique, Method: spec.Method,
	})
	return nil
}

func addColumn(t *schema.Table, spec ColumnSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("a column needs a name")
	}
	if spec.Type.Base == "" {
		return fmt.Errorf("column %q needs a type", spec.Name)
	}
	if t.ColumnByName(spec.Name) != nil {
		return fmt.Errorf("a column named %q already exists on %q", spec.Name, t.Name)
	}
	t.Columns = append(t.Columns, schema.Column{
		ID: schema.NewObjectID(), Name: spec.Name, Type: spec.Type,
		Nullable: spec.Nullable, Default: spec.Default, Position: len(t.Columns) + 1,
	})
	return nil
}

func needTable(s *schema.Schema, id schema.ObjectID) (*schema.Table, error) {
	if id == "" {
		return nil, fmt.Errorf("missing table_id")
	}
	t := s.TableByID(id)
	if t == nil {
		return nil, fmt.Errorf("table not found — it may have been dropped in an earlier operation")
	}
	return t, nil
}

func needColumn(s *schema.Schema, tableID, columnID schema.ObjectID) (*schema.Table, *schema.Column, error) {
	t, err := needTable(s, tableID)
	if err != nil {
		return nil, nil, err
	}
	if columnID == "" {
		return nil, nil, fmt.Errorf("missing column_id")
	}
	c := t.ColumnByID(columnID)
	if c == nil {
		return nil, nil, fmt.Errorf("column not found on table %q", t.Name)
	}
	return t, c, nil
}

// columnReferences names what still points at a column, for actionable errors.
func columnReferences(t *schema.Table, id schema.ObjectID) string {
	for _, c := range t.Constraints {
		if slices.Contains(c.ColumnIDs, id) || slices.Contains(c.RefColumnIDs, id) {
			return fmt.Sprintf("constraint %s", nameOr(c.Name, string(c.Kind)))
		}
	}
	for _, ix := range t.Indexes {
		for _, ic := range ix.Columns {
			if ic.ColumnID == id {
				return fmt.Sprintf("index %q", ix.Name)
			}
		}
	}
	return ""
}

func isPKMember(t *schema.Table, id schema.ObjectID) bool {
	pk := t.PrimaryKey()
	return pk != nil && slices.Contains(pk.ColumnIDs, id)
}

func renumber(t *schema.Table) {
	for i := range t.Columns {
		t.Columns[i].Position = i + 1
	}
}

func nameOr(name, fallback string) string {
	if name != "" {
		return fmt.Sprintf("%q", name)
	}
	return fallback
}

// Describe renders an operation as a human-readable commit-message fragment.
func Describe(s *schema.Schema, op Op) string {
	tbl := func() string {
		if t := s.TableByID(op.TableID); t != nil {
			return t.Name
		}
		return "?"
	}
	col := func() string {
		if t := s.TableByID(op.TableID); t != nil {
			if c := t.ColumnByID(op.ColumnID); c != nil {
				return t.Name + "." + c.Name
			}
			return t.Name + ".?"
		}
		return "?"
	}
	switch op.Op {
	case CreateTable:
		return "create table " + op.Name
	case DropTable:
		return "drop table " + tbl()
	case RenameTable:
		return "rename table " + tbl() + " → " + op.Name
	case AddColumn:
		if op.Column != nil {
			return "add column " + tbl() + "." + op.Column.Name
		}
		return "add column on " + tbl()
	case DropColumn:
		return "drop column " + col()
	case RenameColumn:
		return "rename " + col() + " → " + op.Name
	case RetypeColumn:
		if op.Type != nil {
			return "retype " + col() + " → " + op.Type.String()
		}
		return "retype " + col()
	case SetNullable:
		if op.Nullable != nil && !*op.Nullable {
			return "set " + col() + " NOT NULL"
		}
		return "set " + col() + " nullable"
	case SetDefault:
		if op.Default != nil && *op.Default == "" {
			return "clear default on " + col()
		}
		return "set default on " + col()
	case AddConstraint:
		if op.Constraint != nil {
			return "add " + string(op.Constraint.Kind) + " on " + tbl()
		}
		return "add constraint on " + tbl()
	case DropConstraint:
		return "drop constraint on " + tbl()
	case AddIndex:
		if op.Index != nil {
			return "add index " + op.Index.Name
		}
		return "add index on " + tbl()
	case DropIndex:
		return "drop index on " + tbl()
	default:
		return string(op.Op)
	}
}

// UnmarshalOps parses a JSON operation list.
func UnmarshalOps(raw json.RawMessage) ([]Op, error) {
	var out []Op
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("invalid operations payload: %w", err)
	}
	return out, nil
}
