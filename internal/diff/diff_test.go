package diff

import (
	"strings"
	"testing"

	"github.com/rohithmone27/mergebase/internal/ops"
	"github.com/rohithmone27/mergebase/internal/parser"
	"github.com/rohithmone27/mergebase/internal/schema"
)

func base(t *testing.T) *schema.Schema {
	t.Helper()
	res, err := parser.Parse(`
		CREATE TABLE users (
			id    BIGINT PRIMARY KEY,
			email VARCHAR(255) NOT NULL,
			name  TEXT
		);
		CREATE TABLE orders (
			id      BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users (id)
		);
		CREATE INDEX idx_users_email ON users (email);`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return res.Schema
}

func apply(t *testing.T, s *schema.Schema, operations ...ops.Op) *schema.Schema {
	t.Helper()
	out, err := ops.Apply(s, operations)
	if err != nil {
		t.Fatalf("ops.Apply: %v", err)
	}
	return out
}

func kinds(d *Diff) []ChangeKind {
	out := make([]ChangeKind, 0, len(d.Changes))
	for _, c := range d.Changes {
		out = append(out, c.Kind)
	}
	return out
}

func hasChange(d *Diff, kind ChangeKind, textPart string) bool {
	for _, c := range d.Changes {
		if c.Kind == kind && strings.Contains(c.Text, textPart) {
			return true
		}
	}
	return false
}

func TestIdenticalSchemasProduceNoChanges(t *testing.T) {
	s := base(t)
	d := Compute(s, s.Clone())
	if len(d.Changes) != 0 {
		t.Fatalf("changes = %+v, want none", d.Changes)
	}
	if d.Unchanged == 0 {
		t.Fatal("unchanged count must be positive for identical schemas")
	}
}

func TestEveryChangeKind(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	orders := s.TableByName("orders")

	to := apply(t, s,
		ops.Op{Op: ops.CreateTable, Name: "invoices", Columns: []ops.ColumnSpec{{Name: "id", Type: schema.DataType{Base: "bigint"}}}},
		ops.Op{Op: ops.DropTable, TableID: orders.ID},
		ops.Op{Op: ops.RenameTable, TableID: users.ID, Name: "accounts"},
		ops.Op{Op: ops.AddColumn, TableID: users.ID, Column: &ops.ColumnSpec{Name: "age", Type: schema.DataType{Base: "integer"}, Nullable: true}},
		ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: users.ColumnByName("name").ID, Name: "full_name"},
		ops.Op{Op: ops.RetypeColumn, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Type: &schema.DataType{Base: "text"}},
		ops.Op{Op: ops.SetNullable, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Nullable: ptr(true)},
		ops.Op{Op: ops.SetDefault, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Default: ptr2("'x'")},
		ops.Op{Op: ops.DropIndex, TableID: users.ID, IndexID: users.Indexes[0].ID},
		ops.Op{Op: ops.AddIndex, TableID: users.ID, Index: &ops.IndexSpec{Name: "idx_accounts_age", Columns: []schema.IndexColumn{{ColumnID: mustCol(t, s, "users", "name")}}}},
	)

	d := Compute(s, to)
	want := []struct {
		kind ChangeKind
		text string
	}{
		{TableAdded, "added table invoices"},
		{TableDropped, "dropped table orders"},
		{TableRenamed, "renamed table users → accounts"},
		{ColumnAdded, "added column accounts.age"},
		{ColumnRenamed, "renamed accounts.name → full_name"},
		{ColumnRetyped, "changed type of accounts.email: varchar(255) → text"},
		{NullChanged, "accounts.email is now nullable"},
		{DefChanged, "changed default of accounts.email: none → 'x'"},
		{IndexDropped, "dropped index idx_users_email"},
		{IndexAdded, "added index idx_accounts_age"},
	}
	for _, w := range want {
		if !hasChange(d, w.kind, w.text) {
			t.Errorf("missing %s %q in %v", w.kind, w.text, kinds(d))
		}
	}
	// Dropping the orders table also removes its constraints from the diff's
	// view of matched tables — the drop is reported once, at table level.
	if hasChange(d, ConsDropped, "orders") {
		t.Error("constraints of a dropped table must not double-report")
	}
}

func TestRenamePlusRetypeIsOneObjectTwoChanges(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	emailID := users.ColumnByName("email").ID

	to := apply(t, s,
		ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: emailID, Name: "email_address"},
		ops.Op{Op: ops.RetypeColumn, TableID: users.ID, ColumnID: emailID, Type: &schema.DataType{Base: "text"}},
	)
	d := Compute(s, to)

	var renames, retypes, adds, drops int
	for _, c := range d.Changes {
		switch c.Kind {
		case ColumnRenamed:
			renames++
			if c.ObjectID != emailID {
				t.Error("rename must carry the original ObjectID")
			}
		case ColumnRetyped:
			retypes++
			if c.ObjectID != emailID {
				t.Error("retype must carry the original ObjectID")
			}
		case ColumnAdded:
			adds++
		case ColumnDropped:
			drops++
		}
	}
	if renames != 1 || retypes != 1 || adds != 0 || drops != 0 {
		t.Fatalf("rename+retype = %d renames, %d retypes, %d adds, %d drops — identity was lost", renames, retypes, adds, drops)
	}
}

func TestConstraintChanges(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	orders := s.TableByName("orders")

	to := apply(t, s,
		ops.Op{Op: ops.AddConstraint, TableID: users.ID, Constraint: &ops.ConstraintSpec{
			Kind: schema.Unique, ColumnIDs: []schema.ObjectID{users.ColumnByName("email").ID}}},
		ops.Op{Op: ops.DropConstraint, TableID: orders.ID, ConstraintID: fk(t, s, "orders").ID},
	)
	d := Compute(s, to)
	if !hasChange(d, ConsAdded, "UNIQUE (email)") {
		t.Errorf("missing constraint_added, got %+v", d.Changes)
	}
	if !hasChange(d, ConsDropped, "FOREIGN KEY (user_id) → users(id)") {
		t.Errorf("missing constraint_dropped with resolved names, got %+v", d.Changes)
	}
}

func TestDeterministicOrder(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	to := apply(t, s,
		ops.Op{Op: ops.AddColumn, TableID: users.ID, Column: &ops.ColumnSpec{Name: "b", Type: schema.DataType{Base: "text"}, Nullable: true}},
		ops.Op{Op: ops.AddColumn, TableID: users.ID, Column: &ops.ColumnSpec{Name: "a", Type: schema.DataType{Base: "text"}, Nullable: true}},
	)
	first := Compute(s, to)
	for range 5 {
		if got := Compute(s, to); len(got.Changes) != len(first.Changes) {
			t.Fatal("diff length not deterministic")
		} else {
			for i := range got.Changes {
				if got.Changes[i].Text != first.Changes[i].Text {
					t.Fatalf("diff order not deterministic: %q vs %q", got.Changes[i].Text, first.Changes[i].Text)
				}
			}
		}
	}
	// Alphabetical within a kind.
	if !(first.Changes[0].Object == "a" && first.Changes[1].Object == "b") {
		t.Fatalf("expected alphabetical object order, got %v then %v", first.Changes[0].Object, first.Changes[1].Object)
	}
}

func ptr(b bool) *bool      { return &b }
func ptr2(s string) *string { return &s }

func mustCol(t *testing.T, s *schema.Schema, table, col string) schema.ObjectID {
	t.Helper()
	c := s.TableByName(table).ColumnByName(col)
	if c == nil {
		t.Fatalf("column %s.%s missing", table, col)
	}
	return c.ID
}

func fk(t *testing.T, s *schema.Schema, table string) *schema.Constraint {
	t.Helper()
	for i, c := range s.TableByName(table).Constraints {
		if c.Kind == schema.ForeignKey {
			return &s.TableByName(table).Constraints[i]
		}
	}
	t.Fatalf("no FK on %s", table)
	return nil
}
