package parser

import (
	"strings"
	"testing"

	"github.com/rohithmone27/mergebase/internal/schema"
)

func mustParse(t *testing.T, ddl string) *Result {
	t.Helper()
	res, err := Parse(ddl)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return res
}

func TestCreateTableColumnsTypesDefaults(t *testing.T) {
	res := mustParse(t, `
		CREATE TABLE users (
			id         BIGSERIAL PRIMARY KEY,
			email      VARCHAR(255) NOT NULL,
			nickname   TEXT,
			balance    NUMERIC(10,2) DEFAULT 0,
			currency   VARCHAR(3) NOT NULL DEFAULT 'INR',
			created_at TIMESTAMP DEFAULT now()
		);`)

	users := res.Schema.TableByName("users")
	if users == nil {
		t.Fatal("table users not parsed")
	}
	if len(users.Columns) != 6 {
		t.Fatalf("got %d columns, want 6", len(users.Columns))
	}

	cases := []struct {
		name     string
		typ      string
		nullable bool
		def      string
		position int
	}{
		{"id", "bigserial", false, "", 1}, // PK member ⇒ NOT NULL
		{"email", "varchar(255)", false, "", 2},
		{"nickname", "text", true, "", 3},
		{"balance", "numeric(10,2)", true, "0", 4},
		{"currency", "varchar(3)", false, "'INR'", 5},
		{"created_at", "timestamp", true, "now()", 6},
	}
	for _, c := range cases {
		col := users.ColumnByName(c.name)
		if col == nil {
			t.Fatalf("column %s not parsed", c.name)
		}
		if got := col.Type.String(); got != c.typ {
			t.Errorf("%s type = %q, want %q", c.name, got, c.typ)
		}
		if col.Nullable != c.nullable {
			t.Errorf("%s nullable = %v, want %v", c.name, col.Nullable, c.nullable)
		}
		if col.Default != c.def {
			t.Errorf("%s default = %q, want %q", c.name, col.Default, c.def)
		}
		if col.Position != c.position {
			t.Errorf("%s position = %d, want %d", c.name, col.Position, c.position)
		}
	}
	pk := users.PrimaryKey()
	if pk == nil || len(pk.ColumnIDs) != 1 || pk.ColumnIDs[0] != users.ColumnByName("id").ID {
		t.Fatalf("primary key = %+v, want single column id", pk)
	}
	if len(res.Unsupported) != 0 {
		t.Fatalf("unexpected unsupported entries: %+v", res.Unsupported)
	}
}

func TestConstraintsInlineAndTableLevel(t *testing.T) {
	res := mustParse(t, `
		CREATE TABLE users (
			id    BIGINT PRIMARY KEY,
			email TEXT UNIQUE,
			age   INT CHECK (age >= 18)
		);
		CREATE TABLE orders (
			id      BIGINT,
			user_id BIGINT REFERENCES users,
			buyer   TEXT,
			total   NUMERIC(10,2),
			CONSTRAINT orders_pk PRIMARY KEY (id),
			CONSTRAINT buyer_unique UNIQUE (buyer),
			FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE ON UPDATE SET NULL,
			CHECK (total >= 0)
		);`)

	users := res.Schema.TableByName("users")
	orders := res.Schema.TableByName("orders")

	if got := kinds(users.Constraints); got != "primary_key,unique,check" {
		t.Fatalf("users constraints = %s", got)
	}
	// FKs resolve in a second pass (they may reference tables defined later),
	// so they always append after the table's other constraints.
	if got := kinds(orders.Constraints); got != "primary_key,unique,check,foreign_key,foreign_key" {
		t.Fatalf("orders constraints = %s", got)
	}

	// The inline `REFERENCES users` (no column list) must resolve to users'
	// primary key — by ID.
	var inlineFK, explicitFK *schema.Constraint
	for i := range orders.Constraints {
		c := &orders.Constraints[i]
		if c.Kind != schema.ForeignKey {
			continue
		}
		if c.Name == "" && len(c.ColumnIDs) == 1 && c.OnDelete == schema.NoAction {
			inlineFK = c
		}
		if c.OnDelete == schema.Cascade {
			explicitFK = c
		}
	}
	if inlineFK == nil || explicitFK == nil {
		t.Fatalf("both FKs must be parsed; got %+v", orders.Constraints)
	}
	for _, fk := range []*schema.Constraint{inlineFK, explicitFK} {
		if fk.RefTableID != users.ID {
			t.Fatalf("FK RefTableID = %s, want users' ID", fk.RefTableID)
		}
		if len(fk.RefColumnIDs) != 1 || fk.RefColumnIDs[0] != users.ColumnByName("id").ID {
			t.Fatalf("FK RefColumnIDs = %v, want [users.id]", fk.RefColumnIDs)
		}
		if fk.ColumnIDs[0] != orders.ColumnByName("user_id").ID {
			t.Fatalf("FK ColumnIDs = %v, want [orders.user_id]", fk.ColumnIDs)
		}
	}
	if explicitFK.OnUpdate != schema.SetNull {
		t.Errorf("explicit FK OnUpdate = %s, want set_null", explicitFK.OnUpdate)
	}

	// CHECK expressions round-trip as text.
	var checkExpr string
	for _, c := range orders.Constraints {
		if c.Kind == schema.Check {
			checkExpr = c.Expr
		}
	}
	if checkExpr != "total >= 0" {
		t.Errorf("check expr = %q, want %q", checkExpr, "total >= 0")
	}
}

func TestCreateIndex(t *testing.T) {
	res := mustParse(t, `
		CREATE TABLE t (a INT, b INT);
		CREATE INDEX idx_a ON t (a);
		CREATE UNIQUE INDEX idx_ab ON t (a, b DESC);
		CREATE INDEX idx_hash ON t USING hash (a);`)

	tbl := res.Schema.TableByName("t")
	if len(tbl.Indexes) != 3 {
		t.Fatalf("got %d indexes, want 3", len(tbl.Indexes))
	}
	byName := map[string]schema.Index{}
	for _, ix := range tbl.Indexes {
		byName[ix.Name] = ix
	}
	if ix := byName["idx_a"]; ix.Unique || len(ix.Columns) != 1 || ix.Columns[0].ColumnID != tbl.ColumnByName("a").ID || ix.Method != "" {
		t.Errorf("idx_a parsed wrong: %+v", ix)
	}
	if ix := byName["idx_ab"]; !ix.Unique || len(ix.Columns) != 2 || !ix.Columns[1].Desc {
		t.Errorf("idx_ab parsed wrong: %+v", ix)
	}
	if ix := byName["idx_hash"]; ix.Method != "hash" {
		t.Errorf("idx_hash method = %q, want hash", ix.Method)
	}
}

func TestQuotedIdentifiers(t *testing.T) {
	res := mustParse(t, `CREATE TABLE "Order Items" ("Item ID" INT PRIMARY KEY, "user id" BIGINT);`)
	tbl := res.Schema.TableByName("Order Items")
	if tbl == nil {
		t.Fatal("quoted table name not preserved")
	}
	if tbl.ColumnByName("Item ID") == nil || tbl.ColumnByName("user id") == nil {
		t.Fatal("quoted column names not preserved")
	}
}

func TestUnknownReferencesAreErrors(t *testing.T) {
	cases := []struct {
		name, ddl, wantErr string
	}{
		{"fk to missing table", `CREATE TABLE o (uid INT REFERENCES users (id));`, `"users"`},
		{"fk to missing column", `CREATE TABLE u (id INT); CREATE TABLE o (uid INT REFERENCES u (nope));`, `"nope"`},
		{"fk with no list and no pk", `CREATE TABLE u (id INT); CREATE TABLE o (uid INT REFERENCES u);`, "no primary key"},
		{"index on missing table", `CREATE INDEX i ON missing (a);`, `"missing"`},
		{"index on missing column", `CREATE TABLE t (a INT); CREATE INDEX i ON t (b);`, `"b"`},
		{"duplicate table", `CREATE TABLE t (a INT); CREATE TABLE t (b INT);`, "duplicate table"},
		{"duplicate column", `CREATE TABLE t (a INT, a TEXT);`, "duplicate column"},
		{"pk on missing column", `CREATE TABLE t (a INT, PRIMARY KEY (b));`, `"b"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.ddl)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestMalformedSQLIsAnError(t *testing.T) {
	_, err := Parse(`CREATE TABLE (;`)
	if err == nil || !strings.Contains(err.Error(), "invalid SQL") {
		t.Fatalf("err = %v, want invalid SQL", err)
	}
}

func TestUnsupportedIsRecordedNeverDropped(t *testing.T) {
	res := mustParse(t, `
		CREATE TABLE t (a INT, b TEXT COLLATE "en_US");
		CREATE SEQUENCE t_seq;
		CREATE VIEW v AS SELECT a FROM t;
		CREATE INDEX partial_idx ON t (a) WHERE a > 0;
		CREATE INDEX expr_idx ON t (lower(b));
		COMMENT ON TABLE t IS 'hello';`)

	// The supported parts still import.
	if res.Schema.TableByName("t") == nil {
		t.Fatal("supported table must import despite unsupported statements")
	}
	// Skipped indexes must not half-import.
	if n := len(res.Schema.TableByName("t").Indexes); n != 0 {
		t.Fatalf("partial/expression indexes must be skipped whole, got %d indexes", n)
	}

	want := []string{
		"COLLATE clause",   // b TEXT COLLATE
		"CREATE SEQ",       // sequence
		"VIEW",             // view
		"partial index",    // WHERE clause
		"expression index", // lower(b)
		"COMMENT",          // comment
	}
	if len(res.Unsupported) != len(want) {
		t.Fatalf("unsupported = %+v, want %d entries", res.Unsupported, len(want))
	}
	for i, w := range want {
		if !strings.Contains(res.Unsupported[i].Construct, w) {
			t.Errorf("unsupported[%d] = %+v, want construct containing %q", i, res.Unsupported[i], w)
		}
	}
}

func TestAlterTableAddConstraintAndColumn(t *testing.T) {
	res := mustParse(t, `
		CREATE TABLE u (id BIGINT);
		CREATE TABLE o (id BIGINT, uid BIGINT);
		ALTER TABLE u ADD CONSTRAINT u_pk PRIMARY KEY (id);
		ALTER TABLE o ADD CONSTRAINT o_fk FOREIGN KEY (uid) REFERENCES u (id);
		ALTER TABLE o ADD COLUMN note TEXT;
		ALTER TABLE o ALTER COLUMN id SET NOT NULL;`)

	u, o := res.Schema.TableByName("u"), res.Schema.TableByName("o")
	if u.PrimaryKey() == nil {
		t.Fatal("ALTER TABLE ADD CONSTRAINT pk not applied")
	}
	if got := kinds(o.Constraints); got != "foreign_key" {
		t.Fatalf("o constraints = %s, want foreign_key", got)
	}
	if o.ColumnByName("note") == nil {
		t.Fatal("ALTER TABLE ADD COLUMN not applied")
	}
	// Unsupported ALTER subtypes are recorded.
	found := false
	for _, us := range res.Unsupported {
		if strings.Contains(us.Construct, "ALTER TABLE") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unsupported ALTER subtype must be recorded, got %+v", res.Unsupported)
	}
}

func TestSchemaQualifiedNamesImportWithWarning(t *testing.T) {
	res := mustParse(t, `CREATE TABLE billing.invoices (id INT PRIMARY KEY);`)
	if res.Schema.TableByName("invoices") == nil {
		t.Fatal("schema-qualified table must import under its bare name")
	}
	if len(res.Unsupported) != 1 || !strings.Contains(res.Unsupported[0].Construct, "schema-qualified") {
		t.Fatalf("unsupported = %+v, want schema-qualified warning", res.Unsupported)
	}
}

func kinds(cs []schema.Constraint) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, string(c.Kind))
	}
	return strings.Join(parts, ",")
}
