package workentry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// pgExecutor is the raw-SQL seam the PG store uses for tables that have no
// generated query yet (project_lifecycle_receipt). *pgxpool.Pool satisfies it.
// ExternalActorCreatorID is the deterministic sentinel issue creator UUID for
// external_agent / automation_service / observed_unclaimed_actor actors that
// have no agent row. issue.creator_id has no FK and creator_type CHECK only
// allows member|agent, so this sentinel keeps the zero-migration path valid
// without impersonating a registered employee. A real work_actor→creator
// mapping is deferred to the ≥400 migration join.
var ExternalActorCreatorID = pgtype.UUID{Bytes: [16]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, Valid: true}

type pgExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGStore implements Store against the existing HiveCrew tables:
//
//	resolve:  external_work_order_link, project, issue, ILIKE similarity
//	register: project_lifecycle_receipt idempotency anchor + project/issue reuse
//	heartbeat: terminal_presence upsert
//
// Events/handoff/finish/inbox persistence return ErrUnavailable in this slice
// because no reusable table exists without either crossing the workflow
// boundary or adding a migration (deferred to P1-join, ≥400).
type PGStore struct {
	queries *db.Queries
	exec    pgExecutor
	pool    *pgxpool.Pool
}

// NewPGStore binds the generated queries, the raw executor, and the pool used
// to begin the register-path transaction. The pool also satisfies pgExecutor.
func NewPGStore(queries *db.Queries, pool *pgxpool.Pool) *PGStore {
	return &PGStore{queries: queries, exec: pool, pool: pool}
}

func (p *PGStore) uuid(s string) (pgtype.UUID, error) { return util.ParseUUID(s) }

func (p *PGStore) LookupWorkOrder(ctx context.Context, workspaceID, workOrderRef string) (*ExternalWorkOrderLink, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	row, err := p.queries.GetExternalWorkOrderLink(ctx, db.GetExternalWorkOrderLinkParams{
		WorkspaceID: ws, WorkOrderRef: workOrderRef,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup external work order link: %w", err)
	}
	return &ExternalWorkOrderLink{
		WorkspaceID:    util.UUIDToString(row.WorkspaceID),
		WorkOrderRef:   row.WorkOrderRef,
		LinkedRevision: row.LinkedRevision,
		LinkedDigest:   row.LinkedDigest,
		IssueID:        util.UUIDToString(row.IssueID),
	}, nil
}

// PutWorkOrderLink reuses external_work_order_link through the store's default
// (non-transactional) query handle.
func (p *PGStore) PutWorkOrderLink(ctx context.Context, link ExternalWorkOrderLink) error {
	return p.putWorkOrderLink(ctx, p.queries, link)
}

// putWorkOrderLink inserts the WorkOrder→Issue projection link through the
// given query handle (pool or transaction). ON CONFLICT DO NOTHING makes it
// idempotent; a pre-existing link with a different revision/digest returns
// ErrConflict so the register path can treat it as a best-effort anchor.
func (p *PGStore) putWorkOrderLink(ctx context.Context, q *db.Queries, link ExternalWorkOrderLink) error {
	ws, err := p.uuid(link.WorkspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	issueID, err := p.uuid(link.IssueID)
	if err != nil {
		return ErrInvalidRequest
	}
	_, err = q.InsertExternalWorkOrderLink(ctx, db.InsertExternalWorkOrderLinkParams{
		WorkspaceID:      ws,
		WorkOrderRef:     link.WorkOrderRef,
		LinkedRevision:   link.LinkedRevision,
		LinkedDigest:     link.LinkedDigest,
		SourceObservedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		FreshnessAtLink:  "current",
		IssueID:          issueID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING returned no row: the link already exists.
		existing, err := q.GetExternalWorkOrderLink(ctx, db.GetExternalWorkOrderLinkParams{
			WorkspaceID: ws, WorkOrderRef: link.WorkOrderRef,
		})
		if err != nil {
			return fmt.Errorf("read existing external work order link: %w", err)
		}
		if existing.LinkedDigest != link.LinkedDigest || existing.LinkedRevision != link.LinkedRevision {
			return ErrConflict
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("insert external work order link: %w", err)
	}
	return nil
}

func (p *PGStore) LookupProject(ctx context.Context, workspaceID, projectID string) (*ProjectRef, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	id, err := p.uuid(projectID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	row, err := p.queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup project: %w", err)
	}
	return &ProjectRef{ID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID), Title: row.Title, Status: row.Status}, nil
}

func (p *PGStore) LookupIssue(ctx context.Context, workspaceID, issueID string) (*IssueRef, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	id, err := p.uuid(issueID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	row, err := p.queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup issue: %w", err)
	}
	return &IssueRef{ID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID), Title: row.Title, Status: row.Status, ProjectID: util.UUIDToString(row.ProjectID)}, nil
}

// LookupRepoRevisionBranch has no reusable table/index without a new migration;
// it always reports no match in this slice.
func (p *PGStore) LookupRepoRevisionBranch(_ context.Context, _, _, _, _ string) (*RepoMatch, error) {
	return nil, nil
}

func (p *PGStore) SearchSimilar(ctx context.Context, workspaceID, query string, limit int) ([]SimilarMatch, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	pattern := "%" + strings.ToLower(q) + "%"
	var out []SimilarMatch
	appendRows := func(rows pgx.Rows, kind string) error {
		defer rows.Close()
		for rows.Next() {
			var id pgtype.UUID
			var title string
			if err := rows.Scan(&id, &title); err != nil {
				return err
			}
			out = append(out, SimilarMatch{
				Kind: kind, RefID: util.UUIDToString(id), Title: title,
				WorkspaceID: workspaceID, Similarity: similarityScore(title, q),
			})
		}
		return rows.Err()
	}
	rows, err := p.exec.Query(ctx,
		"SELECT id, title FROM project WHERE workspace_id = $1 AND LOWER(title) LIKE $2 ORDER BY title LIMIT $3",
		ws, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search similar projects: %w", err)
	}
	if err := appendRows(rows, "project"); err != nil {
		return nil, fmt.Errorf("scan similar projects: %w", err)
	}
	rows2, err := p.exec.Query(ctx,
		"SELECT id, title FROM issue WHERE workspace_id = $1 AND LOWER(title) LIKE $2 ORDER BY title LIMIT $3",
		ws, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search similar issues: %w", err)
	}
	if err := appendRows(rows2, "issue"); err != nil {
		return nil, fmt.Errorf("scan similar issues: %w", err)
	}
	return out, nil
}

// workRegisterAction encodes the decision suffix into the receipt action value
// so the anchor round-trips through project_lifecycle_receipt without a new
// column.
const workRegisterActionPrefix = "work_register:"

// GetReceipt reads the registration receipt through the store's default
// (non-transactional) handle.
func (p *PGStore) GetReceipt(ctx context.Context, workspaceID, dedupeKey string) (*ReceiptRecord, error) {
	return p.getReceipt(ctx, p.exec, workspaceID, dedupeKey)
}

// getReceipt is the executor-parameterized receipt reader used by both the
// pool-backed GetReceipt and the transaction-backed register path.
func (p *PGStore) getReceipt(ctx context.Context, exec pgExecutor, workspaceID, dedupeKey string) (*ReceiptRecord, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	var (
		id            pgtype.UUID
		projectID     pgtype.UUID
		action        string
		idemKey       string
		payloadDigest string
		taskID        pgtype.UUID
		issueID       pgtype.UUID
	)
	err = exec.QueryRow(ctx,
		"SELECT id, project_id, action, idempotency_key, payload_digest, task_id, issue_id FROM project_lifecycle_receipt WHERE workspace_id = $1 AND idempotency_key = $2 AND action LIKE $3 ORDER BY created_at DESC LIMIT 1",
		ws, dedupeKey, workRegisterActionPrefix+"%").Scan(&id, &projectID, &action, &idemKey, &payloadDigest, &taskID, &issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read work registration receipt: %w", err)
	}
	decision := DecisionContinued
	switch strings.TrimPrefix(action, workRegisterActionPrefix) {
	case "created":
		decision = DecisionCreated
	case "continued":
		decision = DecisionContinued
	default:
		decision = DecisionContinued
	}
	projectStr, issueStr, taskStr := util.UUIDToString(projectID), util.UUIDToString(issueID), util.UUIDToString(taskID)
	return &ReceiptRecord{
		WorkspaceID: workspaceID,
		DedupeKey:   idemKey,
		Digest:      payloadDigest,
		// WorkRef is derived, not stored; recompute it from the persisted lineage
		// so replay returns the exact same work_ref (VC-03). The actor/intent
		// snapshot is NOT recoverable from project_lifecycle_receipt — that is a
		// known gap deferred to the ≥400 work_registration_receipt join.
		WorkRef:   FormatWorkRef(workspaceID, projectStr, issueStr, taskStr),
		ProjectID: projectStr,
		IssueID:   issueStr,
		TaskID:    taskStr,
		Decision:  decision,
	}, nil
}

// PutReceipt writes the receipt anchor through the store's default
// (non-transactional) handle. Used by the continued path, where no project/
// issue row is created so a single INSERT is already atomic.
func (p *PGStore) PutReceipt(ctx context.Context, receipt ReceiptRecord) error {
	return p.putReceipt(ctx, p.exec, receipt)
}

// putReceipt is the executor-parameterized receipt writer used by both the
// pool-backed PutReceipt and the transaction-backed register path. It returns
// ErrConflict when the same dedupe key already holds a different digest.
func (p *PGStore) putReceipt(ctx context.Context, exec pgExecutor, receipt ReceiptRecord) error {
	// project_lifecycle_receipt requires project_id NOT NULL. For a continued
	// work with no project, the existing object (issue/link) is the anchor;
	// no receipt row is written in that case.
	if strings.TrimSpace(receipt.ProjectID) == "" {
		return nil
	}
	ws, err := p.uuid(receipt.WorkspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	projectID, err := p.uuid(receipt.ProjectID)
	if err != nil {
		return ErrInvalidRequest
	}
	action := workRegisterActionPrefix + string(receipt.Decision)
	var issueID, taskID pgtype.UUID
	if receipt.IssueID != "" {
		if issueID, err = p.uuid(receipt.IssueID); err != nil {
			return ErrInvalidRequest
		}
	}
	if receipt.TaskID != "" {
		if taskID, err = p.uuid(receipt.TaskID); err != nil {
			return ErrInvalidRequest
		}
	}
	_, err = exec.Exec(ctx,
		"INSERT INTO project_lifecycle_receipt (workspace_id, project_id, action, idempotency_key, payload_digest, before_status, after_status, task_id, issue_id, blockers, applied, replayed) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'[]'::jsonb,true,false) ON CONFLICT (workspace_id, idempotency_key) DO NOTHING",
		ws, projectID, action, receipt.DedupeKey, receipt.Digest, "", "", taskID, issueID)
	if err != nil {
		return fmt.Errorf("insert work registration receipt: %w", err)
	}
	// Re-read to detect a same-key/different-digest conflict.
	stored, err := p.getReceipt(ctx, exec, receipt.WorkspaceID, receipt.DedupeKey)
	if err != nil {
		return err
	}
	if stored == nil {
		// A concurrent conflicting insert may have been skipped; re-read once.
		stored, err = p.getReceipt(ctx, exec, receipt.WorkspaceID, receipt.DedupeKey)
		if err != nil {
			return err
		}
	}
	if stored != nil && stored.Digest != receipt.Digest {
		return ErrConflict
	}
	return nil
}

func (p *PGStore) FindReceiptByWorkRef(ctx context.Context, workspaceID, workRef string) (*ReceiptRecord, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	rows, err := p.exec.Query(ctx,
		"SELECT idempotency_key, payload_digest, project_id, issue_id, task_id, action FROM project_lifecycle_receipt WHERE workspace_id = $1 AND action LIKE $2",
		ws, workRegisterActionPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("scan work registration receipts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var idemKey, digest, action string
		var projectID, issueID, taskID pgtype.UUID
		if err := rows.Scan(&idemKey, &digest, &projectID, &issueID, &taskID, &action); err != nil {
			return nil, err
		}
		decision := DecisionContinued
		if strings.HasSuffix(action, ":created") {
			decision = DecisionCreated
		}
		rec := &ReceiptRecord{
			WorkspaceID: workspaceID, DedupeKey: idemKey, Digest: digest,
			ProjectID: util.UUIDToString(projectID), IssueID: util.UUIDToString(issueID),
			TaskID: util.UUIDToString(taskID), Decision: decision,
		}
		rebuilt := FormatWorkRef(workspaceID, rec.ProjectID, rec.IssueID, rec.TaskID)
		if rebuilt == workRef {
			return rec, nil
		}
	}
	return nil, rows.Err()
}

func (p *PGStore) AppendEvent(_ context.Context, _ EventRecord) (*EventRecord, error) {
	return nil, ErrUnavailable
}

func (p *PGStore) GetEvent(_ context.Context, _, _, _ string) (*EventRecord, error) {
	return nil, ErrUnavailable
}

func (p *PGStore) UpsertHeartbeat(ctx context.Context, hb HeartbeatRecord) error {
	ws, err := p.uuid(hb.WorkspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	return p.queries.UpsertTerminalPresence(ctx, db.UpsertTerminalPresenceParams{
		WorkspaceID:    ws,
		Host:           hb.Host,
		SessionName:    hb.SessionName,
		WindowIndex:    int32(hb.WindowIndex),
		PaneIndex:      int32(hb.PaneIndex),
		PanePid:        0,
		CurrentCommand: hb.CurrentCommand,
		AgentHint:      hb.ActorID,
	})
}

func (p *PGStore) SaveHandoff(_ context.Context, _ HandoffRecord) error {
	return ErrUnavailable
}

func (p *PGStore) SaveCompletion(_ context.Context, _ CompletionRecord) error {
	return ErrUnavailable
}

func (p *PGStore) ListInbox(_ context.Context, _ string) ([]InboxItem, error) {
	return nil, ErrUnavailable
}

func (p *PGStore) AttachInbox(_ context.Context, _, _, _, _ string) error {
	return ErrUnavailable
}

func (p *PGStore) IgnoreInbox(_ context.Context, _, _, _ string) error {
	return ErrUnavailable
}

func (p *PGStore) CreateWork(ctx context.Context, req CreateWorkRequest) (*CreateWorkResult, error) {
	ws, err := p.uuid(req.WorkspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	projectID := req.ProjectID
	if projectID == "" {
		project, err := p.queries.CreateProject(ctx, db.CreateProjectParams{
			WorkspaceID: ws,
			Title:       req.Title,
			Description: pgtype.Text{String: req.Description, Valid: req.Description != ""},
			Status:      "planned",
			Priority:    "none",
		})
		if err != nil {
			return nil, fmt.Errorf("create project: %w", err)
		}
		projectID = util.UUIDToString(project.ID)
	}
	pid, err := p.uuid(projectID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	number, err := p.queries.IncrementIssueCounter(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("allocate issue number: %w", err)
	}
	issue, err := p.queries.CreateIssue(ctx, db.CreateIssueParams{
		WorkspaceID: ws,
		Title:       req.Title,
		Description: pgtype.Text{String: req.Description, Valid: req.Description != ""},
		Status:      "todo",
		Priority:    "none",
		CreatorType: "agent",
		// issue.creator_id is NOT NULL (no FK) and issue_creator_type_check only
		// allows member|agent. External/unclaimed actors have no agent row, so we
		// use the documented sentinel creator UUID for the first slice; a proper
		// work_actor → creator mapping is deferred to the ≥400 join.
		CreatorID: ExternalActorCreatorID,
		Number:    number,
		ProjectID: pid,
	})
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	return &CreateWorkResult{ProjectID: projectID, IssueID: util.UUIDToString(issue.ID)}, nil
}

// CommitWorkRegistration persists the created-path projection in one PostgreSQL
// transaction: create project → allocate issue number → create issue →
// (optional external_work_order_link) → write receipt. Any failure rolls the
// whole transaction back so no orphan project/issue/receipt survives (zero
// partial writes). A same-key/different-digest receipt conflict surfaces as
// ErrConflict and rolls back this attempt's project/issue.
func (p *PGStore) CommitWorkRegistration(ctx context.Context, req CommitWorkRegistrationRequest) (*CreateWorkResult, error) {
	ws, err := p.uuid(req.WorkspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work registration transaction: %w", err)
	}
	defer tx.Rollback(context.Background())

	tq := p.queries.WithTx(tx)

	projectID := req.ProjectID
	if projectID == "" {
		project, err := tq.CreateProject(ctx, db.CreateProjectParams{
			WorkspaceID: ws,
			Title:       req.Title,
			Description: pgtype.Text{String: req.Description, Valid: req.Description != ""},
			Status:      "planned",
			Priority:    "none",
		})
		if err != nil {
			return nil, fmt.Errorf("create project: %w", err)
		}
		projectID = util.UUIDToString(project.ID)
	}
	pid, err := p.uuid(projectID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	number, err := tq.IncrementIssueCounter(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("allocate issue number: %w", err)
	}
	issue, err := tq.CreateIssue(ctx, db.CreateIssueParams{
		WorkspaceID: ws,
		Title:       req.Title,
		Description: pgtype.Text{String: req.Description, Valid: req.Description != ""},
		Status:      "todo",
		Priority:    "none",
		CreatorType: "agent",
		// issue.creator_id is NOT NULL (no FK) and issue_creator_type_check only
		// allows member|agent. External/unclaimed actors have no agent row, so we
		// use the documented sentinel creator UUID for the first slice; a proper
		// work_actor → creator mapping is deferred to the ≥400 join.
		CreatorID: ExternalActorCreatorID,
		Number:    number,
		ProjectID: pid,
	})
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	issueID := util.UUIDToString(issue.ID)

	receipt := req.Receipt
	receipt.WorkspaceID = req.WorkspaceID
	receipt.ProjectID = projectID
	receipt.IssueID = issueID
	receipt.TaskID = ""
	receipt.WorkRef = FormatWorkRef(req.WorkspaceID, projectID, issueID, "")

	if req.WorkOrderLink != nil {
		link := *req.WorkOrderLink
		link.WorkspaceID = req.WorkspaceID
		link.IssueID = issueID
		if err := p.putWorkOrderLink(ctx, tq, link); err != nil && !errors.Is(err, ErrConflict) {
			return nil, err
		}
		// A conflicting pre-existing link is best-effort (never blocks the
		// receipt), matching memory-store semantics.
	}

	if err := p.putReceipt(ctx, tx, receipt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work registration transaction: %w", err)
	}
	return &CreateWorkResult{ProjectID: projectID, IssueID: issueID}, nil
}
