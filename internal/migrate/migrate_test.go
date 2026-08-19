package migrate

import (
	"strings"
	"testing"

	"mergebase/internal/ops"
	"mergebase/internal/parser"
	"mergebase/internal/schema"
)

func parse(t *testing.T, ddl string) *schema.Schema {
	t.Helper()
	res, err := parser.Parse(ddl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return res.Schema
}

func edit(t *testing.T, s *schema.Schema, operations ...ops.Op) *schema.Schema {
	t.Helper()
	out, err := ops.Apply(s, operations)
	if err != nil {
		t.Fatalf("ops.Apply: %v", err)
	}
	return out
}

func statementIndex(t *testing.T, script *Script, part string) int {
	t.Helper()
	for i, st := range script.Statements {
		if strings.Contains(st.SQL, part) {
			return i
		}
	}
	t.Fatalf("no statement containing %q in:\n%s", part, script.SQL())
	return -1
}

func TestIdenticalSchemasEmitNothing(t *testing.T) {
	s := parse(t, `CREATE TABLE t (id INT PRIMARY KEY);`)
	script := Generate(s, s.Clone())
	if len(script.Statements) != 0 {
		t.Fatalf("statements = %+v, want none", script.Statements)
	}
	if !strings.Contains(script.SQL(), "No changes") {
		t.Fatal("empty script must say so")
	}
}

// Invariant 5, exercised end to end for creation: generate from empty →
// target, parse the emitted SQL with the real parser, and the result must
// equal the target schema shape.
func TestCreateFromEmptyRoundTrips(t *testing.T) {
	target := parse(t, `
		CREATE TABLE users (
			id    BIGINT PRIMARY KEY,
			email VARCHAR(255) NOT NULL,
			bio   TEXT DEFAULT 'hi'
		);
		CREATE TABLE orders (
			id      BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL,
			CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX idx_users_email ON users (email);`)

	script := Generate(&schema.Schema{}, target)
	reparsed, err := parser.Parse(script.SQL())
	if err != nil {
		t.Fatalf("emitted SQL does not parse: %v\n%s", err, script.SQL())
	}

	for _, tbl := range target.Tables {
		got := reparsed.Schema.TableByName(tbl.Name)
		if got == nil {
			t.Fatalf("emitted SQL lost table %q", tbl.Name)
		}
		if len(got.Columns) != len(tbl.Columns) {
			t.Fatalf("table %q: %d columns, want %d", tbl.Name, len(got.Columns), len(tbl.Columns))
		}
		for _, c := range tbl.Columns {
			rc := got.ColumnByName(c.Name)
			if rc == nil || !rc.Type.Equal(c.Type) || rc.Nullable != c.Nullable || rc.Default != c.Default {
				t.Fatalf("column %s.%s did not round-trip: %+v vs %+v", tbl.Name, c.Name, rc, c)
			}
		}
		if len(got.Constraints) != len(tbl.Constraints) || len(got.Indexes) != len(tbl.Indexes) {
			t.Fatalf("table %q constraints/indexes did not round-trip", tbl.Name)
		}
	}
	// FK actions survive.
	refk := reparsed.Schema.TableByName("orders").Constraints
	foundCascade := false
	for _, c := range refk {
		if c.Kind == schema.ForeignKey && c.OnDelete == schema.Cascade {
			foundCascade = true
		}
	}
	if !foundCascade {
		t.Fatal("ON DELETE CASCADE lost in round-trip")
	}
}

// Circular foreign keys: the reason phased emission exists. Both tables
// reference each other; the script must still parse and land both FKs.
func TestCircularForeignKeys(t *testing.T) {
	target := parse(t, `
		CREATE TABLE a (id BIGINT PRIMARY KEY, b_id BIGINT);
		CREATE TABLE b (id BIGINT PRIMARY KEY, a_id BIGINT);
		ALTER TABLE a ADD CONSTRAINT a_b_fk FOREIGN KEY (b_id) REFERENCES b (id);
		ALTER TABLE b ADD CONSTRAINT b_a_fk FOREIGN KEY (a_id) REFERENCES a (id);`)

	script := Generate(&schema.Schema{}, target)
	if _, err := parser.Parse(script.SQL()); err != nil {
		t.Fatalf("circular-FK script does not parse: %v\n%s", err, script.SQL())
	}
	// Both CREATE TABLEs precede both FK additions.
	lastCreate := max(statementIndex(t, script, `CREATE TABLE "a"`), statementIndex(t, script, `CREATE TABLE "b"`))
	firstFK := min(statementIndex(t, script, `"a_b_fk"`), statementIndex(t, script, `"b_a_fk"`))
	if lastCreate > firstFK {
		t.Fatalf("FKs emitted before both tables exist:\n%s", script.SQL())
	}
	// No FK inline in CREATE TABLE.
	for _, st := range script.Statements {
		if st.Phase == "create tables" && strings.Contains(st.SQL, "FOREIGN KEY") {
			t.Fatalf("CREATE TABLE must not inline FKs:\n%s", st.SQL)
		}
	}
}

func TestDropOrderingConstraintsBeforeTables(t *testing.T) {
	from := parse(t, `
		CREATE TABLE users (id BIGINT PRIMARY KEY);
		CREATE TABLE orders (id BIGINT PRIMARY KEY,
			user_id BIGINT, CONSTRAINT o_fk FOREIGN KEY (user_id) REFERENCES users (id));`)
	// Drop users; orders keeps living but loses the FK and the column.
	orders := from.TableByName("orders")
	to := edit(t, from,
		ops.Op{Op: ops.DropConstraint, TableID: orders.ID, ConstraintID: orders.Constraints[1].ID},
		ops.Op{Op: ops.DropColumn, TableID: orders.ID, ColumnID: orders.ColumnByName("user_id").ID},
		ops.Op{Op: ops.DropTable, TableID: from.TableByName("users").ID})

	script := Generate(from, to)
	fkDrop := statementIndex(t, script, `DROP CONSTRAINT "o_fk"`)
	colDrop := statementIndex(t, script, `DROP COLUMN "user_id"`)
	tblDrop := statementIndex(t, script, `DROP TABLE "users"`)
	if !(fkDrop < colDrop && colDrop < tblDrop) {
		t.Fatalf("drop order wrong (fk=%d col=%d table=%d):\n%s", fkDrop, colDrop, tblDrop, script.SQL())
	}
}

func TestRenamesEmitRenameNeverDropAdd(t *testing.T) {
	from := parse(t, `CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(255));`)
	users := from.TableByName("users")
	to := edit(t, from,
		ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: users.ColumnByName("email").ID, Name: "email_address"},
		ops.Op{Op: ops.RenameTable, TableID: users.ID, Name: "accounts"})

	script := Generate(from, to)
	sql := script.SQL()
	if !strings.Contains(sql, `ALTER TABLE "users" RENAME TO "accounts";`) {
		t.Fatalf("missing table rename:\n%s", sql)
	}
	if !strings.Contains(sql, `ALTER TABLE "accounts" RENAME COLUMN "email" TO "email_address";`) {
		t.Fatalf("column rename must use the table's new name:\n%s", sql)
	}
	if strings.Contains(sql, "DROP COLUMN") || strings.Contains(sql, "ADD COLUMN") {
		t.Fatalf("a rename tore into drop+add — data loss:\n%s", sql)
	}
}

func TestRenameSwapUsesTempName(t *testing.T) {
	from := parse(t, `CREATE TABLE t (a INT, b INT);`)
	tbl := from.TableByName("t")
	aID, bID := tbl.ColumnByName("a").ID, tbl.ColumnByName("b").ID
	// Swap the two column names — impossible without a temp step.
	to := from.Clone()
	to.TableByName("t").ColumnByID(aID).Name = "b"
	to.TableByName("t").ColumnByID(bID).Name = "a"

	script := Generate(from, to)
	sql := script.SQL()
	if !strings.Contains(sql, "__mergebase_tmp_") {
		t.Fatalf("swap must go through a temp name:\n%s", sql)
	}
	// Exactly three renames: aside, over, back.
	renames := 0
	for _, st := range script.Statements {
		if strings.Contains(st.SQL, "RENAME COLUMN") {
			renames++
		}
	}
	if renames != 3 {
		t.Fatalf("swap needs exactly 3 renames, got %d:\n%s", renames, sql)
	}
}

func TestDataDependentWarnings(t *testing.T) {
	from := parse(t, `CREATE TABLE t (a VARCHAR(10), b INT);`)
	tbl := from.TableByName("t")
	to := edit(t, from,
		ops.Op{Op: ops.RetypeColumn, TableID: tbl.ID, ColumnID: tbl.ColumnByName("a").ID, Type: &schema.DataType{Base: "integer"}},
		ops.Op{Op: ops.SetNullable, TableID: tbl.ID, ColumnID: tbl.ColumnByName("b").ID, Nullable: bp(false)},
		ops.Op{Op: ops.AddColumn, TableID: tbl.ID, Column: &ops.ColumnSpec{Name: "c", Type: schema.DataType{Base: "text"}, Nullable: false}})

	script := Generate(from, to)
	got := map[string]bool{}
	for _, w := range script.Warnings {
		got[w.Code] = true
	}
	for _, want := range []string{"retype_needs_using", "set_not_null", "add_not_null_no_default"} {
		if !got[want] {
			t.Errorf("missing warning %s; got %+v", want, script.Warnings)
		}
	}
	// Warnings appear as comments in the rendered SQL.
	if !strings.Contains(script.SQL(), "-- WARNING:") {
		t.Fatal("warnings must render as comments in the script")
	}
}

func TestUnnamedConstraintGetsPostgresDefaultName(t *testing.T) {
	from := parse(t, `CREATE TABLE t (id BIGINT PRIMARY KEY);`)
	to := edit(t, from, ops.Op{Op: ops.DropConstraint,
		TableID: from.TableByName("t").ID, ConstraintID: from.TableByName("t").PrimaryKey().ID})
	script := Generate(from, to)
	if !strings.Contains(script.SQL(), `DROP CONSTRAINT "t_pkey"`) {
		t.Fatalf("unnamed PK must drop by PostgreSQL's default name:\n%s", script.SQL())
	}
}

func bp(b bool) *bool { return &b }
