// Package api is the thin JSON layer between the UI and the engine/store.
// It translates requests into calls and results into JSON; it holds no
// domain logic. Every error response is {error: {code, message, hint}} with
// a hint the user can act on.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"mergebase/internal/diff"
	"mergebase/internal/merge"
	"mergebase/internal/ops"
	"mergebase/internal/parser"
	"mergebase/internal/schema"
	"mergebase/internal/seed"
	"mergebase/internal/store"
)

// maxBodyBytes bounds request bodies; the largest legitimate payload is a
// pasted DDL file, and 1 MiB of DDL is far beyond any real schema.
const maxBodyBytes = 1 << 20

type Server struct {
	store *store.Store
	log   *slog.Logger
}

func New(st *store.Store, log *slog.Logger) *Server {
	return &Server{store: st, log: log}
}

// Register attaches all API routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("GET /api/projects/{id}", s.getProject)
	mux.HandleFunc("POST /api/projects/{id}/branches", s.createBranch)
	mux.HandleFunc("GET /api/branches/{id}/schema", s.branchSchema)
	mux.HandleFunc("GET /api/branches/{id}/commits", s.branchCommits)
	mux.HandleFunc("POST /api/branches/{id}/changes", s.applyChanges)
	mux.HandleFunc("GET /api/diff", s.diff)
	mux.HandleFunc("POST /api/merge/preview", s.mergePreview)
	mux.HandleFunc("POST /api/merge", s.mergeExecute)
	mux.HandleFunc("POST /api/demo/reset", s.demoReset)
}

// ---- handlers ----

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	s.json(w, http.StatusOK, map[string]string{"status": "ok"})
}

type createProjectReq struct {
	Name   string `json:"name"`
	DDL    string `json:"ddl"`
	Author string `json:"author"`
}

type createProjectResp struct {
	Project     store.Project        `json:"project"`
	Branch      store.Branch         `json:"branch"`
	CommitID    string               `json:"commit_id"`
	Unsupported []parser.Unsupported `json:"unsupported"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectReq
	if !s.decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.error(w, http.StatusBadRequest, "missing_name", "The project needs a name.", "Include a non-empty \"name\" field.")
		return
	}

	sch := &schema.Schema{}
	var unsupported []parser.Unsupported
	if req.DDL != "" {
		res, err := parser.Parse(req.DDL)
		if err != nil {
			s.error(w, http.StatusUnprocessableEntity, "invalid_ddl", err.Error(),
				"Fix the SQL and try again — Mergebase understands PostgreSQL CREATE TABLE, CREATE INDEX, and ALTER TABLE ADD COLUMN/CONSTRAINT.")
			return
		}
		sch = res.Schema
		unsupported = res.Unsupported
	}

	project, err := s.store.CreateProject(req.Name)
	if err != nil {
		s.error(w, http.StatusConflict, "project_exists",
			fmt.Sprintf("A project named %q already exists.", req.Name),
			"Pick a different name.")
		return
	}
	message := "Initial schema import"
	if req.DDL == "" {
		message = "Empty project"
	}
	commit := &store.Commit{ProjectID: project.ID, Message: message, Author: req.Author, Schema: sch, Unsupported: unsupported}
	if err := s.store.CreateCommit(commit); err != nil {
		s.internal(w, err)
		return
	}
	branch, err := s.store.CreateBranch(project.ID, "main", commit.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	_ = s.store.AppendEvent(project.ID, branch.ID, "project_created", map[string]any{"name": req.Name, "tables": len(sch.Tables)})

	if unsupported == nil {
		unsupported = []parser.Unsupported{}
	}
	s.json(w, http.StatusCreated, createProjectResp{Project: project, Branch: branch, CommitID: commit.ID, Unsupported: unsupported})
}

func (s *Server) listProjects(w http.ResponseWriter, _ *http.Request) {
	projects, err := s.store.ListProjects()
	if err != nil {
		s.internal(w, err)
		return
	}
	if projects == nil {
		projects = []store.Project{}
	}
	s.json(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.GetProject(r.PathValue("id"))
	if err != nil {
		s.notFoundOrInternal(w, err, "project")
		return
	}
	branches, err := s.store.ListBranches(project.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	if branches == nil {
		branches = []store.Branch{}
	}
	s.json(w, http.StatusOK, map[string]any{"project": project, "branches": branches})
}

type createBranchReq struct {
	Name string `json:"name"`
	// From is the branch ID to branch off (its current head becomes the
	// new branch's starting commit).
	From string `json:"from"`
}

func (s *Server) createBranch(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	var req createBranchReq
	if !s.decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.error(w, http.StatusBadRequest, "missing_name", "The branch needs a name.", "Include a non-empty \"name\" field.")
		return
	}
	src, err := s.store.GetBranch(req.From)
	if err != nil {
		s.error(w, http.StatusNotFound, "branch_not_found",
			"The branch to start from was not found.",
			"Pass an existing branch's id as \"from\".")
		return
	}
	if src.ProjectID != projectID {
		s.error(w, http.StatusBadRequest, "cross_project_branch",
			"The source branch belongs to a different project.",
			"Branch from a branch of the same project.")
		return
	}
	branch, err := s.store.CreateBranch(projectID, req.Name, src.HeadCommitID)
	if err != nil {
		s.error(w, http.StatusConflict, "branch_exists",
			fmt.Sprintf("A branch named %q already exists in this project.", req.Name),
			"Pick a different name.")
		return
	}
	_ = s.store.AppendEvent(projectID, branch.ID, "branch_created", map[string]any{"name": req.Name, "from": src.Name})
	s.json(w, http.StatusCreated, map[string]any{"branch": branch})
}

func (s *Server) branchSchema(w http.ResponseWriter, r *http.Request) {
	branch, err := s.store.GetBranch(r.PathValue("id"))
	if err != nil {
		s.notFoundOrInternal(w, err, "branch")
		return
	}
	commit, err := s.store.GetCommit(branch.HeadCommitID)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.json(w, http.StatusOK, map[string]any{
		"branch":      branch,
		"commit":      commit,
		"schema":      commit.Schema,
		"unsupported": commit.Unsupported,
	})
}

func (s *Server) branchCommits(w http.ResponseWriter, r *http.Request) {
	branch, err := s.store.GetBranch(r.PathValue("id"))
	if err != nil {
		s.notFoundOrInternal(w, err, "branch")
		return
	}
	history, err := s.store.History(branch.HeadCommitID, 100)
	if err != nil {
		s.internal(w, err)
		return
	}
	// History entries carry full snapshots; the list view only needs metadata.
	type entry struct {
		ID        string `json:"id"`
		Message   string `json:"message"`
		Author    string `json:"author"`
		ParentID  string `json:"parent_id,omitempty"`
		Parent2ID string `json:"parent2_id,omitempty"`
		CreatedAt string `json:"created_at"`
		Tables    int    `json:"tables"`
	}
	out := make([]entry, 0, len(history))
	for _, c := range history {
		out = append(out, entry{
			ID: c.ID, Message: c.Message, Author: c.Author,
			ParentID: c.ParentID, Parent2ID: c.Parent2ID,
			CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			Tables:    len(c.Schema.Tables),
		})
	}
	s.json(w, http.StatusOK, map[string]any{"commits": out})
}

type applyChangesReq struct {
	Operations json.RawMessage `json:"operations"`
	Message    string          `json:"message"`
	Author     string          `json:"author"`
}

// applyChanges applies edit operations to the branch head and commits the
// result. The head move is compare-and-swapped: if someone else committed
// meanwhile, the client gets a conflict instead of silently clobbering them.
func (s *Server) applyChanges(w http.ResponseWriter, r *http.Request) {
	branch, err := s.store.GetBranch(r.PathValue("id"))
	if err != nil {
		s.notFoundOrInternal(w, err, "branch")
		return
	}
	var req applyChangesReq
	if !s.decode(w, r, &req) {
		return
	}
	operations, err := ops.UnmarshalOps(req.Operations)
	if err != nil {
		s.error(w, http.StatusBadRequest, "invalid_operations", err.Error(),
			"Operations must be a JSON array of {op, ...} objects.")
		return
	}
	if len(operations) == 0 {
		s.error(w, http.StatusBadRequest, "no_operations", "There are no operations to apply.",
			"Send at least one operation.")
		return
	}

	head, err := s.store.GetCommit(branch.HeadCommitID)
	if err != nil {
		s.internal(w, err)
		return
	}
	next, err := ops.Apply(head.Schema, operations)
	if err != nil {
		s.error(w, http.StatusUnprocessableEntity, "invalid_change", err.Error(),
			"Nothing was committed — fix the operation and retry.")
		return
	}

	message := req.Message
	if message == "" {
		parts := make([]string, 0, len(operations))
		for _, op := range operations {
			parts = append(parts, ops.Describe(head.Schema, op))
		}
		message = strings.Join(parts, "; ")
	}
	commit := &store.Commit{
		ProjectID: branch.ProjectID, Message: message, Author: req.Author,
		ParentID: head.ID, Schema: next, Unsupported: head.Unsupported,
	}
	if err := s.store.CommitAndMoveHead(branch.ID, head.ID, commit); err != nil {
		if errors.Is(err, store.ErrConcurrentUpdate) {
			s.error(w, http.StatusConflict, "branch_moved",
				"Someone else committed to this branch while you were editing.",
				"Reload the branch to see the latest schema, then re-apply your change.")
			return
		}
		s.internal(w, err)
		return
	}
	_ = s.store.AppendEvent(branch.ProjectID, branch.ID, "changes_applied",
		map[string]any{"operations": len(operations), "commit": commit.ID})
	s.json(w, http.StatusCreated, map[string]any{"commit_id": commit.ID, "message": message, "schema": next})
}

// diff compares two refs. A ref is a branch ID (its head is used) or a
// commit ID — so "what changed on this branch" and "what changed between
// these two commits" are the same endpoint.
func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	fromRef, toRef := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if fromRef == "" || toRef == "" {
		s.error(w, http.StatusBadRequest, "missing_refs",
			"Both \"from\" and \"to\" refs are required.",
			"Pass branch IDs or commit IDs as ?from=…&to=…")
		return
	}
	from, fromName, err := s.resolveRef(fromRef)
	if err != nil {
		s.notFoundOrInternal(w, err, "from_ref")
		return
	}
	to, toName, err := s.resolveRef(toRef)
	if err != nil {
		s.notFoundOrInternal(w, err, "to_ref")
		return
	}
	d := diff.Compute(from.Schema, to.Schema)
	s.json(w, http.StatusOK, map[string]any{
		"from": map[string]string{"ref": fromRef, "name": fromName, "commit_id": from.ID},
		"to":   map[string]string{"ref": toRef, "name": toName, "commit_id": to.ID},
		"diff": d,
	})
}

// resolveRef turns a branch ID (preferred) or commit ID into a commit,
// with a display name for the UI.
func (s *Server) resolveRef(ref string) (*store.Commit, string, error) {
	if branch, err := s.store.GetBranch(ref); err == nil {
		c, err := s.store.GetCommit(branch.HeadCommitID)
		return c, branch.Name, err
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, "", err
	}
	c, err := s.store.GetCommit(ref)
	if err != nil {
		return nil, "", err
	}
	return c, "commit " + c.ID[:8], nil
}

type mergeReq struct {
	// Source and Target are branch IDs: source (theirs) merges into target (ours).
	Source      string             `json:"source"`
	Target      string             `json:"target"`
	Resolutions []merge.Resolution `json:"resolutions"`
	Author      string             `json:"author"`
}

// mergeInput loads both branches, computes the merge-base, and runs the
// three-way merge. Shared by preview and execute so what the user saw is
// exactly what commits.
func (s *Server) mergeInput(w http.ResponseWriter, r *http.Request) (*mergeReq, *merge.Result, store.Branch, *store.Commit, bool) {
	var req mergeReq
	if !s.decode(w, r, &req) {
		return nil, nil, store.Branch{}, nil, false
	}
	source, err := s.store.GetBranch(req.Source)
	if err != nil {
		s.notFoundOrInternal(w, err, "source_branch")
		return nil, nil, store.Branch{}, nil, false
	}
	target, err := s.store.GetBranch(req.Target)
	if err != nil {
		s.notFoundOrInternal(w, err, "target_branch")
		return nil, nil, store.Branch{}, nil, false
	}
	if source.ProjectID != target.ProjectID {
		s.error(w, http.StatusBadRequest, "cross_project_merge",
			"The two branches belong to different projects.", "Merge branches of the same project.")
		return nil, nil, store.Branch{}, nil, false
	}
	if source.ID == target.ID {
		s.error(w, http.StatusBadRequest, "self_merge",
			"A branch cannot merge into itself.", "Pick a different target branch.")
		return nil, nil, store.Branch{}, nil, false
	}

	baseID, err := s.store.MergeBase(target.HeadCommitID, source.HeadCommitID)
	if err != nil {
		s.internal(w, err)
		return nil, nil, store.Branch{}, nil, false
	}
	base, err := s.store.GetCommit(baseID)
	if err != nil {
		s.internal(w, err)
		return nil, nil, store.Branch{}, nil, false
	}
	ours, err := s.store.GetCommit(target.HeadCommitID)
	if err != nil {
		s.internal(w, err)
		return nil, nil, store.Branch{}, nil, false
	}
	theirs, err := s.store.GetCommit(source.HeadCommitID)
	if err != nil {
		s.internal(w, err)
		return nil, nil, store.Branch{}, nil, false
	}

	result, err := merge.Merge(merge.Input{
		Base: base.Schema, Ours: ours.Schema, Theirs: theirs.Schema,
		OursName: target.Name, TheirsName: source.Name,
		Resolutions: req.Resolutions,
	})
	if err != nil {
		s.error(w, http.StatusUnprocessableEntity, "invalid_resolution", err.Error(),
			"Check each resolution's conflict_id, choice, and custom value.")
		return nil, nil, store.Branch{}, nil, false
	}

	return &req, result, target, theirs, true
}

func (s *Server) mergePreview(w http.ResponseWriter, r *http.Request) {
	_, result, target, theirs, ok := s.mergeInput(w, r)
	if !ok {
		return
	}
	s.json(w, http.StatusOK, map[string]any{
		"clean":     len(result.Conflicts) == 0 && len(result.Problems) == 0,
		"conflicts": result.Conflicts,
		"problems":  result.Problems,
		"changes":   result.Changes,
		"target":    map[string]string{"id": target.ID, "name": target.Name},
		"source_head": theirs.ID,
	})
}

func (s *Server) mergeExecute(w http.ResponseWriter, r *http.Request) {
	req, result, target, theirs, ok := s.mergeInput(w, r)
	if !ok {
		return
	}
	if len(result.Conflicts) > 0 {
		s.json(w, http.StatusConflict, map[string]any{
			"error": errBody{Code: "unresolved_conflicts",
				Message: fmt.Sprintf("%d conflict(s) still need a resolution.", len(result.Conflicts)),
				Hint:    "Resolve each conflict (ours / theirs / custom) and try again."},
			"conflicts": result.Conflicts,
		})
		return
	}
	if len(result.Problems) > 0 {
		s.json(w, http.StatusConflict, map[string]any{
			"error": errBody{Code: "invalid_merged_schema",
				Message: "The merged schema is not coherent — nothing was committed.",
				Hint:    "Each branch is valid alone, but the combination is broken. Fix one side (or resolve differently) and retry."},
			"problems": result.Problems,
		})
		return
	}

	sourceBranch, _ := s.store.GetBranch(req.Source)
	commit := &store.Commit{
		ProjectID: target.ProjectID,
		Message:   fmt.Sprintf("Merge %s into %s", sourceBranch.Name, target.Name),
		Author:    req.Author,
		ParentID:  target.HeadCommitID,
		Parent2ID: theirs.ID,
		Schema:    result.Schema,
	}
	if err := s.store.CommitAndMoveHead(target.ID, target.HeadCommitID, commit); err != nil {
		if errors.Is(err, store.ErrConcurrentUpdate) {
			s.error(w, http.StatusConflict, "branch_moved",
				"The target branch moved while you were resolving conflicts.",
				"Re-open the merge preview against the new head and try again.")
			return
		}
		s.internal(w, err)
		return
	}
	_ = s.store.AppendEvent(target.ProjectID, target.ID, "merge",
		map[string]any{"source": sourceBranch.Name, "target": target.Name,
			"commit": commit.ID, "resolved_conflicts": len(req.Resolutions), "auto_changes": len(result.Changes)})
	s.json(w, http.StatusCreated, map[string]any{
		"commit_id": commit.ID,
		"message":   commit.Message,
		"changes":   result.Changes,
	})
}

func (s *Server) demoReset(w http.ResponseWriter, _ *http.Request) {
	if err := s.store.ResetAll(); err != nil {
		s.internal(w, err)
		return
	}
	if err := seed.Ensure(s.store); err != nil {
		s.internal(w, err)
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "demo restored"})
}

// ---- plumbing ----

func (s *Server) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		s.error(w, http.StatusBadRequest, "invalid_json",
			"The request body is not valid JSON for this endpoint: "+err.Error(),
			"Check the request payload against the API docs in the README.")
		return false
	}
	return true
}

func (s *Server) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("encoding response", "err", err)
	}
}

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (s *Server) error(w http.ResponseWriter, status int, code, message, hint string) {
	s.json(w, status, map[string]errBody{"error": {Code: code, Message: message, Hint: hint}})
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.log.Error("internal error", "err", err)
	s.error(w, http.StatusInternalServerError, "internal",
		"Something went wrong on the server.", "Try again; if it persists, check the server logs.")
}

func (s *Server) notFoundOrInternal(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, store.ErrNotFound) {
		s.error(w, http.StatusNotFound, what+"_not_found",
			"The requested "+what+" does not exist.",
			"It may have been removed by a demo reset — go back to the project list.")
		return
	}
	s.internal(w, err)
}
