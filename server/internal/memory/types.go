// Package memory implements the employee memory strict contract
// (HIVECREW-WORKFLOW-MEMORY-OS-V1, Phase 4). It covers only the HiveCrew-owned
// candidate layer: MemoryCandidate is per-employee execution state bound to
// Task/Run/Outcome evidence. Promotion to company knowledge / team playbook /
// skill is a PROPOSAL receipt — HiveCosm Knowledge/Harness authorities own the
// formal promotion; this package never writes those truths directly.
package memory

import "time"

// MemoryKind discriminates episodic (single-work) vs experience (multi-work)
// candidates.
type MemoryKind string

const (
	KindEpisodic   MemoryKind = "episodic"
	KindExperience MemoryKind = "experience"
)

// CandidateStatus is the closed candidate lifecycle.
type CandidateStatus string

const (
	StatusPending   CandidateStatus = "pending"
	StatusValidated CandidateStatus = "validated"
	StatusRejected  CandidateStatus = "rejected"
	StatusPromoted  CandidateStatus = "promoted"
	StatusRevoked   CandidateStatus = "revoked"
)

// PromotionTarget is what a validated candidate may be proposed into.
type PromotionTarget string

const (
	TargetEmployeeMemory PromotionTarget = "employee_memory"
	TargetTeamPlaybook   PromotionTarget = "team_playbook"
	TargetSkill          PromotionTarget = "skill"
)

// EvidenceRef binds a candidate to one authoritative work artifact.
type EvidenceRef struct {
	Type string // task | run | outcome
	ID   string
}

// MemoryCandidate is one evidence-bound memory candidate in an employee's
// namespace. Free-text reflection can only ever become a candidate; it is
// never company knowledge or a Skill until promoted through the authority.
type MemoryCandidate struct {
	ID         string
	EmployeeID string
	PositionID string // optional 岗位/role dimension for 岗位记忆 read-model
	Kind       MemoryKind
	Content    string
	Evidence   []EvidenceRef
	SourceRefs []string
	AuthorID   string
	CreatedAt  time.Time
	Status     CandidateStatus
}

// Revocation records the retraction of a promoted memory. Revoked memories are
// retained (append-only) but never returned by Retrieve.
type Revocation struct {
	CandidateID string
	ReviewerID  string
	Reason      string
	RevokedAt   time.Time
}

// WorkingMemory is short-lived per-employee context (工作记忆). It expires by
// TTL after the task ends and never becomes a candidate on its own.
type WorkingMemory struct {
	EmployeeID string
	Key        string
	Value      string
	ExpiresAt  time.Time
}

// MemoryPromotion is the promotion receipt (proposal), not the promoted truth.
type MemoryPromotion struct {
	CandidateID string
	Target      PromotionTarget
	ReviewerID  string
	Approved    bool
	Reason      string
	PromotedAt  time.Time
}
