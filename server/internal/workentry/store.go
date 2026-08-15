package workentry

import "context"

// Store is the persistence seam the kernel services depend on. A memory
// implementation backs unit tests and offline spool replay; a PostgreSQL
// implementation reuses the existing project/issue/agent_task_queue/
// external_work_order_link/project_lifecycle_receipt/terminal_presence tables
// without adding a second task/run table set.
//
// Lookup methods return (nil, nil) when nothing matches; they return a typed
// error only on a real failure.
type Store interface {
	// Read-only resolution selectors.
	LookupWorkOrder(ctx context.Context, workspaceID, workOrderRef string) (*ExternalWorkOrderLink, error)
	LookupProject(ctx context.Context, workspaceID, projectID string) (*ProjectRef, error)
	LookupIssue(ctx context.Context, workspaceID, issueID string) (*IssueRef, error)
	LookupRepoRevisionBranch(ctx context.Context, workspaceID, repo, revision, branch string) (*RepoMatch, error)
	SearchSimilar(ctx context.Context, workspaceID, query string, limit int) ([]SimilarMatch, error)

	// Receipt idempotency anchor. GetReceipt returns (nil, nil) when absent;
	// PutReceipt returns ErrConflict when the same key exists with a different
	// digest.
	GetReceipt(ctx context.Context, workspaceID, dedupeKey string) (*ReceiptRecord, error)
	PutReceipt(ctx context.Context, receipt ReceiptRecord) error
	FindReceiptByWorkRef(ctx context.Context, workspaceID, workRef string) (*ReceiptRecord, error)

	// PutWorkOrderLink reuses external_work_order_link for the WorkOrder→Issue
	// projection identity (step-1 exact-match anchor).
	PutWorkOrderLink(ctx context.Context, link ExternalWorkOrderLink) error

	// Event append-only ledger. AppendEvent is idempotent by
	// (work_ref, idempotency_key): same key + same payload returns the stored
	// event; different payload returns ErrConflict.
	AppendEvent(ctx context.Context, event EventRecord) (*EventRecord, error)
	GetEvent(ctx context.Context, workspaceID, workRef, idempotencyKey string) (*EventRecord, error)

	// Heartbeat/现场 presence.
	UpsertHeartbeat(ctx context.Context, hb HeartbeatRecord) error

	// Handoff / completion candidate persistence (candidate only, never
	// auto-pass).
	SaveHandoff(ctx context.Context, h HandoffRecord) error
	SaveCompletion(ctx context.Context, c CompletionRecord) error

	// Inbox reconcile / attach / ignore semantics.
	ListInbox(ctx context.Context, workspaceID string) ([]InboxItem, error)
	AttachInbox(ctx context.Context, workspaceID, inboxID, projectID, issueID string) error
	IgnoreInbox(ctx context.Context, workspaceID, inboxID, reason string) error

	// CreateWork creates the minimal project + issue projection in one logical
	// step, reusing the existing project/issue tables.
	CreateWork(ctx context.Context, req CreateWorkRequest) (*CreateWorkResult, error)

	// CommitWorkRegistration atomically projects an intent into project + issue
	// (+ optional external_work_order_link) and records the receipt anchor in a
	// single transaction (all-or-nothing; never leaves an orphan project/issue/
	// receipt). The service still performs the idempotency pre-check first, so a
	// same key+digest replay never enters this creation path.
	CommitWorkRegistration(ctx context.Context, req CommitWorkRegistrationRequest) (*CreateWorkResult, error)
}

// ExternalWorkOrderLink is the reused provenance link identity
// (external_work_order_link table).
type ExternalWorkOrderLink struct {
	WorkspaceID    string
	WorkOrderRef   string
	LinkedRevision string
	LinkedDigest   string
	IssueID        string
}

// ProjectRef is the minimal project projection used by resolution.
type ProjectRef struct {
	ID          string
	WorkspaceID string
	Title       string
	Status      string
}

// IssueRef is the minimal issue projection used by resolution.
type IssueRef struct {
	ID          string
	WorkspaceID string
	Title       string
	Status      string
	ProjectID   string
}

// RepoMatch is a repo+revision+branch/worktree exact match (no table exists
// without a new migration; the PG store returns nil).
type RepoMatch struct {
	WorkspaceID string
	ProjectID   string
	IssueID     string
	Repo        string
	Revision    string
	Branch      string
}

// SimilarMatch is a read-only historical similarity candidate.
type SimilarMatch struct {
	Kind        string  `json:"kind"`
	RefID       string  `json:"ref_id"`
	Title       string  `json:"title"`
	WorkspaceID string  `json:"workspace_id"`
	Similarity  float64 `json:"similarity"`
}

// ReceiptRecord is the persisted idempotency anchor for a registration.
type ReceiptRecord struct {
	WorkspaceID string
	DedupeKey   string
	Digest      string
	WorkRef     string
	ProjectID   string
	IssueID     string
	TaskID      string
	Decision    ResolutionDecision
	Actor       WorkActorIdentityV1
	Intent      WorkIntentV1
	CreatedAt   string
}

// EventRecord is the persisted append-only work event.
type EventRecord struct {
	ID             string
	WorkspaceID    string
	WorkRef        string
	SessionID      string
	RunID          string
	EventType      WorkEventType
	EventPayload   map[string]any
	BlockerReason  string
	Receiver       string
	IdempotencyKey string
	OccurredAt     string
	ObservedAt     string
	Sequence       int64
}

// HeartbeatRecord is the presence heartbeat payload.
type HeartbeatRecord struct {
	WorkspaceID    string
	ActorID        string
	SessionID      string
	Host           string
	SessionName    string
	WindowIndex    int
	PaneIndex      int
	CurrentCommand string
	AgentHint      string
	HeartbeatAt    string
}

// HandoffRecord is the persisted handoff package.
type HandoffRecord struct {
	WorkspaceID string
	WorkRef     string
	Package     WorkHandoffV1
}

// CompletionRecord is the persisted completion candidate.
type CompletionRecord struct {
	WorkspaceID    string
	WorkRef        string
	Package        WorkCompletionV1
	RoutedToReview bool
}

// InboxItem is an unclaimed work entry awaiting attach/ignore.
type InboxItem struct {
	ID          string
	WorkspaceID string
	WorkRef     string
}

// CreateWorkRequest carries the minimal inputs to project/issue an intent.
type CreateWorkRequest struct {
	WorkspaceID string
	Title       string
	Description string
	// ProjectID, when set, attaches the new issue to an existing project
	// instead of creating a new one.
	ProjectID string
	// IssueID, when set, means the issue already exists and only the receipt
	// must be recorded (continued path).
	IssueID string
}

// CreateWorkResult is the created/continued projection.
type CreateWorkResult struct {
	ProjectID string
	IssueID   string
}

// CommitWorkRegistrationRequest carries the created-path projection inputs and
// the receipt anchor so a store can persist them all-or-nothing. The store
// fills Receipt.ProjectID/IssueID/WorkRef from the concrete created lineage.
// WorkOrderLink is optional (nil when the intent carries no Goal/WorkOrder
// selector); a conflicting pre-existing link never blocks the receipt.
type CommitWorkRegistrationRequest struct {
	CreateWorkRequest
	Receipt       ReceiptRecord
	WorkOrderLink *ExternalWorkOrderLink
}
