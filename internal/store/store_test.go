package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/rohithmone27/mergebase/internal/parser"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedProject(t *testing.T, s *Store) (Project, Branch, *Commit) {
	t.Helper()
	res, err := parser.Parse(`CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(255));`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, err := s.CreateProject("payments")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c := &Commit{ProjectID: p.ID, Message: "initial import", Author: "rohith", Schema: res.Schema, Unsupported: res.Unsupported}
	if err := s.CreateCommit(c); err != nil {
		t.Fatalf("CreateCommit: %v", err)
	}
	b, err := s.CreateBranch(p.ID, "main", c.ID)
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	return p, b, c
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := open(t)
	_, _, c := seedProject(t, s)

	got, err := s.GetCommit(c.ID)
	if err != nil {
		t.Fatalf("GetCommit: %v", err)
	}
	users := got.Schema.TableByName("users")
	if users == nil {
		t.Fatal("schema snapshot lost through storage")
	}
	// Identity must survive persistence — IDs are the merge engine's ground truth.
	orig := c.Schema.TableByName("users")
	if users.ID != orig.ID || users.ColumnByName("email").ID != orig.ColumnByName("email").ID {
		t.Fatal("ObjectIDs changed through a storage round-trip")
	}
	if got.Message != "initial import" || got.Author != "rohith" || got.ParentID != "" {
		t.Fatalf("commit metadata wrong: %+v", got)
	}
}

func TestCommitAndMoveHead(t *testing.T) {
	s := open(t)
	p, b, c1 := seedProject(t, s)

	c2 := &Commit{ProjectID: p.ID, Message: "add column", ParentID: c1.ID, Schema: c1.Schema}
	if err := s.CommitAndMoveHead(b.ID, c1.ID, c2); err != nil {
		t.Fatalf("CommitAndMoveHead: %v", err)
	}
	got, _ := s.GetBranch(b.ID)
	if got.HeadCommitID != c2.ID {
		t.Fatalf("head = %s, want %s", got.HeadCommitID, c2.ID)
	}

	hist, err := s.History(c2.ID, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 2 || hist[0].ID != c2.ID || hist[1].ID != c1.ID {
		t.Fatalf("history wrong: %+v", hist)
	}
}

func TestConcurrentHeadMoveIsRejectedAtomically(t *testing.T) {
	s := open(t)
	p, b, c1 := seedProject(t, s)

	// First writer wins.
	c2 := &Commit{ProjectID: p.ID, Message: "writer A", ParentID: c1.ID, Schema: c1.Schema}
	if err := s.CommitAndMoveHead(b.ID, c1.ID, c2); err != nil {
		t.Fatalf("writer A: %v", err)
	}

	// Second writer still expects the old head — must fail...
	c3 := &Commit{ProjectID: p.ID, Message: "writer B", ParentID: c1.ID, Schema: c1.Schema}
	err := s.CommitAndMoveHead(b.ID, c1.ID, c3)
	if !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatalf("err = %v, want ErrConcurrentUpdate", err)
	}
	// ...and must not have written its commit (the insert rolls back too).
	if _, err := s.GetCommit(c3.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("loser's commit must not persist, got err = %v", err)
	}
	// Winner's state intact.
	got, _ := s.GetBranch(b.ID)
	if got.HeadCommitID != c2.ID {
		t.Fatalf("head = %s, want winner %s", got.HeadCommitID, c2.ID)
	}
}

func TestBranchNamesUniquePerProject(t *testing.T) {
	s := open(t)
	p, _, c := seedProject(t, s)
	if _, err := s.CreateBranch(p.ID, "main", c.ID); err == nil {
		t.Fatal("duplicate branch name in one project must fail")
	}
	p2, err := s.CreateProject("other")
	if err != nil {
		t.Fatal(err)
	}
	c2 := &Commit{ProjectID: p2.ID, Message: "init", Schema: c.Schema}
	if err := s.CreateCommit(c2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBranch(p2.ID, "main", c2.ID); err != nil {
		t.Fatalf("same branch name in another project must be fine: %v", err)
	}
}

func TestNotFound(t *testing.T) {
	s := open(t)
	if _, err := s.GetProject("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProject err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetCommit("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCommit err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetBranch("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBranch err = %v, want ErrNotFound", err)
	}
}
