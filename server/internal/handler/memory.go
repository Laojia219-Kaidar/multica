package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/memory"
)

// The employee memory candidate layer (Slice-M1) is now exposed over HTTP.
// The in-memory Store is a process-global singleton; persistence reads/writes
// go through memory.Repository (migration 342). Promotion is a PROPOSAL
// receipt only — it never writes HiveCosm Knowledge/Harness truth (D5).

var (
	memStoreOnce sync.Once
	memStore     *memory.Store
)

func memoryStore() *memory.Store {
	memStoreOnce.Do(func() { memStore = memory.NewStore() })
	return memStore
}

type memoryEvidenceRefDTO struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type memoryCandidateDTO struct {
	ID         string                 `json:"id"`
	EmployeeID string                 `json:"employee_id"`
	PositionID string                 `json:"position_id,omitempty"`
	Kind       string                 `json:"kind"`
	Content    string                 `json:"content"`
	Evidence   []memoryEvidenceRefDTO `json:"evidence"`
	SourceRefs []string               `json:"source_refs"`
	AuthorID   string                 `json:"author_id"`
	CreatedAt  string                 `json:"created_at"`
	Status     string                 `json:"status"`
}

func toMemoryCandidateDTO(c memory.MemoryCandidate) memoryCandidateDTO {
	ev := make([]memoryEvidenceRefDTO, 0, len(c.Evidence))
	for _, e := range c.Evidence {
		ev = append(ev, memoryEvidenceRefDTO{Type: e.Type, ID: e.ID})
	}
	return memoryCandidateDTO{
		ID:         c.ID,
		EmployeeID: c.EmployeeID,
		PositionID: c.PositionID,
		Kind:       string(c.Kind),
		Content:    c.Content,
		Evidence:   ev,
		SourceRefs: c.SourceRefs,
		AuthorID:   c.AuthorID,
		CreatedAt:  c.CreatedAt.UTC().Format(time.RFC3339),
		Status:     string(c.Status),
	}
}

type memoryPromotionDTO struct {
	CandidateID string `json:"candidate_id"`
	Target      string `json:"target"`
	ReviewerID  string `json:"reviewer_id"`
	Approved    bool   `json:"approved"`
	Reason      string `json:"reason"`
	PromotedAt  string `json:"promoted_at"`
}

func toMemoryPromotionDTO(p memory.MemoryPromotion) memoryPromotionDTO {
	return memoryPromotionDTO{
		CandidateID: p.CandidateID,
		Target:      string(p.Target),
		ReviewerID:  p.ReviewerID,
		Approved:    p.Approved,
		Reason:      p.Reason,
		PromotedAt:  p.PromotedAt.UTC().Format(time.RFC3339),
	}
}

type createMemoryCandidateRequest struct {
	EmployeeID string                 `json:"employee_id"`
	PositionID string                 `json:"position_id,omitempty"`
	Kind       string                 `json:"kind"`
	Content    string                 `json:"content"`
	Evidence   []memoryEvidenceRefDTO `json:"evidence"`
	SourceRefs []string               `json:"source_refs"`
}

func (h *Handler) memoryRepo() *memory.Repository { return memory.NewRepository(h.Queries) }

// CreateMemoryCandidate POST /api/memory/candidates
func (h *Handler) CreateMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	var req createMemoryCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ev := make([]memory.EvidenceRef, 0, len(req.Evidence))
	for _, e := range req.Evidence {
		ev = append(ev, memory.EvidenceRef{Type: e.Type, ID: e.ID})
	}
	c := memory.MemoryCandidate{
		ID:         uuid.NewString(),
		EmployeeID: req.EmployeeID,
		PositionID: req.PositionID,
		Kind:       memory.MemoryKind(req.Kind),
		Content:    req.Content,
		Evidence:   ev,
		SourceRefs: req.SourceRefs,
		AuthorID:   requestUserID(r),
	}
	created, err := memoryStore().Create(c)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.memoryRepo().SaveCandidate(r.Context(), created); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist memory candidate")
		return
	}
	writeJSON(w, http.StatusOK, toMemoryCandidateDTO(created))
}

// ListMemoryCandidates GET /api/memory/candidates?employee_id= | ?position_id=
func (h *Handler) ListMemoryCandidates(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	q := r.URL.Query()
	employeeID := q.Get("employee_id")
	positionID := q.Get("position_id")

	var out []memory.MemoryCandidate
	if positionID != "" {
		out = memoryStore().ListByPosition(positionID)
	} else if employeeID != "" {
		// Resume-after-restart read-back: hydrate this employee's candidates.
		if err := memoryStore().Hydrate(r.Context(), h.memoryRepo(), employeeID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to hydrate memory")
			return
		}
		out = memoryStore().List(employeeID)
	} else {
		// Workspace-wide listing reads the durable table directly so
		// candidates persisted before a restart remain visible.
		recent, err := h.memoryRepo().ListRecent(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list memory candidates")
			return
		}
		out = recent
	}
	dtos := make([]memoryCandidateDTO, 0, len(out))
	for _, c := range out {
		dtos = append(dtos, toMemoryCandidateDTO(c))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// ValidateMemoryCandidate POST /api/memory/candidates/{id}/validate
func (h *Handler) ValidateMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	id := r.PathValue("id")
	// The in-memory store loses candidates across restarts; rehydrate the
	// owning employee from the durable table before mutating so a
	// post-restart promotion/revocation doesn't 404 on a candidate that
	// List just showed.
	h.hydrateMemoryCandidate(r.Context(), id)
	c, err := memoryStore().ValidateCandidate(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "memory candidate not found")
		return
	}
	if err := h.memoryRepo().UpdateStatus(r.Context(), id, c.Status); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist validation")
		return
	}
	writeJSON(w, http.StatusOK, toMemoryCandidateDTO(c))
}

// hydrateMemoryCandidate loads the candidate row and hydrates its employee's
// store partition, so lifecycle mutations work after a server restart.
// Failures are best-effort: the caller's store call surfaces the 404.
func (h *Handler) hydrateMemoryCandidate(ctx context.Context, id string) {
	c, err := h.memoryRepo().LoadCandidate(ctx, id)
	if err != nil {
		return
	}
	_ = memoryStore().Hydrate(ctx, h.memoryRepo(), c.EmployeeID)
}

type promoteMemoryRequest struct {
	Target   string `json:"target"`
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
}

// PromoteMemoryCandidate POST /api/memory/candidates/{id}/promote
func (h *Handler) PromoteMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	id := r.PathValue("id")
	var req promoteMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Always rehydrate before promoting: the in-memory store starts empty
	// after restarts AND can hold stale copies if the durable row changed;
	// promotion is a rare admin action so a fresh read is cheap.
	h.hydrateMemoryCandidate(r.Context(), id)
	p, err := memoryStore().Promote(id, memory.PromotionTarget(req.Target), requestUserID(r), req.Approved, req.Reason)
	if err != nil {
		if errors.Is(err, memory.ErrCandidateNotFound) {
			// Retry once after rehydrating from the durable table — the
			// in-memory store starts empty after every server restart.
			h.hydrateMemoryCandidate(r.Context(), id)
			p, err = memoryStore().Promote(id, memory.PromotionTarget(req.Target), requestUserID(r), req.Approved, req.Reason)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	repo := h.memoryRepo()
	if err := repo.SavePromotion(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist promotion receipt")
		return
	}
	c, _ := memoryStore().ValidateCandidate(id)
	if err := repo.UpdateStatus(r.Context(), id, c.Status); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist promotion status")
		return
	}
	writeJSON(w, http.StatusOK, toMemoryPromotionDTO(p))
}

type revokeMemoryRequest struct {
	Reason string `json:"reason"`
}

// RevokeMemoryCandidate POST /api/memory/candidates/{id}/revoke
func (h *Handler) RevokeMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	id := r.PathValue("id")
	var req revokeMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Same freshness rule as promotion.
	h.hydrateMemoryCandidate(r.Context(), id)
	c, err := memoryStore().Revoke(id, req.Reason, requestUserID(r))
	if err != nil {
		if errors.Is(err, memory.ErrCandidateNotFound) {
			h.hydrateMemoryCandidate(r.Context(), id)
			c, err = memoryStore().Revoke(id, req.Reason, requestUserID(r))
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := h.memoryRepo().UpdateStatus(r.Context(), id, c.Status); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist revocation")
		return
	}
	writeJSON(w, http.StatusOK, toMemoryCandidateDTO(c))
}

// ListPromotedMemories GET /api/memory/promoted?target=employee_memory|team_playbook|skill
func (h *Handler) ListPromotedMemories(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	target := memory.PromotionTarget(r.URL.Query().Get("target"))
	out := memoryStore().Promoted(target)
	dtos := make([]memoryCandidateDTO, 0, len(out))
	for _, c := range out {
		dtos = append(dtos, toMemoryCandidateDTO(c))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// RetrieveMemories GET /api/memory/retrieve?employee_id=&q=
func (h *Handler) RetrieveMemories(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	q := r.URL.Query()
	employeeID := q.Get("employee_id")
	if employeeID == "" {
		writeError(w, http.StatusBadRequest, "employee_id required")
		return
	}
	if err := memoryStore().Hydrate(r.Context(), h.memoryRepo(), employeeID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hydrate memory")
		return
	}
	out := memoryStore().Retrieve(employeeID, strings.TrimSpace(q.Get("q")))
	dtos := make([]memoryCandidateDTO, 0, len(out))
	for _, c := range out {
		dtos = append(dtos, toMemoryCandidateDTO(c))
	}
	writeJSON(w, http.StatusOK, dtos)
}
