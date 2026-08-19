package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
