package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func ev(typ, id string) EvidenceRef { return EvidenceRef{Type: typ, ID: id} }

func candidate(over func(*MemoryCandidate)) MemoryCandidate {
	c := MemoryCandidate{
		ID:         "c1",
		EmployeeID: "EMP-01",
		Kind:       KindEpisodic,
		Content:    "run completed with 32 passing tests",
		Evidence:   []EvidenceRef{ev("run", "r1")},
		SourceRefs: []string{"run://r1"},
		AuthorID:   "EMP-01",
	}
	if over != nil {
		over(&c)
	}
	return c
}

func TestValidate_AcceptsValidCandidate(t *testing.T) {
	if err := Validate(candidate(nil)); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
}

func TestValidate_RejectsSecrets(t *testing.T) {
	cases := []string{
		"api_key=abc123", "API-KEY: abc123", "password = hunter2",
		"postgres://user:pass@host/db", "mongodb://u:p@h", "redis://h",
		"-----BEGIN RSA PRIVATE KEY-----",
	}
	for _, content := range cases {
		t.Run(content, func(t *testing.T) {
			err := Validate(candidate(func(c *MemoryCandidate) { c.Content = content }))
			if !errors.Is(err, ErrSecretContent) {
				t.Fatalf("expected ErrSecretContent, got %v", err)
			}
		})
	}
}

func TestValidate_RejectsMissingOrBadEvidence(t *testing.T) {
	err := Validate(candidate(func(c *MemoryCandidate) { c.Evidence = nil }))
	if !errors.Is(err, ErrMissingEvidence) {
		t.Fatalf("expected ErrMissingEvidence, got %v", err)
	}
	err = Validate(candidate(func(c *MemoryCandidate) { c.Evidence = []EvidenceRef{{Type: "chat", ID: "x"}} }))
	if !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("expected ErrInvalidCandidate for bad evidence type, got %v", err)
	}
}

func TestValidate_RejectsEmptyOrOversizedContent(t *testing.T) {
	if err := Validate(candidate(func(c *MemoryCandidate) { c.Content = "  " })); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("empty content: got %v", err)
	}
	big := strings.Repeat("x", maxContentBytes+1)
	if err := Validate(candidate(func(c *MemoryCandidate) { c.Content = big })); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("oversized content: got %v", err)
	}
}

func TestValidate_ExperienceRequiresTwoEvidence(t *testing.T) {
	c := candidate(func(c *MemoryCandidate) { c.Kind = KindExperience })
	if err := Validate(c); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("experience with 1 evidence: got %v", err)
	}
	c.Evidence = []EvidenceRef{ev("task", "t1"), ev("run", "r1")}
	if err := Validate(c); err != nil {
		t.Fatalf("experience with 2 evidence: got %v", err)
	}
}

func TestStore_PromoteRequiresIndependentReviewer(t *testing.T) {
	s := NewStore()
	_, _ = s.Create(candidate(nil))
	_, _ = s.ValidateCandidate("c1")

	if _, err := s.Promote("c1", TargetEmployeeMemory, "EMP-01", true, ""); !errors.Is(err, ErrSelfPromotion) {
		t.Fatalf("self-promotion: got %v", err)
	}
	if _, err := s.Promote("c1", TargetEmployeeMemory, "REV-01", true, "verified"); err != nil {
		t.Fatalf("independent promotion: %v", err)
	}
}

func TestStore_PromoteRequiresValidationFirst(t *testing.T) {
	s := NewStore()
	_, _ = s.Create(candidate(nil)) // still pending
	if _, err := s.Promote("c1", TargetEmployeeMemory, "REV-01", true, ""); !errors.Is(err, ErrNotValidated) {
		t.Fatalf("promote before validate: got %v", err)
	}
}

func TestStore_ReadModelIsPerEmployeeNamespace(t *testing.T) {
	s := NewStore()
	_, _ = s.Create(candidate(nil)) // EMP-01
	_, _ = s.Create(candidate(func(c *MemoryCandidate) { c.ID = "c2"; c.EmployeeID = "EMP-02"; c.AuthorID = "EMP-02" }))

	if got := s.List("EMP-01"); len(got) != 1 || got[0].ID != "c1" {
		t.Fatalf("EMP-01 list = %+v", got)
	}
	if got := s.List("EMP-02"); len(got) != 1 || got[0].ID != "c2" {
		t.Fatalf("EMP-02 list = %+v", got)
	}
}

func TestStore_FullPromotionLoop(t *testing.T) {
	fixed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	s := NewStore()
	s.SetClock(func() time.Time { return fixed })

	c, err := s.Create(candidate(nil))
	if err != nil || c.Status != StatusPending {
		t.Fatalf("create: %v %+v", err, c)
	}
	c, _ = s.ValidateCandidate("c1")
	if c.Status != StatusValidated {
		t.Fatalf("validate: %+v", c)
	}
	p, err := s.Promote("c1", TargetEmployeeMemory, "REV-01", true, "verified")
	if err != nil || !p.Approved || p.ReviewerID != "REV-01" {
		t.Fatalf("promote: %v %+v", err, p)
	}
	if got := s.List("EMP-01"); got[0].Status != StatusPromoted {
		t.Fatalf("status after promote = %q", got[0].Status)
	}
	if n := len(s.Promotions()); n != 1 {
		t.Fatalf("promotions len = %d", n)
	}
}

func TestStore_RevokeExcludesFromRetrieve(t *testing.T) {
	s := NewStore()
	_, _ = s.Create(candidate(nil))
	_, _ = s.ValidateCandidate("c1")
	_, _ = s.Promote("c1", TargetEmployeeMemory, "REV-01", true, "verified")

	if got := s.Retrieve("EMP-01", ""); len(got) != 1 {
		t.Fatalf("before revoke, retrieve len = %d", len(got))
	}

	// Self-revoke is rejected.
	if _, err := s.Revoke("c1", "wrong", "EMP-01"); !errors.Is(err, ErrSelfPromotion) {
		t.Fatalf("self-revoke: %v", err)
	}

	c, err := s.Revoke("c1", "wrong conclusion", "REV-02")
	if err != nil || c.Status != StatusRevoked {
		t.Fatalf("revoke: %v %+v", err, c)
	}
	if got := s.Retrieve("EMP-01", ""); len(got) != 0 {
		t.Fatalf("revoked memory must not hit retrieve, got %d", len(got))
	}
	if n := len(s.Revocations()); n != 1 {
		t.Fatalf("revocations len = %d", n)
	}
}

func TestStore_RetrieveOnlyVerifiedAndMatching(t *testing.T) {
	s := NewStore()
	// pending candidate must not hit
	_, _ = s.Create(candidate(func(c *MemoryCandidate) { c.ID = "p1"; c.Content = "alpha" }))
	// promoted candidate (EMP-01)
	_, _ = s.Create(candidate(func(c *MemoryCandidate) { c.ID = "v1"; c.Content = "postgres queries tuned" }))
	_, _ = s.ValidateCandidate("v1")
	_, _ = s.Promote("v1", TargetEmployeeMemory, "REV-01", true, "ok")
	// promoted candidate for another employee must not hit EMP-01
	_, _ = s.Create(candidate(func(c *MemoryCandidate) {
		c.ID = "v2"
		c.EmployeeID = "EMP-02"
		c.AuthorID = "EMP-02"
		c.Content = "postgres queries tuned"
	}))
	_, _ = s.ValidateCandidate("v2")
	_, _ = s.Promote("v2", TargetEmployeeMemory, "REV-01", true, "ok")

	if got := s.Retrieve("EMP-01", ""); len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("retrieve all = %+v", got)
	}
	if got := s.Retrieve("EMP-01", "postgres"); len(got) != 1 {
		t.Fatalf("retrieve by content = %+v", got)
	}
	if got := s.Retrieve("EMP-01", "nonexistent"); len(got) != 0 {
		t.Fatalf("retrieve no-match = %+v", got)
	}
}

func TestWorkingMemory_TTLExpiry(t *testing.T) {
	fixed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	s := NewStore()
	s.SetClock(func() time.Time { return fixed })

	s.SetWorkingMemory("EMP-01", "task-1", "frontier issue HIV-100", 5*time.Minute)

	if v, ok := s.GetWorkingMemory("EMP-01", "task-1", fixed.Add(1*time.Minute)); !ok || v != "frontier issue HIV-100" {
		t.Fatalf("within TTL: v=%q ok=%v", v, ok)
	}
	// After TTL: expired and evicted.
	if _, ok := s.GetWorkingMemory("EMP-01", "task-1", fixed.Add(6*time.Minute)); ok {
		t.Fatalf("after TTL must be expired")
	}
}

func TestWorkingMemory_SweepAndIsolation(t *testing.T) {
	fixed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	s := NewStore()
	s.SetClock(func() time.Time { return fixed })

	s.SetWorkingMemory("EMP-01", "k", "fresh", 5*time.Minute)
	s.SetWorkingMemory("EMP-02", "k2", "short", time.Second)

	// At +30s: EMP-02 short-TTL expired (and evicted on read), EMP-01 fresh.
	if _, ok := s.GetWorkingMemory("EMP-02", "k2", fixed.Add(30*time.Second)); ok {
		t.Fatalf("EMP-02 short-TTL entry must be expired at +30s")
	}
	if v, ok := s.GetWorkingMemory("EMP-01", "k", fixed.Add(30*time.Second)); !ok || v != "fresh" {
		t.Fatalf("EMP-01 entry must be fresh at +30s: v=%q ok=%v", v, ok)
	}

	s.SweepWorkingMemory(fixed.Add(30 * time.Second))
	if v, ok := s.GetWorkingMemory("EMP-01", "k", fixed.Add(2*time.Minute)); !ok || v != "fresh" {
		t.Fatalf("EMP-01 entry must survive sweep: v=%q ok=%v", v, ok)
	}
	// Cross-employee isolation: EMP-02's key is not visible under EMP-01.
	if _, ok := s.GetWorkingMemory("EMP-01", "k2", fixed); ok {
		t.Fatalf("EMP-01 must not see EMP-02 working memory")
	}
}

func TestStore_ListByPosition(t *testing.T) {
	s := NewStore()
	_, _ = s.Create(candidate(func(c *MemoryCandidate) { c.PositionID = "SWE" })) // EMP-01
	_, _ = s.Create(candidate(func(c *MemoryCandidate) {
		c.ID = "c2"
		c.EmployeeID = "EMP-02"
		c.AuthorID = "EMP-02"
		c.PositionID = "SWE"
	})) // EMP-02, same position
	_, _ = s.Create(candidate(func(c *MemoryCandidate) { c.ID = "c3"; c.PositionID = "QA" })) // different position

	if got := s.ListByPosition("SWE"); len(got) != 2 {
		t.Fatalf("SWE list len = %d, want 2", len(got))
	}
	if got := s.ListByPosition("QA"); len(got) != 1 || got[0].ID != "c3" {
		t.Fatalf("QA list = %+v", got)
	}
}

func TestStore_PromotedProjection(t *testing.T) {
	s := NewStore()
	_, _ = s.Create(candidate(nil)) // EMP-01
	_, _ = s.ValidateCandidate("c1")
	_, _ = s.Promote("c1", TargetSkill, "REV-01", true, "verified skill")

	if got := s.Promoted(TargetSkill); len(got) != 1 || got[0].ID != "c1" {
		t.Fatalf("Promoted(skill) = %+v", got)
	}
	if got := s.Promoted(TargetTeamPlaybook); len(got) != 0 {
		t.Fatalf("Promoted(team_playbook) should be empty, got %+v", got)
	}

	// Revoke -> the skill projection must no longer include it.
	_, _ = s.Revoke("c1", "wrong skill", "REV-02")
	if got := s.Promoted(TargetSkill); len(got) != 0 {
		t.Fatalf("revoked skill must drop from Promoted projection, got %+v", got)
	}
}
