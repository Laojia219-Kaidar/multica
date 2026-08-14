package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
)

type reviewDispatchHandlerFixture struct {
	preview      service.ReviewDispatchPreview
	result       service.ReviewDispatchBatchResult
	err          error
	previewCall  int
	dispatchCall int
	workspaceID  pgtype.UUID
	projectID    pgtype.UUID
	actorUserID  pgtype.UUID
	limit        int
	offset       int
}

func (f *reviewDispatchHandlerFixture) PreviewProject(_ context.Context, workspaceID, projectID pgtype.UUID, limit, offset int) (service.ReviewDispatchPreview, error) {
	f.previewCall++
	f.workspaceID, f.projectID, f.limit, f.offset = workspaceID, projectID, limit, offset
	return f.preview, f.err
}

func (f *reviewDispatchHandlerFixture) DispatchProject(_ context.Context, workspaceID, projectID, actorUserID pgtype.UUID, limit, offset int) (service.ReviewDispatchBatchResult, error) {
	f.dispatchCall++
	f.workspaceID, f.projectID, f.actorUserID, f.limit, f.offset = workspaceID, projectID, actorUserID, limit, offset
	return f.result, f.err
}

func withReviewDispatchProjectParam(req *http.Request, projectID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestProjectReviewDispatchPreviewAndExplicitBatchAreOwnerControlled(t *testing.T) {
	projectID := "00000000-0000-0000-0000-000000000601"
	fixture := &reviewDispatchHandlerFixture{
		preview: service.ReviewDispatchPreview{SchemaVersion: "hivecrew.review-dispatch-preview/v1"},
		result:  service.ReviewDispatchBatchResult{Preview: service.ReviewDispatchPreview{SchemaVersion: "hivecrew.review-dispatch-preview/v1"}},
	}
	h := *testHandler
	h.ReviewDispatch = fixture

	previewReq := withReviewDispatchProjectParam(newRequest(http.MethodGet,
		"/api/projects/"+projectID+"/review-dispatch/preview?workspace_id="+testWorkspaceID+"&limit=25&offset=0", nil), projectID)
	previewW := httptest.NewRecorder()
	h.GetProjectReviewDispatchPreview(previewW, previewReq)
	if previewW.Code != http.StatusOK || fixture.previewCall != 1 || fixture.limit != 25 || fixture.offset != 0 {
		t.Fatalf("preview status/call/page = %d/%d/%d/%d body=%s", previewW.Code, fixture.previewCall, fixture.limit, fixture.offset, previewW.Body.String())
	}

	dispatchReq := withReviewDispatchProjectParam(newRequest(http.MethodPost,
		"/api/projects/"+projectID+"/review-dispatch/dispatch?workspace_id="+testWorkspaceID, map[string]any{}), projectID)
	dispatchW := httptest.NewRecorder()
	h.DispatchProjectReviewBatch(dispatchW, dispatchReq)
	if dispatchW.Code != http.StatusOK || fixture.dispatchCall != 1 || fixture.limit != 1 || uuidToString(fixture.actorUserID) != testUserID {
		t.Fatalf("dispatch status/call/limit/actor = %d/%d/%d/%s body=%s", dispatchW.Code, fixture.dispatchCall, fixture.limit, uuidToString(fixture.actorUserID), dispatchW.Body.String())
	}
	var body service.ReviewDispatchBatchResult
	if err := json.Unmarshal(dispatchW.Body.Bytes(), &body); err != nil || body.Preview.SchemaVersion == "" {
		t.Fatalf("dispatch body = %s err=%v", dispatchW.Body.String(), err)
	}
}

func TestProjectReviewDispatchRejectsRouteChoiceAndOversizedBatch(t *testing.T) {
	projectID := "00000000-0000-0000-0000-000000000602"
	fixture := &reviewDispatchHandlerFixture{}
	h := *testHandler
	h.ReviewDispatch = fixture
	for _, request := range []*http.Request{
		withReviewDispatchProjectParam(newRequest(http.MethodPost,
			"/api/projects/"+projectID+"/review-dispatch/dispatch?workspace_id="+testWorkspaceID, map[string]any{"agent_id": "client-selected"}), projectID),
		withReviewDispatchProjectParam(newRequest(http.MethodPost,
			"/api/projects/"+projectID+"/review-dispatch/dispatch?workspace_id="+testWorkspaceID+"&limit=26", map[string]any{}), projectID),
	} {
		w := httptest.NewRecorder()
		h.DispatchProjectReviewBatch(w, request)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s, want bad request", w.Code, w.Body.String())
		}
	}
	if fixture.dispatchCall != 0 {
		t.Fatalf("rejected browser input dispatched %d batches", fixture.dispatchCall)
	}
}

var _ interface {
	PreviewProject(context.Context, pgtype.UUID, pgtype.UUID, int, int) (service.ReviewDispatchPreview, error)
	DispatchProject(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, int, int) (service.ReviewDispatchBatchResult, error)
} = (*reviewDispatchHandlerFixture)(nil)
