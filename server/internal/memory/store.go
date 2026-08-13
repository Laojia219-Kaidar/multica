package memory

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrCandidateNotFound = errors.New("memory candidate not found")
	ErrSelfPromotion     = errors.New("promotion requires an independent reviewer")
	ErrNotValidated      = errors.New("candidate must be validated before promotion")
)

// Store is the in-memory (Slice-M1) candidate read model. Persistence and
// formal promotion to HiveCosm Knowledge/Harness authorities are gated on the
// Owner decisions; this store only holds the HiveCrew candidate layer.
type Store struct {
	mu          sync.Mutex
	candidates  map[string]*MemoryCandidate
	promotions  []MemoryPromotion
	revocations []Revocation
	working     map[string]WorkingMemory
	now         func() time.Time
}

func NewStore() *Store {
	return &Store{
		candidates: map[string]*MemoryCandidate{},
		working:    map[string]WorkingMemory{},
		now:        time.Now,
	}
}

func (s *Store) SetClock(fn func() time.Time) { s.now = fn }

func (s *Store) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// Create stores a validated candidate (idempotent by ID). It never stores an
// invalid candidate.
func (s *Store) Create(c MemoryCandidate) (MemoryCandidate, error) {
	if err := Validate(c); err != nil {
		return MemoryCandidate{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.candidates[c.ID]; ok {
		return *existing, nil
	}
	cp := c
	cp.Status = StatusPending
	cp.CreatedAt = s.clock()
	s.candidates[c.ID] = &cp
	return cp, nil
}

// ValidateCandidate moves a pending candidate to validated (idempotent).
func (s *Store) ValidateCandidate(id string) (MemoryCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.candidates[id]
	if !ok {
		return MemoryCandidate{}, ErrCandidateNotFound
	}
	if c.Status == StatusPending {
		c.Status = StatusValidated
	}
	return *c, nil
}

// Promote records a promotion proposal receipt. The reviewer must be
// independent (never the author), and the candidate must be validated.
// Approved -> promoted; rejected -> rejected. This is a PROPOSAL, not a write
// to the HiveCosm Knowledge/Harness authority.
func (s *Store) Promote(id string, target PromotionTarget, reviewerID string, approved bool, reason string) (MemoryPromotion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.candidates[id]
	if !ok {
		return MemoryPromotion{}, ErrCandidateNotFound
	}
	if reviewerID == "" || reviewerID == c.AuthorID {
		return MemoryPromotion{}, ErrSelfPromotion
	}
	if c.Status != StatusValidated {
		return MemoryPromotion{}, ErrNotValidated
	}
	p := MemoryPromotion{
		CandidateID: id,
		Target:      target,
		ReviewerID:  reviewerID,
		Approved:    approved,
		Reason:      reason,
		PromotedAt:  s.clock(),
	}
	s.promotions = append(s.promotions, p)
	if approved {
		c.Status = StatusPromoted
	} else {
		c.Status = StatusRejected
	}
	return p, nil
}

// Revoke retracts a promoted memory (错误经验撤销). The reviewer must be
// independent, and the memory must already be promoted. The revocation is
// append-only; the memory is never deleted.
func (s *Store) Revoke(id, reason, reviewerID string) (MemoryCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.candidates[id]
	if !ok {
		return MemoryCandidate{}, ErrCandidateNotFound
	}
	if reviewerID == "" || reviewerID == c.AuthorID {
		return MemoryCandidate{}, ErrSelfPromotion
	}
	if c.Status != StatusPromoted {
		return MemoryCandidate{}, ErrNotValidated
	}
	c.Status = StatusRevoked
	s.revocations = append(s.revocations, Revocation{
		CandidateID: id,
		ReviewerID:  reviewerID,
		Reason:      reason,
		RevokedAt:   s.clock(),
	})
	return *c, nil
}

// Retrieve returns only this employee's PROMOTED (verified) memories matching
// the query, newest first. Revoked/rejected/pending/validated candidates never
// hit. An empty query returns all verified memories for the employee.
func (s *Store) Retrieve(employeeID, query string) []MemoryCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]MemoryCandidate, 0)
	for _, c := range s.candidates {
		if c.EmployeeID != employeeID || c.Status != StatusPromoted {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(c.Content), q) {
			refHit := false
			for _, e := range c.Evidence {
				if strings.Contains(strings.ToLower(e.ID), q) {
					refHit = true
					break
				}
			}
			if !refHit {
				continue
			}
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// SetWorkingMemory stores per-employee working context with a TTL.
func (s *Store) SetWorkingMemory(employeeID, key, value string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.working[employeeID+"\x00"+key] = WorkingMemory{
		EmployeeID: employeeID,
		Key:        key,
		Value:      value,
		ExpiresAt:  s.clock().Add(ttl),
	}
}

// GetWorkingMemory returns the working context if it exists and has not
// expired; otherwise ("", false). Expired entries are evicted on read.
func (s *Store) GetWorkingMemory(employeeID, key string, now time.Time) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := employeeID + "\x00" + key
	wm, ok := s.working[k]
	if !ok || now.After(wm.ExpiresAt) {
		if ok {
			delete(s.working, k)
		}
		return "", false
	}
	return wm.Value, true
}

// SweepWorkingMemory evicts all expired working-memory entries.
func (s *Store) SweepWorkingMemory(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for k, wm := range s.working {
		if now.After(wm.ExpiresAt) {
			delete(s.working, k)
			removed++
		}
	}
	return removed
}

// ListByPosition returns candidates tagged with the given position (岗位记忆
// read-model), newest first, across employees.
func (s *Store) ListByPosition(positionID string) []MemoryCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MemoryCandidate, 0)
	for _, c := range s.candidates {
		if c.PositionID == positionID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Promoted returns the projection of candidates promoted to the given target
// (employee_memory / team_playbook / skill). This is a read-only projection —
// the formal Playbook/Skill truth remains the HiveCosm Harness/Knowledge
// authority; this only mirrors the promotion receipts.
func (s *Store) Promoted(target PromotionTarget) []MemoryCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MemoryCandidate, 0)
	for _, c := range s.candidates {
		if c.Status != StatusPromoted {
			continue
		}
		// Recover the promotion target from the receipt log for this candidate.
		if s.promotionTarget(c.ID) == target {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// promotionTarget returns the target of the latest approved promotion for a
// candidate, or "" if none.
func (s *Store) promotionTarget(candidateID string) PromotionTarget {
	var target PromotionTarget
	for _, p := range s.promotions {
		if p.CandidateID == candidateID && p.Approved {
			target = p.Target
		}
	}
	return target
}

// Revocations returns the append-only revocation records, in order.
func (s *Store) Revocations() []Revocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Revocation, len(s.revocations))
	copy(out, s.revocations)
	return out
}

// List is the read model: only the given employee's candidates, newest first.
// Cross-employee isolation is enforced here, not by the caller.
func (s *Store) List(employeeID string) []MemoryCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MemoryCandidate, 0)
	for _, c := range s.candidates {
		if c.EmployeeID == employeeID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Promotions returns the append-only promotion receipts, in order.
func (s *Store) Promotions() []MemoryPromotion {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MemoryPromotion, len(s.promotions))
	copy(out, s.promotions)
	return out
}
