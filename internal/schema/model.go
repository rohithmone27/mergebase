// Package schema defines the versioned artifact: a structured model of a
// database schema. Diff, merge, validation, and migration all operate on this
// model — SQL text is only ever an input or output format.
//
// Identity rules, which the whole system depends on:
//
//   - Every table and column carries a stable ObjectID that survives renames.
//     A rename is "same ID, new name", never "drop + add".
//   - Constraints and indexes reference tables and columns by ID, never by
//     name. Names are resolved from IDs only at SQL-emit and display time.
//   - Collections are ordered slices, not name-keyed maps: names change and
//     iteration order must be deterministic.
package schema

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ObjectID is a stable identity for a table, column, constraint, or index.
type ObjectID string

// NewObjectID returns a random 128-bit hex identifier.
func NewObjectID() ObjectID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("schema: reading random bytes: %v", err))
	}
	return ObjectID(hex.EncodeToString(b[:]))
}

// Schema is one complete snapshot of a database schema.
type Schema struct {
	Tables []Table `json:"tables"`
}

type Table struct {
	ID          ObjectID     `json:"id"`
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Constraints []Constraint `json:"constraints,omitempty"`
	Indexes     []Index      `json:"indexes,omitempty"`
}

type Column struct {
	ID       ObjectID `json:"id"`
	Name     string   `json:"name"`
	Type     DataType `json:"type"`
	Nullable bool     `json:"nullable"`
	// Default is the raw SQL default expression ("" means no default).
	// Compared textually; never evaluated.
	Default  string `json:"default,omitempty"`
	Position int    `json:"position"`
}

// DataType is a normalized type name plus its parameters,
// e.g. varchar(255) → {Base: "varchar", Params: [255]},
// numeric(10,2) → {Base: "numeric", Params: [10, 2]}.
type DataType struct {
	Base   string `json:"base"`
	Params []int  `json:"params,omitempty"`
}

func (t DataType) Equal(o DataType) bool {
	if t.Base != o.Base || len(t.Params) != len(o.Params) {
		return false
	}
	for i := range t.Params {
		if t.Params[i] != o.Params[i] {
			return false
		}
	}
	return true
}

// String renders the type as it appears in SQL, e.g. "varchar(255)".
func (t DataType) String() string {
	if len(t.Params) == 0 {
		return t.Base
	}
	var b strings.Builder
	b.WriteString(t.Base)
	b.WriteByte('(')
	for i, p := range t.Params {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprint(&b, p)
	}
	b.WriteByte(')')
	return b.String()
}

type ConstraintKind string

const (
	PrimaryKey ConstraintKind = "primary_key"
	ForeignKey ConstraintKind = "foreign_key"
	Unique     ConstraintKind = "unique"
	Check      ConstraintKind = "check"
)

type ReferentialAction string

const (
	NoAction   ReferentialAction = "no_action"
	Cascade    ReferentialAction = "cascade"
	SetNull    ReferentialAction = "set_null"
	SetDefault ReferentialAction = "set_default"
	Restrict   ReferentialAction = "restrict"
)

type Constraint struct {
	ID   ObjectID       `json:"id"`
	Name string         `json:"name,omitempty"`
	Kind ConstraintKind `json:"kind"`
	// ColumnIDs are the constrained columns of the owning table.
	ColumnIDs []ObjectID `json:"column_ids,omitempty"`

	// Foreign keys only. Targets are IDs so that renaming the referenced
	// table or columns never invalidates the reference.
	RefTableID   ObjectID          `json:"ref_table_id,omitempty"`
	RefColumnIDs []ObjectID        `json:"ref_column_ids,omitempty"`
	OnDelete     ReferentialAction `json:"on_delete,omitempty"`
	OnUpdate     ReferentialAction `json:"on_update,omitempty"`

	// Check constraints only: the raw expression, compared textually.
	Expr string `json:"expr,omitempty"`
}

type IndexColumn struct {
	ColumnID ObjectID `json:"column_id"`
	Desc     bool     `json:"desc,omitempty"`
}

type Index struct {
	ID      ObjectID      `json:"id"`
	Name    string        `json:"name"`
	Columns []IndexColumn `json:"columns"`
	Unique  bool          `json:"unique,omitempty"`
	Method  string        `json:"method,omitempty"` // "" means btree
}

// TableByID returns the table with the given ID, or nil.
func (s *Schema) TableByID(id ObjectID) *Table {
	for i := range s.Tables {
		if s.Tables[i].ID == id {
			return &s.Tables[i]
		}
	}
	return nil
}

// TableByName returns the table with the given name, or nil.
func (s *Schema) TableByName(name string) *Table {
	for i := range s.Tables {
		if s.Tables[i].Name == name {
			return &s.Tables[i]
		}
	}
	return nil
}

// ColumnByID returns the column with the given ID, or nil.
func (t *Table) ColumnByID(id ObjectID) *Column {
	for i := range t.Columns {
		if t.Columns[i].ID == id {
			return &t.Columns[i]
		}
	}
	return nil
}

// ColumnByName returns the column with the given name, or nil.
func (t *Table) ColumnByName(name string) *Column {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}

// ConstraintByID returns the constraint with the given ID, or nil.
func (t *Table) ConstraintByID(id ObjectID) *Constraint {
	for i := range t.Constraints {
		if t.Constraints[i].ID == id {
			return &t.Constraints[i]
		}
	}
	return nil
}

// IndexByID returns the index with the given ID, or nil.
func (t *Table) IndexByID(id ObjectID) *Index {
	for i := range t.Indexes {
		if t.Indexes[i].ID == id {
			return &t.Indexes[i]
		}
	}
	return nil
}

// PrimaryKey returns the table's primary key constraint, or nil.
func (t *Table) PrimaryKey() *Constraint {
	for i := range t.Constraints {
		if t.Constraints[i].Kind == PrimaryKey {
			return &t.Constraints[i]
		}
	}
	return nil
}

// Clone returns a deep copy sharing no memory with the receiver.
func (s *Schema) Clone() *Schema {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("schema: marshaling for clone: %v", err))
	}
	var out Schema
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(fmt.Sprintf("schema: unmarshaling clone: %v", err))
	}
	return &out
}
