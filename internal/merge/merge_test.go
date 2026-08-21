// The conflict-taxonomy suite: for every class, one case that must conflict
// and one adjacent case that must merge cleanly. Divergence is produced with
// real edit operations (ops.Apply on clones), exactly as the app does it.
package merge

import (
	"strings"
	"testing"

	"github.com/rohithmone27/mergebase/internal/ops"
	"github.com/rohithmone27/mergebase/internal/parser"
	"github.com/rohithmone27/mergebase/internal/schema"
)

func baseSchema(t *testing.T) *schema.Schema {
	t.Helper()
	res, err := parser.Parse(`
		CREATE TABLE users (
			id    BIGINT PRIMARY KEY,
			email VARCHAR(255) NOT NULL,
			name  TEXT
		);
		CREATE TABLE orders (
			id      BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users (id),
			status  VARCHAR(16) NOT NULL DEFAULT 'pending'
		);
		CREATE INDEX idx_orders_status ON orders (status);`)
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

func run(t *testing.T, base, ours, theirs *schema.Schema, resolutions ...Resolution) *Result {
	t.Helper()
	res, err := Merge(Input{Base: base, Ours: ours, Theirs: theirs,
		OursName: "main", TheirsName: "feature", Resolutions: resolutions})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return res
}

func mustClean(t *testing.T, res *Result) *schema.Schema {
	t.Helper()
	if len(res.Conflicts) != 0 {
		t.Fatalf("expected clean merge, got conflicts: %+v", res.Conflicts)
	}
	if len(res.Problems) != 0 {
		t.Fatalf("expected valid merge, got problems: %+v", res.Problems)
	}
	if res.Schema == nil {
		t.Fatal("clean merge must produce a schema")
	}
	return res.Schema
}

func oneConflict(t *testing.T, res *Result, class string) Conflict {
	t.Helper()
	if len(res.Conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %d: %+v", len(res.Conflicts), res.Conflicts)
	}
	c := res.Conflicts[0]
	if c.Class != class {
		t.Fatalf("conflict class = %s, want %s (%+v)", c.Class, class, c)
	}
	if res.Schema != nil {
		t.Fatal("a merge with conflicts must not produce a schema")
	}
	return c
}

// --- the five-case algebra itself ---

func TestAlgebraOnlyOneSideChanged(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	ours := edit(t, base, ops.Op{Op: ops.RetypeColumn, TableID: users.ID,
		ColumnID: users.ColumnByName("email").ID, Type: &schema.DataType{Base: "text"}})

	merged := mustClean(t, run(t, base, ours, base.Clone()))
	if merged.TableByName("users").ColumnByName("email").Type.Base != "text" {
		t.Fatal("ours-only change must be taken")
	}
	merged = mustClean(t, run(t, base, base.Clone(), ours))
	if merged.TableByName("users").ColumnByName("email").Type.Base != "text" {
		t.Fatal("theirs-only change must be taken")
	}
}

func TestAlgebraSameChangeBothSides(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	retype := ops.Op{Op: ops.RetypeColumn, TableID: users.ID,
		ColumnID: users.ColumnByName("email").ID, Type: &schema.DataType{Base: "text"}}

	merged := mustClean(t, run(t, base, edit(t, base, retype), edit(t, base, retype)))
	if merged.TableByName("users").ColumnByName("email").Type.Base != "text" {
		t.Fatal("identical change on both sides must merge to that change, without conflict")
	}
}

// --- C1: rename / rename ---

func TestC1RenameRenameConflicts(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	emailID := users.ColumnByName("email").ID
	ours := edit(t, base, ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: emailID, Name: "email_address"})
	theirs := edit(t, base, ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: emailID, Name: "contact_email"})

	res := run(t, base, ours, theirs)
	c := oneConflict(t, res, "rename_rename")
	if c.OursValue != "email_address" || c.TheirsValue != "contact_email" || c.Base != "email" {
		t.Fatalf("conflict values wrong: %+v", c)
	}
	if !c.AllowCustom {
		t.Fatal("rename/rename must allow a custom name")
	}

	// All three resolution kinds work.
	for _, tc := range []struct{ choice, custom, want string }{
		{"ours", "", "email_address"},
		{"theirs", "", "contact_email"},
		{"custom", "primary_email", "primary_email"},
	} {
		merged := mustClean(t, run(t, base, ours, theirs,
			Resolution{ConflictID: c.ID, Choice: Choice(tc.choice), Custom: tc.custom}))
		col := merged.TableByName("users").ColumnByID(emailID)
		if col == nil || col.Name != tc.want {
			t.Fatalf("resolution %s: column name = %v, want %s", tc.choice, col, tc.want)
		}
	}
}

func TestC1CleanRenameOneSide(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	ours := edit(t, base, ops.Op{Op: ops.RenameTable, TableID: users.ID, Name: "accounts"})
	merged := mustClean(t, run(t, base, ours, base.Clone()))
	if merged.TableByName("accounts") == nil {
		t.Fatal("one-sided table rename must merge cleanly")
	}
}

// --- C2: retype / retype ---

func TestC2RetypeRetypeConflicts(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	emailID := users.ColumnByName("email").ID
	ours := edit(t, base, ops.Op{Op: ops.RetypeColumn, TableID: users.ID, ColumnID: emailID, Type: &schema.DataType{Base: "varchar", Params: []int{500}}})
	theirs := edit(t, base, ops.Op{Op: ops.RetypeColumn, TableID: users.ID, ColumnID: emailID, Type: &schema.DataType{Base: "text"}})

	res := run(t, base, ours, theirs)
	c := oneConflict(t, res, "retype_retype")
	if c.Base != "varchar(255)" || c.OursValue != "varchar(500)" || c.TheirsValue != "text" {
		t.Fatalf("conflict values wrong: %+v", c)
	}

	// Custom type resolution parses back into a structured type.
	merged := mustClean(t, run(t, base, ours, theirs,
		Resolution{ConflictID: c.ID, Choice: Custom, Custom: "varchar(1000)"}))
	got := merged.TableByName("users").ColumnByID(emailID).Type
	if got.Base != "varchar" || len(got.Params) != 1 || got.Params[0] != 1000 {
		t.Fatalf("custom type = %v, want varchar(1000)", got)
	}
}

func TestC2CleanRenamePlusRetypeDifferentProperties(t *testing.T) {
	// The signature clean case: both sides touched the SAME column but
	// different properties — rename on one, retype on the other.
	base := baseSchema(t)
	users := base.TableByName("users")
	emailID := users.ColumnByName("email").ID
	ours := edit(t, base, ops.Op{Op: ops.RetypeColumn, TableID: users.ID, ColumnID: emailID, Type: &schema.DataType{Base: "text"}})
	theirs := edit(t, base, ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: emailID, Name: "email_address"})

	merged := mustClean(t, run(t, base, ours, theirs))
	col := merged.TableByName("users").ColumnByID(emailID)
	if col.Name != "email_address" || col.Type.Base != "text" {
		t.Fatalf("per-property merge failed: %+v", col)
	}
}

// --- C3: delete vs modify ---

func TestC3DeleteVsModifyConflicts(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	emailID := users.ColumnByName("email").ID
	ours := edit(t, base,
		ops.Op{Op: ops.DropIndex, TableID: base.TableByName("orders").ID, IndexID: base.TableByName("orders").Indexes[0].ID},
		ops.Op{Op: ops.DropConstraint, TableID: users.ID, ConstraintID: uniqueOrPK(t, users)},
		ops.Op{Op: ops.DropColumn, TableID: users.ID, ColumnID: emailID})
	theirs := edit(t, base, ops.Op{Op: ops.RetypeColumn, TableID: users.ID, ColumnID: emailID, Type: &schema.DataType{Base: "text"}})

	res := run(t, base, ours, theirs)
	var found *Conflict
	for i, c := range res.Conflicts {
		if c.Class == "delete_modify" && c.Property == "existence" && c.Object == "email" {
			found = &res.Conflicts[i]
		}
	}
	if found == nil {
		t.Fatalf("expected delete_modify conflict on email, got %+v", res.Conflicts)
	}

	// ours = keep the drop; theirs = keep the modified column.
	resolved := run(t, base, ours, theirs, Resolution{ConflictID: found.ID, Choice: Theirs})
	if len(resolved.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts after resolution: %+v", resolved.Conflicts)
	}
	col := resolved.Schema.TableByName("users").ColumnByID(emailID)
	if col == nil || col.Type.Base != "text" {
		t.Fatalf("theirs resolution must keep the modified column, got %+v", col)
	}
}

func TestC3CleanDropVsUntouched(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	nameID := users.ColumnByName("name").ID
	ours := edit(t, base, ops.Op{Op: ops.DropColumn, TableID: users.ID, ColumnID: nameID})

	merged := mustClean(t, run(t, base, ours, base.Clone()))
	if merged.TableByName("users").ColumnByID(nameID) != nil {
		t.Fatal("a drop against an untouched side must win silently")
	}
}

// --- C4: add / add name collision ---

func TestC4AddAddSameColumnNameConflicts(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	ours := edit(t, base, ops.Op{Op: ops.AddColumn, TableID: users.ID,
		Column: &ops.ColumnSpec{Name: "status", Type: schema.DataType{Base: "varchar", Params: []int{20}}, Nullable: true}})
	theirs := edit(t, base, ops.Op{Op: ops.AddColumn, TableID: users.ID,
		Column: &ops.ColumnSpec{Name: "status", Type: schema.DataType{Base: "integer"}}})

	res := run(t, base, ours, theirs)
	c := oneConflict(t, res, "name_collision")
	if !c.AllowCustom {
		t.Fatal("add/add must allow renaming one side")
	}

	// Custom keeps both columns: theirs' gets the new name.
	merged := mustClean(t, run(t, base, ours, theirs,
		Resolution{ConflictID: c.ID, Choice: Custom, Custom: "status_code"}))
	u := merged.TableByName("users")
	if u.ColumnByName("status") == nil || u.ColumnByName("status_code") == nil {
		t.Fatalf("custom rename must keep both columns, got %+v", u.Columns)
	}
	// ours keeps only ours'.
	merged2 := mustClean(t, run(t, base, ours, theirs, Resolution{ConflictID: c.ID, Choice: Ours}))
	if got := merged2.TableByName("users").ColumnByName("status"); got == nil || got.Type.Base != "varchar" {
		t.Fatalf("ours resolution must keep ours' column, got %+v", got)
	}
}

func TestC4CleanTwoRenamesCollide(t *testing.T) {
	// The unwinnable-in-binary case: ours renames users→archive, theirs
	// renames orders→archive. Both individually clean; only a new name fixes it.
	base := baseSchema(t)
	ours := edit(t, base, ops.Op{Op: ops.RenameTable, TableID: base.TableByName("users").ID, Name: "archive"})
	theirs := edit(t, base, ops.Op{Op: ops.RenameTable, TableID: base.TableByName("orders").ID, Name: "archive"})

	res := run(t, base, ours, theirs)
	c := oneConflict(t, res, "name_collision")
	merged := mustClean(t, run(t, base, ours, theirs,
		Resolution{ConflictID: c.ID, Choice: Custom, Custom: "orders_archive"}))
	if merged.TableByName("archive") == nil || merged.TableByName("orders_archive") == nil {
		t.Fatal("custom rename must keep both tables under distinct names")
	}
}

// --- C5: constraint divergence ---

func TestC5BothAddDifferentPrimaryKeys(t *testing.T) {
	base := baseSchema(t)
	// Strip users' PK from the base so both sides can add one.
	users := base.TableByName("users")
	noPK := edit(t, base, ops.Op{Op: ops.DropConstraint, TableID: users.ID, ConstraintID: users.PrimaryKey().ID})
	u := noPK.TableByName("users")

	ours := edit(t, noPK, ops.Op{Op: ops.AddConstraint, TableID: u.ID,
		Constraint: &ops.ConstraintSpec{Kind: schema.PrimaryKey, ColumnIDs: []schema.ObjectID{u.ColumnByName("id").ID}}})
	theirs := edit(t, noPK, ops.Op{Op: ops.AddConstraint, TableID: u.ID,
		Constraint: &ops.ConstraintSpec{Kind: schema.PrimaryKey, ColumnIDs: []schema.ObjectID{u.ColumnByName("email").ID}}})

	res := run(t, noPK, ours, theirs)
	c := oneConflict(t, res, "pk_pk")
	merged := mustClean(t, run(t, noPK, ours, theirs, Resolution{ConflictID: c.ID, Choice: Theirs}))
	pk := merged.TableByName("users").PrimaryKey()
	if pk == nil || merged.TableByName("users").ColumnByID(pk.ColumnIDs[0]).Name != "email" {
		t.Fatalf("theirs resolution must keep theirs' PK, got %+v", pk)
	}
}

func TestC5CleanIdenticalConstraintAddedBothSides(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	addUnique := ops.Op{Op: ops.AddConstraint, TableID: users.ID,
		Constraint: &ops.ConstraintSpec{Kind: schema.Unique, ColumnIDs: []schema.ObjectID{users.ColumnByName("email").ID}}}

	merged := mustClean(t, run(t, base, edit(t, base, addUnique), edit(t, base, addUnique)))
	uniques := 0
	for _, c := range merged.TableByName("users").Constraints {
		if c.Kind == schema.Unique {
			uniques++
		}
	}
	if uniques != 1 {
		t.Fatalf("structurally identical constraints must dedupe to one, got %d", uniques)
	}
}

// --- C6: index divergence ---

func TestC6IndexModifiedDifferently(t *testing.T) {
	base := baseSchema(t)
	orders := base.TableByName("orders")
	ixID := orders.Indexes[0].ID
	// "Modify" = drop and re-add with the same ID is impossible via ops; the
	// realistic divergence is one side dropping and re-creating vs the other
	// side... so modify directly on clones, as a re-import with preserved
	// identity would produce.
	ours := base.Clone()
	ours.TableByName("orders").Indexes[0].Unique = true
	theirs := base.Clone()
	theirs.TableByName("orders").Indexes[0].Columns = append(theirs.TableByName("orders").Indexes[0].Columns,
		schema.IndexColumn{ColumnID: theirs.TableByName("orders").ColumnByName("id").ID})

	res := run(t, base, ours, theirs)
	c := oneConflict(t, res, "index_index")
	if c.Object != "idx_orders_status" {
		t.Fatalf("conflict object = %q", c.Object)
	}
	merged := mustClean(t, run(t, base, ours, theirs, Resolution{ConflictID: c.ID, Choice: Ours}))
	if !merged.TableByName("orders").IndexByID(ixID).Unique {
		t.Fatal("ours resolution must keep ours' index definition")
	}
}

func TestC6CleanTwoNewIndexesClaimOneName(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	orders := base.TableByName("orders")
	ours := edit(t, base, ops.Op{Op: ops.AddIndex, TableID: users.ID,
		Index: &ops.IndexSpec{Name: "idx_new", Columns: []schema.IndexColumn{{ColumnID: users.ColumnByName("email").ID}}}})
	theirs := edit(t, base, ops.Op{Op: ops.AddIndex, TableID: orders.ID,
		Index: &ops.IndexSpec{Name: "idx_new", Columns: []schema.IndexColumn{{ColumnID: orders.ColumnByName("status").ID}}}})

	res := run(t, base, ours, theirs)
	c := oneConflict(t, res, "name_collision")
	merged := mustClean(t, run(t, base, ours, theirs,
		Resolution{ConflictID: c.ID, Choice: Custom, Custom: "idx_new_orders"}))
	total := len(merged.TableByName("users").Indexes) + len(merged.TableByName("orders").Indexes)
	if total != 3 { // idx_orders_status + both new ones
		t.Fatalf("custom rename must keep both indexes; total = %d", total)
	}
}

// --- C7: default / nullability divergence ---

func TestC7DefaultAndNullability(t *testing.T) {
	base := baseSchema(t)
	orders := base.TableByName("orders")
	statusID := orders.ColumnByName("status").ID

	ours := edit(t, base, ops.Op{Op: ops.SetDefault, TableID: orders.ID, ColumnID: statusID, Default: sp("'new'")})
	theirs := edit(t, base, ops.Op{Op: ops.SetDefault, TableID: orders.ID, ColumnID: statusID, Default: sp("'open'")})
	res := run(t, base, ours, theirs)
	c := oneConflict(t, res, "default_default")
	if !c.AllowCustom {
		t.Fatal("default divergence must allow a custom value")
	}

	// Clean adjacent case: one side changes the default, the other the
	// nullability — different properties, one column, no conflict.
	theirs2 := edit(t, base, ops.Op{Op: ops.SetNullable, TableID: orders.ID, ColumnID: statusID, Nullable: bp(true)})
	merged := mustClean(t, run(t, base, ours, theirs2))
	col := merged.TableByName("orders").ColumnByID(statusID)
	if col.Default != "'new'" || !col.Nullable {
		t.Fatalf("per-property merge failed: %+v", col)
	}
}

// --- C8: cross-object — valid alone, broken together ---

func TestC8DropTableVsAddFK(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	orders := base.TableByName("orders")

	// ours drops the FK then the users table; theirs adds a fresh FK
	// pointing at users. No property overlaps — the merge is conflict-free
	// and the RESULT is broken. Validation must catch it and block.
	ours := edit(t, base,
		ops.Op{Op: ops.DropConstraint, TableID: orders.ID, ConstraintID: fkOf(t, base, "orders").ID},
		ops.Op{Op: ops.DropColumn, TableID: orders.ID, ColumnID: orders.ColumnByName("user_id").ID},
		ops.Op{Op: ops.DropTable, TableID: users.ID})
	theirs := edit(t, base, ops.Op{Op: ops.AddColumn, TableID: orders.ID,
		Column: &ops.ColumnSpec{Name: "buyer_id", Type: schema.DataType{Base: "bigint"}, Nullable: true}})
	theirs = edit(t, theirs, ops.Op{Op: ops.AddConstraint, TableID: orders.ID,
		Constraint: &ops.ConstraintSpec{Kind: schema.ForeignKey,
			ColumnIDs:  []schema.ObjectID{theirs.TableByName("orders").ColumnByName("buyer_id").ID},
			RefTableID: users.ID, RefColumnIDs: []schema.ObjectID{users.ColumnByName("id").ID}}})

	res := run(t, base, ours, theirs)
	if len(res.Conflicts) != 0 {
		t.Fatalf("C8 must NOT be a property conflict, got %+v", res.Conflicts)
	}
	found := false
	for _, p := range res.Problems {
		if p.Code == "fk_missing_table" {
			found = true
		}
	}
	if !found {
		t.Fatalf("validation must report the dangling FK, got %+v", res.Problems)
	}
}

func TestC8CleanDisjointChanges(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	orders := base.TableByName("orders")
	ours := edit(t, base, ops.Op{Op: ops.AddColumn, TableID: users.ID,
		Column: &ops.ColumnSpec{Name: "age", Type: schema.DataType{Base: "integer"}, Nullable: true}})
	theirs := edit(t, base, ops.Op{Op: ops.AddColumn, TableID: orders.ID,
		Column: &ops.ColumnSpec{Name: "note", Type: schema.DataType{Base: "text"}, Nullable: true}})

	merged := mustClean(t, run(t, base, ours, theirs))
	if merged.TableByName("users").ColumnByName("age") == nil ||
		merged.TableByName("orders").ColumnByName("note") == nil {
		t.Fatal("disjoint additions must both land")
	}
	if len(run(t, base, ours, theirs).Changes) == 0 {
		t.Fatal("a clean merge must report its changes against ours")
	}
}

// --- resolution plumbing ---

func TestResolutionErrors(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	emailID := users.ColumnByName("email").ID
	ours := edit(t, base, ops.Op{Op: ops.SetNullable, TableID: users.ID, ColumnID: emailID, Nullable: bp(true)})
	theirs := base.Clone()
	theirs.TableByName("users").ColumnByID(emailID).Nullable = false
	theirs.TableByName("users").ColumnByID(emailID).Default = "'x'" // force divergence marker
	theirs2 := edit(t, base, ops.Op{Op: ops.SetDefault, TableID: users.ID, ColumnID: emailID, Default: sp("'x'")})
	_ = theirs
	_ = theirs2

	if _, err := Merge(Input{Base: base, Ours: ours, Theirs: base.Clone(),
		Resolutions: []Resolution{{ConflictID: "x", Choice: "sideways"}}}); err == nil {
		t.Fatal("unknown choice must error")
	}
	if _, err := Merge(Input{Base: base, Ours: ours, Theirs: base.Clone(),
		Resolutions: []Resolution{{ConflictID: "x", Choice: Custom, Custom: "  "}}}); err == nil {
		t.Fatal("custom with empty value must error")
	}

	// Custom on a conflict that doesn't accept it is an error.
	oursN := edit(t, base, ops.Op{Op: ops.SetNullable, TableID: users.ID, ColumnID: users.ColumnByName("name").ID, Nullable: bp(false)})
	theirsN := base.Clone()
	theirsN.TableByName("users").ColumnByName("name").Default = "'d'"
	theirsN.TableByName("users").ColumnByName("name").Nullable = true
	// build a nullable conflict: ours NOT NULL, theirs stays nullable but base nullable → only ours changed... need both changed:
	theirsBoth := edit(t, base, ops.Op{Op: ops.DropColumn, TableID: users.ID, ColumnID: users.ColumnByName("name").ID})
	resC := run(t, base, oursN, theirsBoth)
	if len(resC.Conflicts) != 1 {
		t.Fatalf("setup: want 1 delete_modify conflict, got %+v", resC.Conflicts)
	}
	if _, err := Merge(Input{Base: base, Ours: oursN, Theirs: theirsBoth,
		Resolutions: []Resolution{{ConflictID: resC.Conflicts[0].ID, Choice: Custom, Custom: "zz"}}}); err == nil {
		t.Fatal("custom on a non-custom conflict must error")
	}
}

func TestConflictOrderIsDeterministic(t *testing.T) {
	base := baseSchema(t)
	users := base.TableByName("users")
	emailID := users.ColumnByName("email").ID
	nameID := users.ColumnByName("name").ID
	ours := edit(t, base,
		ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: emailID, Name: "a1"},
		ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: nameID, Name: "b1"})
	theirs := edit(t, base,
		ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: emailID, Name: "a2"},
		ops.Op{Op: ops.RenameColumn, TableID: users.ID, ColumnID: nameID, Name: "b2"})

	first := run(t, base, ours, theirs)
	for range 5 {
		again := run(t, base, ours, theirs)
		if len(again.Conflicts) != len(first.Conflicts) {
			t.Fatal("conflict count not deterministic")
		}
		for i := range again.Conflicts {
			if again.Conflicts[i].ID != first.Conflicts[i].ID {
				t.Fatal("conflict order not deterministic")
			}
		}
	}
}

func TestParseType(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"text", "text", false},
		{"varchar(255)", "varchar(255)", false},
		{"numeric(10,2)", "numeric(10,2)", false},
		{"double precision", "double precision", false},
		{" VARCHAR( 40 ) ", "varchar(40)", false},
		{"", "", true},
		{"(5)", "", true},
	}
	for _, c := range cases {
		got, err := ParseType(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseType(%q): want error", c.in)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("ParseType(%q) = %v, %v; want %s", c.in, got, err, c.want)
		}
	}
}

// --- helpers ---

func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }

func fkOf(t *testing.T, s *schema.Schema, table string) *schema.Constraint {
	t.Helper()
	for i, c := range s.TableByName(table).Constraints {
		if c.Kind == schema.ForeignKey {
			return &s.TableByName(table).Constraints[i]
		}
	}
	t.Fatalf("no FK on %s", table)
	return nil
}

func uniqueOrPK(t *testing.T, table *schema.Table) schema.ObjectID {
	t.Helper()
	if pk := table.PrimaryKey(); pk != nil {
		return pk.ID
	}
	t.Fatal("no PK")
	return ""
}

// guard against unused-import lint in edited test variants
var _ = strings.TrimSpace
