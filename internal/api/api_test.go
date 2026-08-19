package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"mergebase/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	mux := http.NewServeMux()
	New(st, slog.New(slog.DiscardHandler)).Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func doJSON(t *testing.T, method, url string, body any, wantStatus int) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d (body: %v)", method, url, resp.StatusCode, wantStatus, out)
	}
	return out
}

func TestProjectLifecycle(t *testing.T) {
	ts := newTestServer(t)

	// Create a project from DDL.
	created := doJSON(t, "POST", ts.URL+"/api/projects", map[string]string{
		"name": "shop",
		"ddl":  "CREATE TABLE t (id INT PRIMARY KEY); CREATE SEQUENCE s;",
	}, http.StatusCreated)
	projectID := created["project"].(map[string]any)["id"].(string)
	branchID := created["branch"].(map[string]any)["id"].(string)
	if created["branch"].(map[string]any)["name"] != "main" {
		t.Fatalf("default branch = %v, want main", created["branch"])
	}
	// The fidelity report reaches the client.
	if n := len(created["unsupported"].([]any)); n != 1 {
		t.Fatalf("unsupported entries = %d, want 1 (the sequence)", n)
	}

	// It shows up in the list and detail views.
	list := doJSON(t, "GET", ts.URL+"/api/projects", nil, http.StatusOK)
	if n := len(list["projects"].([]any)); n != 1 {
		t.Fatalf("projects = %d, want 1", n)
	}
	detail := doJSON(t, "GET", ts.URL+"/api/projects/"+projectID, nil, http.StatusOK)
	if n := len(detail["branches"].([]any)); n != 1 {
		t.Fatalf("branches = %d, want 1", n)
	}

	// The head schema round-trips.
	sch := doJSON(t, "GET", ts.URL+"/api/branches/"+branchID+"/schema", nil, http.StatusOK)
	tables := sch["schema"].(map[string]any)["tables"].([]any)
	if len(tables) != 1 || tables[0].(map[string]any)["name"] != "t" {
		t.Fatalf("schema tables = %v", tables)
	}

	// Branching from main.
	branch := doJSON(t, "POST", ts.URL+"/api/projects/"+projectID+"/branches",
		map[string]string{"name": "feature/x", "from": branchID}, http.StatusCreated)
	newBranch := branch["branch"].(map[string]any)
	if newBranch["head_commit_id"] != created["commit_id"] {
		t.Fatal("new branch must start at the source branch's head commit")
	}

	// History exists on both branches.
	commits := doJSON(t, "GET", ts.URL+"/api/branches/"+newBranch["id"].(string)+"/commits", nil, http.StatusOK)
	if n := len(commits["commits"].([]any)); n != 1 {
		t.Fatalf("commits = %d, want 1", n)
	}
}

func TestErrorContract(t *testing.T) {
	ts := newTestServer(t)

	cases := []struct {
		name       string
		method     string
		path       string
		body       any
		wantStatus int
		wantCode   string
	}{
		{"bad ddl", "POST", "/api/projects", map[string]string{"name": "x", "ddl": "CREATE TABLE ("}, 422, "invalid_ddl"},
		{"missing name", "POST", "/api/projects", map[string]string{"ddl": ""}, 400, "missing_name"},
		{"unknown project", "GET", "/api/projects/nope", nil, 404, "project_not_found"},
		{"unknown branch", "GET", "/api/branches/nope/schema", nil, 404, "branch_not_found"},
		{"bad json", "POST", "/api/projects", "not-an-object", 400, "invalid_json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := doJSON(t, c.method, ts.URL+c.path, c.body, c.wantStatus)
			errObj, ok := out["error"].(map[string]any)
			if !ok {
				t.Fatalf("no error envelope in %v", out)
			}
			if errObj["code"] != c.wantCode {
				t.Fatalf("code = %v, want %s", errObj["code"], c.wantCode)
			}
			if errObj["message"] == "" || errObj["hint"] == "" {
				t.Fatalf("error must carry message and hint: %v", errObj)
			}
		})
	}

	// Duplicate names are conflicts.
	doJSON(t, "POST", ts.URL+"/api/projects", map[string]string{"name": "dup"}, 201)
	out := doJSON(t, "POST", ts.URL+"/api/projects", map[string]string{"name": "dup"}, 409)
	if out["error"].(map[string]any)["code"] != "project_exists" {
		t.Fatalf("want project_exists, got %v", out)
	}
}

func TestDemoResetRestoresSeed(t *testing.T) {
	ts := newTestServer(t)

	// Litter the workspace, then reset.
	doJSON(t, "POST", ts.URL+"/api/projects", map[string]string{"name": "junk"}, 201)
	doJSON(t, "POST", ts.URL+"/api/demo/reset", nil, http.StatusOK)

	list := doJSON(t, "GET", ts.URL+"/api/projects", nil, http.StatusOK)
	projects := list["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("after reset projects = %d, want exactly the demo", len(projects))
	}
	p := projects[0].(map[string]any)
	if p["name"] != "Payments Platform" {
		t.Fatalf("after reset project = %v, want Payments Platform", p["name"])
	}

	detail := doJSON(t, "GET", ts.URL+fmt.Sprintf("/api/projects/%s", p["id"]), nil, http.StatusOK)
	branches := detail["branches"].([]any)
	if len(branches) != 2 {
		t.Fatalf("demo branches = %d, want main + feature/billing", len(branches))
	}
}

func TestApplyChanges(t *testing.T) {
	ts := newTestServer(t)
	created := doJSON(t, "POST", ts.URL+"/api/projects", map[string]string{
		"name": "shop",
		"ddl":  "CREATE TABLE t (id INT PRIMARY KEY, note TEXT);",
	}, 201)
	branchID := created["branch"].(map[string]any)["id"].(string)

	sch := doJSON(t, "GET", ts.URL+"/api/branches/"+branchID+"/schema", nil, 200)
	table := sch["schema"].(map[string]any)["tables"].([]any)[0].(map[string]any)
	tableID := table["id"].(string)
	noteID := table["columns"].([]any)[1].(map[string]any)["id"].(string)

	// Rename note → comment via the API; auto-generated commit message.
	out := doJSON(t, "POST", ts.URL+"/api/branches/"+branchID+"/changes", map[string]any{
		"operations": []map[string]any{
			{"op": "rename_column", "table_id": tableID, "column_id": noteID, "name": "comment"},
		},
	}, 201)
	if out["message"] != "rename t.note → comment" {
		t.Fatalf("auto message = %v", out["message"])
	}

	// The head moved and the rename preserved the column's ID.
	sch2 := doJSON(t, "GET", ts.URL+"/api/branches/"+branchID+"/schema", nil, 200)
	col := sch2["schema"].(map[string]any)["tables"].([]any)[0].(map[string]any)["columns"].([]any)[1].(map[string]any)
	if col["name"] != "comment" || col["id"] != noteID {
		t.Fatalf("rename lost identity: %v", col)
	}
	commits := doJSON(t, "GET", ts.URL+"/api/branches/"+branchID+"/commits", nil, 200)
	if n := len(commits["commits"].([]any)); n != 2 {
		t.Fatalf("commits = %d, want 2", n)
	}

	// A structurally invalid change commits nothing.
	out = doJSON(t, "POST", ts.URL+"/api/branches/"+branchID+"/changes", map[string]any{
		"operations": []map[string]any{
			{"op": "rename_column", "table_id": tableID, "column_id": "missing", "name": "x"},
		},
	}, 422)
	if out["error"].(map[string]any)["code"] != "invalid_change" {
		t.Fatalf("want invalid_change, got %v", out)
	}
	commits = doJSON(t, "GET", ts.URL+"/api/branches/"+branchID+"/commits", nil, 200)
	if n := len(commits["commits"].([]any)); n != 2 {
		t.Fatalf("failed change must not commit; commits = %d", n)
	}
}

func TestDiffEndpointOnSeededWorkspace(t *testing.T) {
	ts := newTestServer(t)
	doJSON(t, "POST", ts.URL+"/api/demo/reset", nil, 200)

	list := doJSON(t, "GET", ts.URL+"/api/projects", nil, 200)
	projectID := list["projects"].([]any)[0].(map[string]any)["id"].(string)
	detail := doJSON(t, "GET", ts.URL+"/api/projects/"+projectID, nil, 200)
	var mainID, billingID string
	for _, b := range detail["branches"].([]any) {
		br := b.(map[string]any)
		switch br["name"] {
		case "main":
			mainID = br["id"].(string)
		case "feature/billing":
			billingID = br["id"].(string)
		}
	}

	out := doJSON(t, "GET", ts.URL+"/api/diff?from="+mainID+"&to="+billingID, nil, 200)
	if out["from"].(map[string]any)["name"] != "main" || out["to"].(map[string]any)["name"] != "feature/billing" {
		t.Fatalf("ref names wrong: %v", out)
	}
	changes := out["diff"].(map[string]any)["changes"].([]any)
	texts := make([]string, 0, len(changes))
	for _, c := range changes {
		texts = append(texts, c.(map[string]any)["text"].(string))
	}
	joined := strings.Join(texts, " | ")
	// The seeded divergence, read main → billing: refunds disappears,
	// invoices appears, email retypes varchar(500)→text, name renames.
	for _, want := range []string{
		"dropped table refunds",
		"added table invoices",
		"changed type of users.email: varchar(500) → text",
		"renamed users.name → full_name",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("diff missing %q; got: %s", want, joined)
		}
	}
	// The rename must NOT appear as drop+add — that would mean identity loss.
	if strings.Contains(joined, "dropped column users.name") || strings.Contains(joined, "added column users.full_name") {
		t.Fatalf("rename degraded to drop+add: %s", joined)
	}
}

// The reviewer journey as a test: seeded workspace → preview the merge of
// feature/billing into main → hit the prepared conflict → resolve → merge
// commits with two parents → history and schema reflect it.
func TestMergeJourneyOnSeededWorkspace(t *testing.T) {
	ts := newTestServer(t)
	doJSON(t, "POST", ts.URL+"/api/demo/reset", nil, 200)

	list := doJSON(t, "GET", ts.URL+"/api/projects", nil, 200)
	projectID := list["projects"].([]any)[0].(map[string]any)["id"].(string)
	detail := doJSON(t, "GET", ts.URL+"/api/projects/"+projectID, nil, 200)
	var mainID, billingID string
	for _, b := range detail["branches"].([]any) {
		br := b.(map[string]any)
		if br["name"] == "main" {
			mainID = br["id"].(string)
		} else {
			billingID = br["id"].(string)
		}
	}

	// Preview: one prepared conflict (email type), rename merges cleanly.
	preview := doJSON(t, "POST", ts.URL+"/api/merge/preview",
		map[string]any{"source": billingID, "target": mainID}, 200)
	if preview["clean"] != false {
		t.Fatalf("preview must not be clean: %v", preview)
	}
	conflicts := preview["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %d, want exactly the prepared one: %v", len(conflicts), conflicts)
	}
	c := conflicts[0].(map[string]any)
	if c["class"] != "retype_retype" || c["ours"] != "varchar(500)" || c["theirs"] != "text" {
		t.Fatalf("prepared conflict wrong: %v", c)
	}

	// Executing with the conflict unresolved must refuse.
	refused := doJSON(t, "POST", ts.URL+"/api/merge",
		map[string]any{"source": billingID, "target": mainID}, 409)
	if refused["error"].(map[string]any)["code"] != "unresolved_conflicts" {
		t.Fatalf("want unresolved_conflicts, got %v", refused)
	}

	// Resolve as theirs (text) and merge.
	merged := doJSON(t, "POST", ts.URL+"/api/merge", map[string]any{
		"source": billingID, "target": mainID, "author": "rohith",
		"resolutions": []map[string]any{{"conflict_id": c["id"], "choice": "theirs"}},
	}, 201)
	if merged["commit_id"] == "" {
		t.Fatal("merge must return the merge commit")
	}

	// The merged schema on main: invoices AND refunds, email is text,
	// name renamed to full_name — identity preserved end to end.
	sch := doJSON(t, "GET", ts.URL+"/api/branches/"+mainID+"/schema", nil, 200)
	names := map[string]bool{}
	var emailType string
	var fullName bool
	for _, tbl := range sch["schema"].(map[string]any)["tables"].([]any) {
		tb := tbl.(map[string]any)
		names[tb["name"].(string)] = true
		if tb["name"] == "users" {
			for _, col := range tb["columns"].([]any) {
				cc := col.(map[string]any)
				if cc["name"] == "email" {
					emailType = cc["type"].(map[string]any)["base"].(string)
				}
				if cc["name"] == "full_name" {
					fullName = true
				}
			}
		}
	}
	for _, want := range []string{"users", "orders", "payments", "refunds", "invoices"} {
		if !names[want] {
			t.Fatalf("merged schema missing table %q: %v", want, names)
		}
	}
	if emailType != "text" || !fullName {
		t.Fatalf("merged users wrong: email=%s full_name=%v", emailType, fullName)
	}

	// History shows a merge commit with two parents.
	commits := doJSON(t, "GET", ts.URL+"/api/branches/"+mainID+"/commits", nil, 200)
	top := commits["commits"].([]any)[0].(map[string]any)
	if top["parent2_id"] == nil || top["parent2_id"] == "" {
		t.Fatalf("head must be a merge commit with two parents: %v", top)
	}
	if !strings.Contains(top["message"].(string), "Merge feature/billing into main") {
		t.Fatalf("merge message = %v", top["message"])
	}
}
