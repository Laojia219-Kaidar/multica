package memory

import (
	"context"
	"encoding/json"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func marshalEvidence(e []EvidenceRef) ([]byte, error) {
	if e == nil {
		e = []EvidenceRef{}
	}
	return json.Marshal(e)
}

func unmarshalEvidence(b []byte) ([]EvidenceRef, error) {
	var e []EvidenceRef
	if len(b) == 0 {
		return e, nil
	}
	return e, json.Unmarshal(b, &e)
}

func marshalStrings(s []string) ([]byte, error) {
	if s == nil {
		s = []string{}
	}
	return json.Marshal(s)
}

func unmarshalStrings(b []byte) ([]string, error) {
	var s []string
	if len(b) == 0 {
		return s, nil
	}
	return s, json.Unmarshal(b, &s)
}

// Repository persists employee memory candidate state through sqlc queries.
// It is a read/write model for the HiveCrew candidate layer only; formal
// knowledge/playbook/skill authority remains external.
type Repository struct {
	Q *db.Queries
}

func NewRepository(q *db.Queries) *Repository { return &Repository{Q: q} }

func (r *Repository) SaveCandidate(ctx context.Context, c MemoryCandidate) error {
	ev, err := marshalEvidence(c.Evidence)
	if err != nil {
		return err
	}
	refs, err := marshalStrings(c.SourceRefs)
	if err != nil {
		return err
	}
	return r.Q.InsertMemoryCandidate(ctx, db.InsertMemoryCandidateParams{
		ID: c.ID, EmployeeID: c.EmployeeID, PositionID: c.PositionID, Kind: string(c.Kind),
		Content: c.Content, Evidence: ev, SourceRefs: refs, AuthorID: c.AuthorID, Status: string(c.Status),
	})
}

func (r *Repository) LoadCandidate(ctx context.Context, id string) (MemoryCandidate, error) {
	row, err := r.Q.GetMemoryCandidate(ctx, id)
	if err != nil {
		return MemoryCandidate{}, err
	}
	ev, err := unmarshalEvidence(row.Evidence)
	if err != nil {
		return MemoryCandidate{}, err
	}
	refs, err := unmarshalStrings(row.SourceRefs)
	if err != nil {
		return MemoryCandidate{}, err
	}
	return MemoryCandidate{
		ID: row.ID, EmployeeID: row.EmployeeID, PositionID: row.PositionID, Kind: MemoryKind(row.Kind),
		Content: row.Content, Evidence: ev, SourceRefs: refs, AuthorID: row.AuthorID,
		Status: CandidateStatus(row.Status), CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (r *Repository) ListByEmployee(ctx context.Context, employeeID string) ([]MemoryCandidate, error) {
	rows, err := r.Q.ListMemoryCandidatesByEmployee(ctx, employeeID)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryCandidate, 0, len(rows))
	for _, row := range rows {
		c, err := r.LoadCandidate(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ListRecent returns the most recent candidates across the workspace,
// reading the durable table so pre-restart candidates stay visible.
func (r *Repository) ListRecent(ctx context.Context) ([]MemoryCandidate, error) {
	rows, err := r.Q.ListMemoryCandidatesRecent(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryCandidate, 0, len(rows))
	for _, row := range rows {
		c, err := r.LoadCandidate(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *Repository) ListByPosition(ctx context.Context, positionID string) ([]MemoryCandidate, error) {
	rows, err := r.Q.ListMemoryCandidatesByPosition(ctx, positionID)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryCandidate, 0, len(rows))
	for _, row := range rows {
		c, err := r.LoadCandidate(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id string, status CandidateStatus) error {
	return r.Q.UpdateMemoryCandidateStatus(ctx, db.UpdateMemoryCandidateStatusParams{
		ID: id, Status: string(status),
	})
}

func (r *Repository) SavePromotion(ctx context.Context, p MemoryPromotion) error {
	return r.Q.InsertMemoryPromotion(ctx, db.InsertMemoryPromotionParams{
		CandidateID: p.CandidateID, Target: string(p.Target), ReviewerID: p.ReviewerID,
		Approved: p.Approved, Reason: p.Reason,
	})
}

func (r *Repository) ListPromotions(ctx context.Context, candidateID string) ([]MemoryPromotion, error) {
	rows, err := r.Q.ListMemoryPromotions(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryPromotion, 0, len(rows))
	for _, row := range rows {
		out = append(out, MemoryPromotion{
			CandidateID: row.CandidateID, Target: PromotionTarget(row.Target),
			ReviewerID: row.ReviewerID, Approved: row.Approved, Reason: row.Reason,
			PromotedAt: row.PromotedAt.Time,
		})
	}
	return out, nil
}

func (r *Repository) SaveRevocation(ctx context.Context, rev Revocation) error {
	return r.Q.InsertMemoryRevocation(ctx, db.InsertMemoryRevocationParams{
		CandidateID: rev.CandidateID, ReviewerID: rev.ReviewerID, Reason: rev.Reason,
	})
}

func (r *Repository) ListRevocations(ctx context.Context, candidateID string) ([]Revocation, error) {
	rows, err := r.Q.ListMemoryRevocations(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	out := make([]Revocation, 0, len(rows))
	for _, row := range rows {
		out = append(out, Revocation{
			CandidateID: row.CandidateID, ReviewerID: row.ReviewerID, Reason: row.Reason,
			RevokedAt: row.RevokedAt.Time,
		})
	}
	return out, nil
}
