package ops

import (
	"errors"
	"strings"
	"testing"

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

func mustApply(t *testing.T, s *schema.Schema, operations ...Op) *schema.Schema {
	t.Helper()
	out, err := Apply(s, operations)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return out
}

func TestApplyNeverMutatesInput(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	mustApply(t, s,
		Op{Op: RenameColumn, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Name: "email_address"},
		Op{Op: DropTable, TableID: s.TableByName("orders").ID},
	)
	if s.TableByName("users").ColumnByName("email") == nil || s.TableByName("orders") == nil {
		t.Fatal("Apply mutated its input snapshot")
	}
}

func TestRenamePreservesIdentity(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	emailID := users.ColumnByName("email").ID

	out := mustApply(t, s,
		Op{Op: RenameColumn, TableID: users.ID, ColumnID: emailID, Name: "email_address"},
		Op{Op: RenameTable, TableID: users.ID, Name: "accounts"},
	)
	accounts := out.TableByName("accounts")
	if accounts == nil || accounts.ID != users.ID {
		t.Fatal("table rename must preserve the table's ObjectID")
	}
	renamed := accounts.ColumnByName("email_address")
	if renamed == nil || renamed.ID != emailID {
		t.Fatal("column rename must preserve the column's ObjectID")
	}
	// The rename-then-reuse sequence: a NEW column may take the old name,
	// and it must be a different object.
	out2 := mustApply(t, out, Op{Op: AddColumn, TableID: accounts.ID,
		Column: &ColumnSpec{Name: "email", Type: schema.DataType{Base: "text"}, Nullable: true}})
	if out2.TableByName("accounts").ColumnByName("email").ID == emailID {
		t.Fatal("a new column reusing an old name must get a fresh ObjectID")
	}
}

func TestEveryBriefOperation(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	orders := s.TableByName("orders")

	out := mustApply(t, s,
		// create and drop tables
		Op{Op: CreateTable, Name: "sessions", Columns: []ColumnSpec{
			{Name: "id", Type: schema.DataType{Base: "bigint"}},
			{Name: "token", Type: schema.DataType{Base: "varchar", Params: []int{64}}},
		}},
		// add, rename, retype columns; nullability; defaults
		Op{Op: AddColumn, TableID: users.ID, Column: &ColumnSpec{Name: "age", Type: schema.DataType{Base: "integer"}, Nullable: true}},
		Op{Op: RenameColumn, TableID: users.ID, ColumnID: users.ColumnByName("name").ID, Name: "full_name"},
		Op{Op: RetypeColumn, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Type: &schema.DataType{Base: "text"}},
		Op{Op: SetNullable, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Nullable: boolPtr(true)},
		Op{Op: SetDefault, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Default: strPtr("'none'")},
	)

	sessions := out.TableByName("sessions")
	if sessions == nil || len(sessions.Columns) != 2 {
		t.Fatal("create_table failed")
	}
	u := out.TableByName("users")
	if u.ColumnByName("age") == nil || u.ColumnByName("full_name") == nil {
		t.Fatal("add/rename column failed")
	}
	email := u.ColumnByName("email")
	if email.Type.Base != "text" || !email.Nullable || email.Default != "'none'" {
		t.Fatalf("retype/nullable/default failed: %+v", email)
	}

	// constraints and indexes
	sessionsToken := sessions.ColumnByName("token")
	out2 := mustApply(t, out,
		Op{Op: AddConstraint, TableID: sessions.ID, Constraint: &ConstraintSpec{Kind: schema.Unique, ColumnIDs: []schema.ObjectID{sessionsToken.ID}}},
		Op{Op: AddIndex, TableID: sessions.ID, Index: &IndexSpec{Name: "idx_sessions_token", Columns: []schema.IndexColumn{{ColumnID: sessionsToken.ID}}, Unique: true}},
		Op{Op: DropIndex, TableID: users.ID, IndexID: u.Indexes[0].ID},
		Op{Op: DropConstraint, TableID: orders.ID, ConstraintID: findFK(t, out, "orders").ID},
		Op{Op: DropColumn, TableID: orders.ID, ColumnID: out.TableByName("orders").ColumnByName("user_id").ID},
		Op{Op: DropTable, TableID: sessions.ID},
	)
	if len(out2.TableByName("users").Indexes) != 0 {
		t.Fatal("drop_index failed")
	}
	if out2.TableByName("sessions") != nil {
		t.Fatal("drop_table failed")
	}
	if out2.TableByName("orders").ColumnByName("user_id") != nil {
		t.Fatal("drop_column failed")
	}
}

func TestStructuralRules(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	orders := s.TableByName("orders")

	cases := []struct {
		name    string
		op      Op
		wantErr string
	}{
		{"duplicate table", Op{Op: CreateTable, Name: "users", Columns: []ColumnSpec{{Name: "x", Type: schema.DataType{Base: "int"}}}}, "already exists"},
		{"duplicate column", Op{Op: AddColumn, TableID: users.ID, Column: &ColumnSpec{Name: "email", Type: schema.DataType{Base: "text"}}}, "already exists"},
		{"rename collision", Op{Op: RenameColumn, TableID: users.ID, ColumnID: users.ColumnByName("name").ID, Name: "email"}, "already exists"},
		{"pk nullable", Op{Op: SetNullable, TableID: users.ID, ColumnID: users.ColumnByName("id").ID, Nullable: boolPtr(true)}, "primary key"},
		{"second pk", Op{Op: AddConstraint, TableID: users.ID, Constraint: &ConstraintSpec{Kind: schema.PrimaryKey, ColumnIDs: []schema.ObjectID{users.ColumnByName("email").ID}}}, "already has a primary key"},
		{"drop referenced column", Op{Op: DropColumn, TableID: users.ID, ColumnID: users.ColumnByName("email").ID}, "still referenced"},
		{"unknown table", Op{Op: DropTable, TableID: "nope"}, "not found"},
		{"unknown column", Op{Op: RenameColumn, TableID: users.ID, ColumnID: "nope", Name: "x"}, "not found"},
		{"fk to unknown table", Op{Op: AddConstraint, TableID: orders.ID, Constraint: &ConstraintSpec{Kind: schema.ForeignKey, ColumnIDs: []schema.ObjectID{orders.ColumnByName("id").ID}, RefTableID: "nope", RefColumnIDs: []schema.ObjectID{"x"}}}, "does not exist"},
		{"duplicate index name", Op{Op: AddIndex, TableID: orders.ID, Index: &IndexSpec{Name: "idx_users_email", Columns: []schema.IndexColumn{{ColumnID: orders.ColumnByName("id").ID}}}}, "already exists"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Apply(s, []Op{c.op})
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestErrorCarriesOperationIndex(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	_, err := Apply(s, []Op{
		{Op: RenameTable, TableID: users.ID, Name: "accounts"}, // fine
		{Op: DropTable, TableID: "missing"},                    // fails
	})
	var opErr *Error
	if err == nil || !strings.Contains(err.Error(), "operation 2") {
		t.Fatalf("err = %v, want operation 2", err)
	}
	if !errors.As(err, &opErr) || opErr.Index != 1 || opErr.Op != DropTable {
		t.Fatalf("error detail wrong: %+v", err)
	}
}

func TestDescribe(t *testing.T) {
	s := base(t)
	users := s.TableByName("users")
	got := Describe(s, Op{Op: RenameColumn, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Name: "email_address"})
	if got != "rename users.email → email_address" {
		t.Fatalf("Describe = %q", got)
	}
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func findFK(t *testing.T, s *schema.Schema, table string) *schema.Constraint {
	t.Helper()
	for i, c := range s.TableByName(table).Constraints {
		if c.Kind == schema.ForeignKey {
			return &s.TableByName(table).Constraints[i]
		}
	}
	t.Fatalf("no FK on %s", table)
	return nil
}
