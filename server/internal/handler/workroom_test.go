package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestWorkroomToResponse_StableIDs pins VC-17: every Workroom field that
// is present must map to a stable string ID, and optional bindings must be
// omitted (empty) rather than fabricating a value.
func TestWorkroomToResponse_StableIDs(t *testing.T) {
	wr := db.Workroom{
		ID:          uuidMustParse("6b9b0f8e-0000-0000-0000-000000000001"),
		Name:        "产品评审",
		CreatedBy:   uuidMustParse("6b9b0f8e-0000-0000-0000-000000000002"),
		IssueID:     uuidMustParse("6b9b0f8e-0000-0000-0000-000000000003"),
		WorkOrderID: pgtype.Text{String: "WO-1", Valid: true},
		// ProjectID left invalid on purpose.
	}
	out := workroomToResponse(wr)
	if out.ID == "" || out.Name == "" || out.CreatedBy == "" {
		t.Fatalf("stable identity fields must be non-empty: %+v", out)
	}
	if out.IssueID == "" || out.WorkOrderID == "" {
		t.Fatalf("bound issue/workorder must map: %+v", out)
	}
	if out.ProjectID != "" {
		t.Fatalf("unset project_id must stay empty, got %q", out.ProjectID)
	}
}

func uuidMustParse(s string) pgtype.UUID {
	return parseUUID(s)
}

// WO-P3 workroom authority hardening: shared fixtures for the boundary tests
// below. All UUIDs cross a request boundary, so every malformed value must be
// rejected with a stable 400 before any DB access.
const (
	workroomTestWorkspaceID = "11111111-2222-4333-8444-555555555555"
	workroomTestUserID      = "66666666-7777-4888-9999-aaaaaaaaaaaa"
)

// withWorkroomID attaches the chi {id} URL param without a live router.
func withWorkroomID(req *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// decodeErrorBody asserts the stable writeError wire shape.
func decodeErrorBody(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v (raw: %q)", err, w.Body.String())
	}
	return body
}

// TestCreateWorkroom_RejectsMalformedUUIDs — malformed workspace/user/project/
// issue identifiers must answer a stable 400 naming the field, never reach the
// DB, and never degrade to a zero UUID inside the write query.
func TestCreateWorkroom_RejectsMalformedUUIDs(t *testing.T) {
	h := &Handler{} // validation must complete with no Queries wired
	cases := []struct {
		name        string
		workspaceID string
		userID      string
		body        string
		wantError   string
	}{
		{
			name:        "malformed workspace id",
			workspaceID: "not-a-uuid",
			userID:      workroomTestUserID,
			body:        `{"name":"scope-check"}`,
			wantError:   "invalid workspace id",
		},
		{
			name:        "malformed user id",
			workspaceID: workroomTestWorkspaceID,
			userID:      "0000",
			body:        `{"name":"scope-check"}`,
			wantError:   "invalid user id",
		},
		{
			name:        "malformed project id",
			workspaceID: workroomTestWorkspaceID,
			userID:      workroomTestUserID,
			body:        `{"name":"scope-check","project_id":"oops"}`,
			wantError:   "invalid project id",
		},
		{
			name:        "malformed issue id",
			workspaceID: workroomTestWorkspaceID,
			userID:      workroomTestUserID,
			body:        `{"name":"scope-check","issue_id":"12345"}`,
			wantError:   "invalid issue id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/workrooms", strings.NewReader(tc.body))
			req.Header.Set("X-Workspace-ID", tc.workspaceID)
			req.Header.Set("X-User-ID", tc.userID)
			w := httptest.NewRecorder()
			h.CreateWorkroom(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
			if got := decodeErrorBody(t, w)["error"]; got != tc.wantError {
				t.Fatalf("error = %q, want %q", got, tc.wantError)
			}
		})
	}
}

// TestGetWorkroom_RejectsMalformedIDs — the read path validates both the
// workspace identifier and the {id} URL param before touching the DB.
func TestGetWorkroom_RejectsMalformedIDs(t *testing.T) {
	h := &Handler{}
	cases := []struct {
		name        string
		workspaceID string
		workroomID  string
		wantError   string
	}{
		{
			name:        "malformed workroom id",
			workspaceID: workroomTestWorkspaceID,
			workroomID:  "not-a-uuid",
			wantError:   "invalid workroom id",
		},
		{
			name:        "malformed workspace id",
			workspaceID: "slug-not-uuid",
			workroomID:  workroomTestWorkspaceID,
			wantError:   "invalid workspace id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := withWorkroomID(httptest.NewRequest(http.MethodGet, "/api/workrooms/"+tc.workroomID, nil), tc.workroomID)
			req.Header.Set("X-Workspace-ID", tc.workspaceID)
			w := httptest.NewRecorder()
			h.GetWorkroom(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
			if got := decodeErrorBody(t, w)["error"]; got != tc.wantError {
				t.Fatalf("error = %q, want %q", got, tc.wantError)
			}
		})
	}
}

// TestListWorkrooms_RejectsMalformedWorkspaceID — the listing path keeps its
// workspace scoping and must not hand an invalid identifier to the DB.
func TestListWorkrooms_RejectsMalformedWorkspaceID(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workrooms", nil)
	req.Header.Set("X-Workspace-ID", "definitely-not-a-uuid")
	w := httptest.NewRecorder()
	h.ListWorkrooms(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if got := decodeErrorBody(t, w)["error"]; got != "invalid workspace id" {
		t.Fatalf("error = %q, want %q", got, "invalid workspace id")
	}
}

// seedWorkroomWorkspaces creates two fresh workspaces for the scoping tests so
// the shared fixture workspace is never mutated.
func seedWorkroomWorkspaces(t *testing.T) (string, string) {
	t.Helper()
	ids := make([]string, 2)
	for i, label := range []string{"workroom-scope-a", "workroom-scope-b"} {
		slug := label + "-" + uuid.NewString()[:8]
		if err := testPool.QueryRow(context.Background(), `
			INSERT INTO workspace (name, slug, description, issue_prefix)
			VALUES ($1, $1, '', 'WRS')
			RETURNING id::text
		`, slug).Scan(&ids[i]); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, id := range ids {
			testPool.Exec(context.Background(), `DELETE FROM workroom WHERE workspace_id = $1`, id)
			testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, id)
		}
	})
	return ids[0], ids[1]
}

// TestWorkroomWorkspaceScoping_DB — the DB leg of WO-P3: GetWorkroom is scoped
// by (id, workspace_id) at the query level, so the same-workspace lookup
// succeeds and every other workspace gets a not-found — never a cross-workspace
// read.
func TestWorkroomWorkspaceScoping_DB(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test database not configured")
	}
	wsA, wsB := seedWorkroomWorkspaces(t)
	ctx := context.Background()

	queries := db.New(testPool)
	created, err := queries.CreateWorkroom(ctx, db.CreateWorkroomParams{
		WorkspaceID: parseUUID(wsA),
		Name:        "Kai P3 scope probe",
		CreatedBy:   parseUUID(workroomTestUserID),
	})
	if err != nil {
		t.Fatalf("seed workroom: %v", err)
	}
	createdID := uuidToString(created.ID)

	// Query-level scoping: same workspace resolves, foreign workspace misses.
	if _, err := queries.GetWorkroom(ctx, db.GetWorkroomParams{ID: created.ID, WorkspaceID: parseUUID(wsA)}); err != nil {
		t.Fatalf("same-workspace GetWorkroom: %v", err)
	}
	if _, err := queries.GetWorkroom(ctx, db.GetWorkroomParams{ID: created.ID, WorkspaceID: parseUUID(wsB)}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace GetWorkroom error = %v, want pgx.ErrNoRows", err)
	}

	// Handler-level scoping: same workspace 200, foreign workspace 404.
	sameReq := withWorkroomID(httptest.NewRequest(http.MethodGet, "/api/workrooms/"+createdID, nil), createdID)
	sameReq.Header.Set("X-Workspace-ID", wsA)
	sameW := httptest.NewRecorder()
	testHandler.GetWorkroom(sameW, sameReq)
	if sameW.Code != http.StatusOK {
		t.Fatalf("same-workspace status = %d, want 200 (body: %s)", sameW.Code, sameW.Body.String())
	}
	var got WorkroomResponse
	if err := json.NewDecoder(sameW.Body).Decode(&got); err != nil {
		t.Fatalf("decode workroom: %v", err)
	}
	if got.ID != createdID || got.Name != "Kai P3 scope probe" || got.CreatedBy != workroomTestUserID {
		t.Fatalf("same-workspace body = %+v, want id/name/created_by echoed back", got)
	}

	foreignReq := withWorkroomID(httptest.NewRequest(http.MethodGet, "/api/workrooms/"+createdID, nil), createdID)
	foreignReq.Header.Set("X-Workspace-ID", wsB)
	foreignW := httptest.NewRecorder()
	testHandler.GetWorkroom(foreignW, foreignReq)
	if foreignW.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace status = %d, want 404 (body: %s)", foreignW.Code, foreignW.Body.String())
	}
	if got := decodeErrorBody(t, foreignW)["error"]; got != "workroom not found" {
		t.Fatalf("cross-workspace error = %q, want %q", got, "workroom not found")
	}
}

// TestCreateWorkroom_SameWorkspaceSuccess_DB — happy path: a valid create in
// the fixture workspace returns 201 with the pinned response shape, is
// readable through GetWorkroom, and shows up in ListWorkrooms for that
// workspace only.
func TestCreateWorkroom_SameWorkspaceSuccess_DB(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test database not configured")
	}
	ctx := context.Background()

	body := `{"name":"Kai P3 create probe","work_order_id":"WO-P3-KAI-WORKROOM-AUTHORITY-HARDENING"}`
	req := httptest.NewRequest(http.MethodPost, "/api/workrooms", strings.NewReader(body))
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-User-ID", testUserID)
	w := httptest.NewRecorder()
	testHandler.CreateWorkroom(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	var created WorkroomResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode created workroom: %v", err)
	}
	if _, err := uuid.Parse(created.ID); err != nil {
		t.Fatalf("created id %q is not a UUID: %v", created.ID, err)
	}
	if created.Name != "Kai P3 create probe" || created.CreatedBy != testUserID {
		t.Fatalf("created body = %+v, want name/created_by echoed back", created)
	}
	if created.ProjectID != "" || created.IssueID != "" {
		t.Fatalf("unset bindings must stay empty, got %+v", created)
	}
	if created.WorkOrderID != "WO-P3-KAI-WORKROOM-AUTHORITY-HARDENING" {
		t.Fatalf("work_order_id = %q, want passthrough", created.WorkOrderID)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM workroom WHERE id = $1`, created.ID)
	})

	// Round-trip through the scoped read path.
	getReq := withWorkroomID(httptest.NewRequest(http.MethodGet, "/api/workrooms/"+created.ID, nil), created.ID)
	getReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	getW := httptest.NewRecorder()
	testHandler.GetWorkroom(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("roundtrip status = %d, want 200 (body: %s)", getW.Code, getW.Body.String())
	}

	// ListWorkrooms keeps its workspace scoping and array-of-objects shape.
	listReq := httptest.NewRequest(http.MethodGet, "/api/workrooms", nil)
	listReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	listW := httptest.NewRecorder()
	testHandler.ListWorkrooms(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body: %s)", listW.Code, listW.Body.String())
	}
	var listed []WorkroomResponse
	if err := json.NewDecoder(listW.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, wr := range listed {
		if wr.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created workroom %s missing from ListWorkrooms output", created.ID)
	}
}
