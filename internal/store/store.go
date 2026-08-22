// Package store persists projects, commits, and branches in a single SQLite
// file. Commits are immutable whole-schema snapshots forming a DAG (a merge
// commit has two parents); a branch is a named pointer to its head commit.
//
// Concurrency rule: a branch head only ever moves by compare-and-swap against
// the expected previous head, inside the same transaction as the commit
// insert — two writers racing on one branch cannot silently discard each
// other's work; the loser gets ErrConcurrentUpdate.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rohithmone27/mergebase/internal/parser"
	"github.com/rohithmone27/mergebase/internal/schema"
)

// ErrConcurrentUpdate means the branch head moved between reading it and
// writing: the caller should reload and retry or surface a conflict.
var ErrConcurrentUpdate = errors.New("branch head changed concurrently")

// ErrNotFound means the requested row does not exist.
var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Commit struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Message   string    `json:"message"`
	Author    string    `json:"author"`
	ParentID  string    `json:"parent_id,omitempty"`
	Parent2ID string    `json:"parent2_id,omitempty"` // set only on merge commits
	CreatedAt time.Time `json:"created_at"`

	Schema      *schema.Schema       `json:"-"`
	Unsupported []parser.Unsupported `json:"unsupported,omitempty"`
}

type Branch struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"`
	HeadCommitID string    `json:"head_commit_id"`
	CreatedAt    time.Time `json:"created_at"`
}

const ddl = `
CREATE TABLE IF NOT EXISTS projects (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS commits (
	id               TEXT PRIMARY KEY,
	project_id       TEXT NOT NULL REFERENCES projects(id),
	message          TEXT NOT NULL,
	author           TEXT NOT NULL DEFAULT '',
	parent_id        TEXT REFERENCES commits(id),
	parent2_id       TEXT REFERENCES commits(id),
	schema_json      TEXT NOT NULL,
	unsupported_json TEXT NOT NULL DEFAULT '[]',
	created_at       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS branches (
	id             TEXT PRIMARY KEY,
	project_id     TEXT NOT NULL REFERENCES projects(id),
	name           TEXT NOT NULL,
	head_commit_id TEXT NOT NULL REFERENCES commits(id),
	created_at     TEXT NOT NULL,
	UNIQUE (project_id, name)
);
CREATE TABLE IF NOT EXISTS events (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id  TEXT NOT NULL,
	branch_id   TEXT,
	kind        TEXT NOT NULL,
	detail_json TEXT NOT NULL DEFAULT '{}',
	created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_commits_project ON commits(project_id);
CREATE INDEX IF NOT EXISTS idx_events_project  ON events(project_id, id);
`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	// SQLite allows one writer; a single connection avoids SQLITE_BUSY
	// surprises entirely at our scale.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("store: reading random bytes: %v", err))
	}
	return hex.EncodeToString(b[:])
}

func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

const timeFormat = time.RFC3339Nano

// ---- projects ----

func (s *Store) CreateProject(name string) (Project, error) {
	p := Project{ID: newID(), Name: name, CreatedAt: now()}
	_, err := s.db.Exec(`INSERT INTO projects (id, name, created_at) VALUES (?, ?, ?)`,
		p.ID, p.Name, p.CreatedAt.Format(timeFormat))
	if err != nil {
		return Project{}, fmt.Errorf("creating project %q: %w", name, err)
	}
	return p, nil
}

func (s *Store) GetProject(id string) (Project, error) {
	return scanProject(s.db.QueryRow(`SELECT id, name, created_at FROM projects WHERE id = ?`, id))
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- commits ----

// CreateCommit inserts an immutable snapshot. It does not move any branch;
// use CommitAndMoveHead for the normal "commit onto a branch" path.
func (s *Store) CreateCommit(c *Commit) error {
	return s.insertCommit(s.db, c)
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *Store) insertCommit(ex execer, c *Commit) error {
	if c.ID == "" {
		c.ID = newID()
	}
	c.CreatedAt = now()
	schemaJSON, err := json.Marshal(c.Schema)
	if err != nil {
		return fmt.Errorf("marshaling schema: %w", err)
	}
	unsupported := c.Unsupported
	if unsupported == nil {
		unsupported = []parser.Unsupported{}
	}
	unsupportedJSON, err := json.Marshal(unsupported)
	if err != nil {
		return fmt.Errorf("marshaling unsupported list: %w", err)
	}
	_, err = ex.Exec(`INSERT INTO commits
		(id, project_id, message, author, parent_id, parent2_id, schema_json, unsupported_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ProjectID, c.Message, c.Author,
		nullable(c.ParentID), nullable(c.Parent2ID),
		string(schemaJSON), string(unsupportedJSON), c.CreatedAt.Format(timeFormat))
	if err != nil {
		return fmt.Errorf("inserting commit: %w", err)
	}
	return nil
}

func (s *Store) GetCommit(id string) (*Commit, error) {
	row := s.db.QueryRow(`SELECT id, project_id, message, author, parent_id, parent2_id,
		schema_json, unsupported_json, created_at FROM commits WHERE id = ?`, id)
	return scanCommit(row)
}

// History walks first parents from the given commit, newest first, up to
// limit entries. Merge commits appear once, on the first-parent line.
func (s *Store) History(fromCommitID string, limit int) ([]*Commit, error) {
	var out []*Commit
	id := fromCommitID
	for id != "" && len(out) < limit {
		c, err := s.GetCommit(id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
		id = c.ParentID
	}
	return out, nil
}

// ---- branches ----

func (s *Store) CreateBranch(projectID, name, headCommitID string) (Branch, error) {
	b := Branch{ID: newID(), ProjectID: projectID, Name: name, HeadCommitID: headCommitID, CreatedAt: now()}
	_, err := s.db.Exec(`INSERT INTO branches (id, project_id, name, head_commit_id, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		b.ID, b.ProjectID, b.Name, b.HeadCommitID, b.CreatedAt.Format(timeFormat))
	if err != nil {
		return Branch{}, fmt.Errorf("creating branch %q: %w", name, err)
	}
	return b, nil
}

func (s *Store) GetBranch(id string) (Branch, error) {
	return scanBranch(s.db.QueryRow(`SELECT id, project_id, name, head_commit_id, created_at
		FROM branches WHERE id = ?`, id))
}

func (s *Store) ListBranches(projectID string) ([]Branch, error) {
	rows, err := s.db.Query(`SELECT id, project_id, name, head_commit_id, created_at
		FROM branches WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Branch
	for rows.Next() {
		b, err := scanBranch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CommitAndMoveHead atomically inserts a commit and moves the branch head
// from expectedHead to it. Returns ErrConcurrentUpdate if the head is no
// longer expectedHead — nothing is written in that case.
func (s *Store) CommitAndMoveHead(branchID, expectedHead string, c *Commit) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.insertCommit(tx, c); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE branches SET head_commit_id = ? WHERE id = ? AND head_commit_id = ?`,
		c.ID, branchID, expectedHead)
	if err != nil {
		return fmt.Errorf("moving branch head: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConcurrentUpdate
	}
	return tx.Commit()
}

// ProjectGraph returns every commit in a project, newest first, with both
// parents — enough for the UI to draw the actual commit DAG. Schemas are
// omitted: the graph view needs shape and metadata, not snapshots.
func (s *Store) ProjectGraph(projectID string, limit int) ([]*Commit, error) {
	rows, err := s.db.Query(`SELECT id, project_id, message, author, parent_id, parent2_id, created_at
		FROM commits WHERE project_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Commit
	for rows.Next() {
		var c Commit
		var parent, parent2 sql.NullString
		var created string
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Message, &c.Author, &parent, &parent2, &created); err != nil {
			return nil, err
		}
		c.ParentID, c.Parent2ID = parent.String, parent2.String
		c.CreatedAt, _ = time.Parse(timeFormat, created)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// MergeBase returns the common ancestor for a three-way merge of the two
// commits. With merge commits in the DAG, criss-cross histories can have
// more than one lowest common ancestor; the selection rule is deterministic
// and documented: breadth-first from b (first parent before second), the
// first commit reached that is also an ancestor-or-self of a wins — so the
// same merge always sees the same base.
func (s *Store) MergeBase(aID, bID string) (string, error) {
	ancestors := map[string]bool{}
	if err := s.walk(aID, func(id string) bool { ancestors[id] = true; return true }); err != nil {
		return "", err
	}
	var found string
	err := s.walk(bID, func(id string) bool {
		if ancestors[id] {
			found = id
			return false
		}
		return true
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("commits %s and %s share no ancestor", aID, bID)
	}
	return found, nil
}

// walk visits commits breadth-first from id (self included, first parent
// before second) until visit returns false.
func (s *Store) walk(id string, visit func(string) bool) error {
	queue := []string{id}
	seen := map[string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == "" || seen[cur] {
			continue
		}
		seen[cur] = true
		if !visit(cur) {
			return nil
		}
		var p1, p2 sql.NullString
		if err := s.db.QueryRow(`SELECT parent_id, parent2_id FROM commits WHERE id = ?`, cur).Scan(&p1, &p2); err != nil {
			return wrapNotFound(err)
		}
		queue = append(queue, p1.String, p2.String)
	}
	return nil
}

// ResetAll deletes every row — used only by the demo-reset action, which
// restores the seeded workspace afterwards.
func (s *Store) ResetAll() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"events", "branches", "commits", "projects"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("resetting %s: %w", table, err)
		}
	}
	return tx.Commit()
}

// ---- events (audit log) ----

func (s *Store) AppendEvent(projectID, branchID, kind string, detail any) error {
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshaling event detail: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO events (project_id, branch_id, kind, detail_json, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		projectID, nullable(branchID), kind, string(detailJSON), now().Format(timeFormat))
	return err
}

// ---- scanning helpers ----

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(r rowScanner) (Project, error) {
	var p Project
	var created string
	if err := r.Scan(&p.ID, &p.Name, &created); err != nil {
		return Project{}, wrapNotFound(err)
	}
	p.CreatedAt, _ = time.Parse(timeFormat, created)
	return p, nil
}

func scanBranch(r rowScanner) (Branch, error) {
	var b Branch
	var created string
	if err := r.Scan(&b.ID, &b.ProjectID, &b.Name, &b.HeadCommitID, &created); err != nil {
		return Branch{}, wrapNotFound(err)
	}
	b.CreatedAt, _ = time.Parse(timeFormat, created)
	return b, nil
}

func scanCommit(r rowScanner) (*Commit, error) {
	var c Commit
	var parent, parent2 sql.NullString
	var schemaJSON, unsupportedJSON, created string
	if err := r.Scan(&c.ID, &c.ProjectID, &c.Message, &c.Author, &parent, &parent2,
		&schemaJSON, &unsupportedJSON, &created); err != nil {
		return nil, wrapNotFound(err)
	}
	c.ParentID, c.Parent2ID = parent.String, parent2.String
	c.CreatedAt, _ = time.Parse(timeFormat, created)
	c.Schema = &schema.Schema{}
	if err := json.Unmarshal([]byte(schemaJSON), c.Schema); err != nil {
		return nil, fmt.Errorf("unmarshaling schema of commit %s: %w", c.ID, err)
	}
	if err := json.Unmarshal([]byte(unsupportedJSON), &c.Unsupported); err != nil {
		return nil, fmt.Errorf("unmarshaling unsupported list of commit %s: %w", c.ID, err)
	}
	return &c, nil
}

func wrapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// nullable maps "" to NULL so FK references on parent columns stay honest.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
