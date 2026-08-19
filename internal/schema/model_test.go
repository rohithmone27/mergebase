package schema

import "testing"

func sample() *Schema {
	users := Table{
		ID:   NewObjectID(),
		Name: "users",
		Columns: []Column{
			{ID: NewObjectID(), Name: "id", Type: DataType{Base: "bigint"}, Position: 1},
			{ID: NewObjectID(), Name: "email", Type: DataType{Base: "varchar", Params: []int{255}}, Nullable: true, Position: 2},
		},
	}
	users.Constraints = []Constraint{
		{ID: NewObjectID(), Kind: PrimaryKey, ColumnIDs: []ObjectID{users.Columns[0].ID}},
	}
	return &Schema{Tables: []Table{users}}
}

func TestNewObjectIDIsUnique(t *testing.T) {
	seen := map[ObjectID]bool{}
	for range 10_000 {
		id := NewObjectID()
		if seen[id] {
			t.Fatalf("duplicate ObjectID generated: %s", id)
		}
		seen[id] = true
	}
}

func TestLookupsByIDAndName(t *testing.T) {
	s := sample()
	users := s.TableByName("users")
	if users == nil {
		t.Fatal("TableByName(users) = nil")
	}
	if got := s.TableByID(users.ID); got == nil || got.Name != "users" {
		t.Fatalf("TableByID = %v, want users", got)
	}
	email := users.ColumnByName("email")
	if email == nil {
		t.Fatal("ColumnByName(email) = nil")
	}
	if got := users.ColumnByID(email.ID); got == nil || got.Name != "email" {
		t.Fatalf("ColumnByID = %v, want email", got)
	}
	if s.TableByName("missing") != nil || users.ColumnByName("missing") != nil {
		t.Fatal("lookups for missing names must return nil")
	}
	if pk := users.PrimaryKey(); pk == nil || len(pk.ColumnIDs) != 1 {
		t.Fatalf("PrimaryKey = %v, want single-column PK", users.PrimaryKey())
	}
}

func TestCloneIsDeepAndIdentityPreserving(t *testing.T) {
	s := sample()
	c := s.Clone()

	// Identity must survive cloning: same IDs in the copy.
	if c.Tables[0].ID != s.Tables[0].ID || c.Tables[0].Columns[1].ID != s.Tables[0].Columns[1].ID {
		t.Fatal("clone must preserve ObjectIDs")
	}

	// Mutating the clone must not touch the original — a rename in a branch
	// must never leak into its parent snapshot.
	c.Tables[0].Name = "accounts"
	c.Tables[0].Columns[1].Name = "email_address"
	c.Tables[0].Columns[1].Type = DataType{Base: "text"}
	if s.Tables[0].Name != "users" || s.Tables[0].Columns[1].Name != "email" {
		t.Fatal("mutating a clone leaked into the original")
	}
	if s.Tables[0].Columns[1].Type.Base != "varchar" {
		t.Fatal("mutating a clone's column type leaked into the original")
	}
}

func TestDataTypeEqualAndString(t *testing.T) {
	cases := []struct {
		a, b  DataType
		equal bool
		str   string
	}{
		{DataType{Base: "text"}, DataType{Base: "text"}, true, "text"},
		{DataType{Base: "varchar", Params: []int{255}}, DataType{Base: "varchar", Params: []int{255}}, true, "varchar(255)"},
		{DataType{Base: "varchar", Params: []int{255}}, DataType{Base: "varchar", Params: []int{500}}, false, "varchar(255)"},
		{DataType{Base: "numeric", Params: []int{10, 2}}, DataType{Base: "numeric", Params: []int{10}}, false, "numeric(10,2)"},
		{DataType{Base: "varchar", Params: []int{255}}, DataType{Base: "text"}, false, "varchar(255)"},
	}
	for _, c := range cases {
		if got := c.a.Equal(c.b); got != c.equal {
			t.Errorf("%v.Equal(%v) = %v, want %v", c.a, c.b, got, c.equal)
		}
		if got := c.a.String(); got != c.str {
			t.Errorf("%v.String() = %q, want %q", c.a, got, c.str)
		}
	}
}
