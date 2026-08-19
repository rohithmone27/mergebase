package match

import (
	"testing"

	"mergebase/internal/merge"
	"mergebase/internal/parser"
	"mergebase/internal/schema"
)

const ddl = `
CREATE TABLE users (
	id    BIGINT PRIMARY KEY,
	email VARCHAR(255) NOT NULL,
	name  TEXT
);
CREATE TABLE orders (
	id      BIGINT PRIMARY KEY,
	user_id BIGINT NOT NULL REFERENCES users (id)
);
CREATE INDEX idx_users_email ON users (email);`

func parse(t *testing.T, sql string) *schema.Schema {
	t.Helper()
	res, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return res.Schema
}

// Invariant 7: import → re-import identical DDL → identity fully preserved,
// so merging the re-imported side against a sibling yields ZERO conflicts.
func TestIdenticalReimportPreservesAllIdentity(t *testing.T) {
	head := parse(t, ddl)
	reimported := parse(t, ddl) // fresh IDs everywhere

	out := Rematch(head, reimported, nil)
	if len(out.Proposals) != 0 {
		t.Fatalf("identical re-import must need no confirmations: %+v", out.Proposals)
	}

	// Every object carries the head's ID.
	for _, ht := range head.Tables {
		nt := out.Schema.TableByID(ht.ID)
		if nt == nil {
			t.Fatalf("table %q lost its ID", ht.Name)
		}
		for _, hc := range ht.Columns {
			if nt.ColumnByID(hc.ID) == nil {
				t.Fatalf("column %s.%s lost its ID", ht.Name, hc.Name)
			}
		}
		if len(nt.Constraints) != len(ht.Constraints) || len(nt.Indexes) != len(ht.Indexes) {
			t.Fatalf("members of %q not transplanted", ht.Name)
		}
		for i := range ht.Constraints {
			if nt.ConstraintByID(ht.Constraints[i].ID) == nil {
				t.Fatalf("constraint on %q lost its ID", ht.Name)
			}
		}
		for i := range ht.Indexes {
			if nt.IndexByID(ht.Indexes[i].ID) == nil {
				t.Fatalf("index on %q lost its ID", ht.Name)
			}
		}
	}

	// The merge-level proof: base=head, ours=head (untouched sibling),
	// theirs=re-imported — must merge with zero conflicts and zero changes.
	res, err := merge.Merge(merge.Input{Base: head, Ours: head.Clone(), Theirs: out.Schema,
		OursName: "main", TheirsName: "reimport"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 || len(res.Problems) != 0 {
		t.Fatalf("re-import broke identity: conflicts=%+v problems=%+v", res.Conflicts, res.Problems)
	}
	if len(res.Changes) != 0 {
		t.Fatalf("identical re-import must be a no-op merge, got changes: %+v", res.Changes)
	}
}

func TestRenameIsProposedNeverSilentlyApplied(t *testing.T) {
	head := parse(t, ddl)
	renamed := parse(t, `
		CREATE TABLE users (
			id            BIGINT PRIMARY KEY,
			email_address VARCHAR(255) NOT NULL,
			name          TEXT
		);
		CREATE TABLE orders (
			id      BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users (id)
		);
		CREATE INDEX idx_users_email ON users (email_address);`)

	out := Rematch(head, renamed, nil)
	if len(out.Proposals) != 1 {
		t.Fatalf("want exactly one rename proposal, got %+v", out.Proposals)
	}
	p := out.Proposals[0]
	if p.Kind != "column" || p.OldName != "email" || p.NewName != "email_address" {
		t.Fatalf("proposal wrong: %+v", p)
	}
	if p.Confidence < 0.5 {
		t.Fatalf("same type + position + similar name should score confidently, got %v", p.Confidence)
	}
	// Not applied yet: the imported column still carries a fresh ID.
	headEmail := head.TableByName("users").ColumnByName("email")
	if out.Schema.TableByName("users").ColumnByName("email_address").ID == headEmail.ID {
		t.Fatal("rename must not be silently applied before confirmation")
	}

	// Confirm the rename: identity transplants.
	confirmed := Rematch(head, renamed, []Decision{{OldID: p.OldID, Rename: true}})
	if len(confirmed.Proposals) != 0 {
		t.Fatalf("confirmed rename must leave no proposals: %+v", confirmed.Proposals)
	}
	if confirmed.Schema.TableByName("users").ColumnByName("email_address").ID != headEmail.ID {
		t.Fatal("confirmed rename must transplant the head's column ID")
	}

	// Decline the rename: stays drop + add with the fresh ID.
	declined := Rematch(head, renamed, []Decision{{OldID: p.OldID, Rename: false}})
	if len(declined.Proposals) != 0 {
		t.Fatalf("declined rename must not re-propose: %+v", declined.Proposals)
	}
	if declined.Schema.TableByName("users").ColumnByName("email_address").ID == headEmail.ID {
		t.Fatal("declined rename must keep the fresh ID (drop + add)")
	}
}

func TestTableRenameProposal(t *testing.T) {
	head := parse(t, ddl)
	renamed := parse(t, `
		CREATE TABLE accounts (
			id    BIGINT PRIMARY KEY,
			email VARCHAR(255) NOT NULL,
			name  TEXT
		);
		CREATE TABLE orders (
			id      BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES accounts (id)
		);`)

	out := Rematch(head, renamed, nil)
	var tableProp *Proposal
	for i, p := range out.Proposals {
		if p.Kind == "table" {
			tableProp = &out.Proposals[i]
		}
	}
	if tableProp == nil || tableProp.OldName != "users" || tableProp.NewName != "accounts" {
		t.Fatalf("expected users→accounts table proposal, got %+v", out.Proposals)
	}

	usersID := head.TableByName("users").ID
	confirmed := Rematch(head, renamed, []Decision{{OldID: tableProp.OldID, Rename: true}})
	acc := confirmed.Schema.TableByName("accounts")
	if acc == nil || acc.ID != usersID {
		t.Fatal("confirmed table rename must transplant the table ID")
	}
	if acc.ColumnByName("email").ID != head.TableByName("users").ColumnByName("email").ID {
		t.Fatal("table rename must transplant same-named column IDs too")
	}
}

func TestUnrelatedNewColumnGetsNoProposal(t *testing.T) {
	head := parse(t, ddl)
	changed := parse(t, `
		CREATE TABLE users (
			id    BIGINT PRIMARY KEY,
			email VARCHAR(255) NOT NULL,
			name  TEXT,
			created_at TIMESTAMP
		);
		CREATE TABLE orders (
			id      BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users (id)
		);`)
	out := Rematch(head, changed, nil)
	if len(out.Proposals) != 0 {
		t.Fatalf("a plain new column must not trigger rename proposals: %+v", out.Proposals)
	}
}
