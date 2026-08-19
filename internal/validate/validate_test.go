package validate

import (
	"testing"

	"mergebase/internal/parser"
	"mergebase/internal/schema"
)

func valid(t *testing.T) *schema.Schema {
	t.Helper()
	res, err := parser.Parse(`
		CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL);
		CREATE TABLE orders (id BIGINT PRIMARY KEY, user_id BIGINT REFERENCES users (id));
		CREATE INDEX idx_orders_user ON orders (user_id);`)
	if err != nil {
		t.Fatal(err)
	}
	return res.Schema
}

func codes(problems []Problem) map[string]int {
	out := map[string]int{}
	for _, p := range problems {
		out[p.Code]++
	}
	return out
}

func TestValidSchemaHasNoProblems(t *testing.T) {
	if got := Check(valid(t)); len(got) != 0 {
		t.Fatalf("problems on a valid schema: %+v", got)
	}
}

func TestEveryCheck(t *testing.T) {
	s := valid(t)
	users, orders := s.TableByName("users"), s.TableByName("orders")

	// Break it in every way at once.
	orders.Name = "users"                                            // duplicate table name
	users.Columns[1].Name = "id"                                     // duplicate column name
	users.Columns[0].Nullable = true                                 // nullable PK member
	fk := &orders.Constraints[1]                                     // the FK
	fk.RefTableID = "gone"                                           // dangling FK target
	orders.Indexes[0].Columns[0].ColumnID = "gone"                   // index on missing column
	users.Constraints = append(users.Constraints, schema.Constraint{ // second PK
		ID: schema.NewObjectID(), Kind: schema.PrimaryKey, ColumnIDs: []schema.ObjectID{users.Columns[0].ID},
	})
	users.Indexes = append(users.Indexes, schema.Index{ // duplicate index name
		ID: schema.NewObjectID(), Name: "idx_orders_user",
		Columns: []schema.IndexColumn{{ColumnID: users.Columns[0].ID}},
	})
	s.Tables = append(s.Tables, schema.Table{ID: schema.NewObjectID(), Name: "empty"}) // no columns

	got := codes(Check(s))
	for _, want := range []string{
		"duplicate_table_name", "duplicate_column_name", "pk_nullable_column",
		"fk_missing_table", "index_missing_column", "multiple_primary_keys",
		"duplicate_index_name", "table_no_columns",
	} {
		if got[want] == 0 {
			t.Errorf("missing problem %q; got %v", want, got)
		}
	}
}

func TestFKColumnChecks(t *testing.T) {
	s := valid(t)
	orders := s.TableByName("orders")
	fk := &orders.Constraints[1]
	fk.RefColumnIDs = []schema.ObjectID{"gone"}
	got := codes(Check(s))
	if got["fk_missing_column"] == 0 {
		t.Fatalf("missing fk_missing_column: %v", got)
	}

	s2 := valid(t)
	fk2 := &s2.TableByName("orders").Constraints[1]
	fk2.RefColumnIDs = append(fk2.RefColumnIDs, s2.TableByName("users").Columns[1].ID)
	if got := codes(Check(s2)); got["fk_column_count_mismatch"] == 0 {
		t.Fatalf("missing fk_column_count_mismatch: %v", got)
	}
}
