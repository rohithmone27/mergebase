package seed

import (
	"path/filepath"
	"testing"

	"github.com/rohithmone27/mergebase/internal/store"
)

func TestEnsureSeedsOnceAndPreservesIdentity(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := Ensure(s); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Idempotent: a second call must not duplicate the workspace.
	if err := Ensure(s); err != nil {
		t.Fatalf("Ensure (second): %v", err)
	}
	projects, _ := s.ListProjects()
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(projects))
	}

	branches, _ := s.ListBranches(projects[0].ID)
	if len(branches) != 2 {
		t.Fatalf("branches = %d, want 2", len(branches))
	}
	var main, billing store.Branch
	for _, b := range branches {
		switch b.Name {
		case "main":
			main = b
		case "feature/billing":
			billing = b
		}
	}
	if main.ID == "" || billing.ID == "" {
		t.Fatalf("expected main and feature/billing, got %+v", branches)
	}

	mainHead, err := s.GetCommit(main.HeadCommitID)
	if err != nil {
		t.Fatal(err)
	}
	billingHead, err := s.GetCommit(billing.HeadCommitID)
	if err != nil {
		t.Fatal(err)
	}

	// The two branches must agree on the identity of shared objects — this
	// is what makes the seeded conflict a real C2 (same column, divergent
	// types) rather than an add/add artifact.
	mainEmail := mainHead.Schema.TableByName("users").ColumnByName("email")
	billingEmail := billingHead.Schema.TableByName("users").ColumnByName("email")
	if mainEmail.ID != billingEmail.ID {
		t.Fatal("users.email must have the same ObjectID on both branches")
	}
	if mainEmail.Type.String() != "varchar(500)" || billingEmail.Type.String() != "text" {
		t.Fatalf("prepared conflict types = %s vs %s, want varchar(500) vs text",
			mainEmail.Type, billingEmail.Type)
	}

	// The rename on billing keeps identity with main's original column.
	mainName := mainHead.Schema.TableByName("users").ColumnByName("name")
	billingFullName := billingHead.Schema.TableByName("users").ColumnByName("full_name")
	if mainName == nil || billingFullName == nil || mainName.ID != billingFullName.ID {
		t.Fatal("rename name→full_name must preserve the ObjectID across branches")
	}

	// Diverged additions exist on exactly one side each.
	if mainHead.Schema.TableByName("refunds") == nil || mainHead.Schema.TableByName("invoices") != nil {
		t.Fatal("main must have refunds and not invoices")
	}
	if billingHead.Schema.TableByName("invoices") == nil || billingHead.Schema.TableByName("refunds") != nil {
		t.Fatal("billing must have invoices and not refunds")
	}

	// Both branches share the initial commit as ancestor.
	mainHist, _ := s.History(main.HeadCommitID, 10)
	billingHist, _ := s.History(billing.HeadCommitID, 10)
	if mainHist[len(mainHist)-1].ID != billingHist[len(billingHist)-1].ID {
		t.Fatal("both branches must share the initial commit")
	}
	if len(mainHist) != 3 || len(billingHist) != 3 {
		t.Fatalf("history lengths = %d/%d, want 3/3", len(mainHist), len(billingHist))
	}
}
