package workentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
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

// ArtifactCandidateInput connects the kernel's completion candidate to the
// existing artifact_candidate machinery (which flows into review/promotion/
// outcome center). LineageID is the issue the work was registered under.
type ArtifactCandidateInput struct {
	WorkspaceID string
	LineageID   string
	Revision    int32
	StorageKey  string
	DurableRef  string
	Digest      string
	Filename    string
	ContentType string
	IdempotencyKey string
}

// CreateArtifactCandidate inserts a candidate artifact row (reused existing
// table; zero migration). Idempotent on (workspace_id, idempotency_key).
func (p *PGStore) CreateArtifactCandidate(ctx context.Context, in ArtifactCandidateInput) error {
	ws, err := p.uuid(in.WorkspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	lineage, err := p.uuid(in.LineageID)
	if err != nil {
		return ErrInvalidRequest
	}
	_, err = p.queries.InsertArtifactCandidate(ctx, db.InsertArtifactCandidateParams{
		ID:               pgtype.UUID{Bytes: uuid.New(), Valid: true},
		WorkspaceID:      ws,
		LineageID:        lineage,
		Revision:         in.Revision,
		StorageKey:       in.StorageKey,
		DurableObjectRef: in.DurableRef,
		Digest:           in.Digest,
		Filename:         in.Filename,
		ContentType:      in.ContentType,
		IdempotencyKey:   in.IdempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("insert artifact candidate: %w", err)
	}
	return nil
}

// artifactCandidateCreator is the optional capability the Finish path uses to
// persist a candidate artifact into the existing machinery.
type artifactCandidateCreator interface {
	CreateArtifactCandidate(ctx context.Context, in ArtifactCandidateInput) error
}


// escapeLike escapes LIKE wildcards so a caller-supplied query is matched
// literally (F10). PostgreSQL's default LIKE escape character is backslash.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// nullString returns nil for an empty string (nullable text column) and the
// string itself otherwise.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// parseTimestamptz parses an RFC3339 string into a nullable pgtype.Timestamptz.
// An empty string yields an invalid (NULL) value.
func parseTimestamptz(s string) (pgtype.Timestamptz, error) {
	var t pgtype.Timestamptz
	if strings.TrimSpace(s) == "" {
		return t, nil
	}
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return t, err
	}
	t.Time = tm
	t.Valid = true
	return t, nil
}

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
	pattern := "%" + escapeLike(strings.ToLower(q)) + "%"
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
		projectID  pgtype.UUID
		issueID    pgtype.UUID
		taskID     pgtype.UUID
		workRef    string
		idemKey    string
		digest     string
		decision   string
		actorJSON  []byte
		intentJSON []byte
	)
	err = exec.QueryRow(ctx,
		"SELECT work_ref, dedupe_key, payload_digest, project_id, issue_id, task_id, decision, actor, intent FROM work_registration_receipt WHERE workspace_id = $1 AND dedupe_key = $2",
		ws, dedupeKey).Scan(&workRef, &idemKey, &digest, &projectID, &issueID, &taskID, &decision, &actorJSON, &intentJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read work registration receipt: %w", err)
	}
	var actor WorkActorIdentityV1
	var intent WorkIntentV1
	_ = json.Unmarshal(actorJSON, &actor)
	_ = json.Unmarshal(intentJSON, &intent)
	return &ReceiptRecord{
		WorkspaceID: workspaceID,
		DedupeKey:   idemKey,
		Digest:      digest,
		WorkRef:     workRef,
		ProjectID:   util.UUIDToString(projectID),
		IssueID:     util.UUIDToString(issueID),
		TaskID:      util.UUIDToString(taskID),
		Decision:    ResolutionDecision(decision),
		Actor:       actor,
		Intent:      intent,
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
	ws, err := p.uuid(receipt.WorkspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	var projectID, issueID, taskID pgtype.UUID
	if receipt.ProjectID != "" {
		if projectID, err = p.uuid(receipt.ProjectID); err != nil {
			return ErrInvalidRequest
		}
	}
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
	actorJSON, err := json.Marshal(receipt.Actor)
	if err != nil {
		return fmt.Errorf("encode actor snapshot: %w", err)
	}
	intentJSON, err := json.Marshal(receipt.Intent)
	if err != nil {
		return fmt.Errorf("encode intent snapshot: %w", err)
	}
	_, err = exec.Exec(ctx,
		"INSERT INTO work_registration_receipt (workspace_id, work_ref, dedupe_key, payload_digest, project_id, issue_id, task_id, decision, actor, intent, applied, replayed) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,false) ON CONFLICT (workspace_id, dedupe_key) DO NOTHING",
		ws, receipt.WorkRef, receipt.DedupeKey, receipt.Digest, projectID, issueID, taskID, string(receipt.Decision), actorJSON, intentJSON)
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
		"SELECT work_ref, dedupe_key, payload_digest, project_id, issue_id, task_id, decision, actor, intent FROM work_registration_receipt WHERE workspace_id = $1 AND work_ref = $2",
		ws, workRef)
	if err != nil {
		return nil, fmt.Errorf("scan work registration receipts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var storedRef, idemKey, digest, decision string
		var projectID, issueID, taskID pgtype.UUID
		var actorJSON, intentJSON []byte
		if err := rows.Scan(&storedRef, &idemKey, &digest, &projectID, &issueID, &taskID, &decision, &actorJSON, &intentJSON); err != nil {
			return nil, err
		}
		if storedRef != workRef {
			continue
		}
		var actor WorkActorIdentityV1
		var intent WorkIntentV1
		_ = json.Unmarshal(actorJSON, &actor)
		_ = json.Unmarshal(intentJSON, &intent)
		rec := &ReceiptRecord{
			WorkspaceID: workspaceID, DedupeKey: idemKey, Digest: digest, WorkRef: storedRef,
			ProjectID: util.UUIDToString(projectID), IssueID: util.UUIDToString(issueID),
			TaskID: util.UUIDToString(taskID), Decision: ResolutionDecision(decision),
			Actor: actor, Intent: intent,
		}
		if rec.WorkRef == workRef {
			return rec, nil
		}
	}
	return nil, rows.Err()
}

// ListProjectParticipants reads the receipt ledger for one project (workspace-
// scoped) and projects each actor identity into the VC-04 participant read
// model. Read-only; the receipt table's reject-mutation trigger keeps this
// append-only.
func (p *PGStore) ListProjectParticipants(ctx context.Context, workspaceID, projectID string) ([]ProjectParticipant, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	prj, err := p.uuid(projectID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	rows, err := p.exec.Query(ctx,
		"SELECT actor, task_id FROM work_registration_receipt WHERE workspace_id = $1 AND project_id = $2 ORDER BY created_at",
		ws, prj)
	if err != nil {
		return nil, fmt.Errorf("list project participants: %w", err)
	}
	defer rows.Close()
	var out []ProjectParticipant
	for rows.Next() {
		var actorJSON []byte
		var taskID pgtype.UUID
		if err := rows.Scan(&actorJSON, &taskID); err != nil {
			return nil, err
		}
		var actor WorkActorIdentityV1
		_ = json.Unmarshal(actorJSON, &actor)
		out = append(out, ProjectParticipant{
			ActorType:  actor.ActorType,
			ActorID:    actor.ActorID,
			EmployeeID: actor.EmployeeID,
			CarrierID:  actor.CarrierID,
			RuntimeID:  actor.RuntimeID,
			ModelRef:   actor.ModelRef,
			BaseID:     actor.BaseID,
			HostID:     actor.HostID,
			SessionID:  actor.SessionID,
			TaskID:     util.UUIDToString(taskID),
		})
	}
	return out, rows.Err()
}

func (p *PGStore) AppendEvent(ctx context.Context, event EventRecord) (*EventRecord, error) {
	ws, err := p.uuid(event.WorkspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	payloadJSON, err := json.Marshal(event.EventPayload)
	if err != nil {
		return nil, fmt.Errorf("encode event payload: %w", err)
	}
	occurredAt, err := parseTimestamptz(event.OccurredAt)
	if err != nil {
		return nil, fmt.Errorf("parse occurred_at: %w", err)
	}
	observedAt, err := parseTimestamptz(event.ObservedAt)
	if err != nil {
		return nil, fmt.Errorf("parse observed_at: %w", err)
	}
	_, err = p.exec.Exec(ctx,
		"INSERT INTO work_event (workspace_id, work_ref, session_id, run_id, event_type, event_payload, blocker_reason, receiver, idempotency_key, occurred_at, observed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (workspace_id, work_ref, idempotency_key) DO NOTHING",
		ws, event.WorkRef, nullString(event.SessionID), nullString(event.RunID), string(event.EventType),
		payloadJSON, nullString(event.BlockerReason), nullString(event.Receiver), event.IdempotencyKey, occurredAt, observedAt)
	if err != nil {
		return nil, fmt.Errorf("append work event: %w", err)
	}
	stored, err := p.GetEvent(ctx, event.WorkspaceID, event.WorkRef, event.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, fmt.Errorf("work event insert did not persist")
	}
	if !eventEqual(stored, &event) {
		return nil, ErrConflict
	}
	return stored, nil
}

func (p *PGStore) GetEvent(ctx context.Context, workspaceID, workRef, idempotencyKey string) (*EventRecord, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	var (
		rec           EventRecord
		eventType     string
		payloadJSON   []byte
		sessionID     *string
		runID         *string
		blockerReason *string
		receiver      *string
		occurredAt    pgtype.Timestamptz
		observedAt    pgtype.Timestamptz
	)
	err = p.exec.QueryRow(ctx,
		"SELECT work_ref, session_id, run_id, event_type, event_payload, blocker_reason, receiver, idempotency_key, occurred_at, observed_at FROM work_event WHERE workspace_id = $1 AND work_ref = $2 AND idempotency_key = $3",
		ws, workRef, idempotencyKey).Scan(&rec.WorkRef, &sessionID, &runID, &eventType,
		&payloadJSON, &blockerReason, &receiver, &rec.IdempotencyKey, &occurredAt, &observedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read work event: %w", err)
	}
	rec.WorkspaceID = workspaceID
	rec.EventType = WorkEventType(eventType)
	_ = json.Unmarshal(payloadJSON, &rec.EventPayload)
	if sessionID != nil {
		rec.SessionID = *sessionID
	}
	if runID != nil {
		rec.RunID = *runID
	}
	if blockerReason != nil {
		rec.BlockerReason = *blockerReason
	}
	if receiver != nil {
		rec.Receiver = *receiver
	}
	return &rec, nil
}

func (p *PGStore) UpsertHeartbeat(ctx context.Context, hb HeartbeatRecord) error {
	ws, err := p.uuid(hb.WorkspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	host := sanitizeHeartbeatField(hb.Host, 255)
	sessionName := sanitizeHeartbeatField(hb.SessionName, 255)
	if host == "" || sessionName == "" {
		return ErrInvalidRequest
	}
	rows, err := p.queries.UpsertTerminalPresence(ctx, db.UpsertTerminalPresenceParams{
		WorkspaceID:    ws,
		Host:           host,
		SessionName:    sessionName,
		WindowIndex:    int32(hb.WindowIndex),
		PaneIndex:      int32(hb.PaneIndex),
		PanePid:        0,
		CurrentCommand: sanitizeHeartbeatField(hb.CurrentCommand, 120),
		AgentHint:      sanitizeHeartbeatField(hb.ActorID, 120),
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrConflict
	}
	return nil
}

var heartbeatANSIPattern = regexp.MustCompile(`(?:\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))|\x9b[0-?]*[ -/]*[@-~]|\x9d[^\x07\x9c]*(?:\x07|\x9c))`)

func sanitizeHeartbeatField(value string, maxRunes int) string {
	value = heartbeatANSIPattern.ReplaceAllString(value, "")
	var clean strings.Builder
	for _, ch := range value {
		if !unicode.IsControl(ch) {
			clean.WriteRune(ch)
		}
	}
	value = strings.TrimSpace(redact.Text(clean.String()))
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value
}

func (p *PGStore) SaveHandoff(ctx context.Context, h HandoffRecord) error {
	return p.saveDocument(ctx, h.WorkspaceID, h.WorkRef, "handoff", h.Package, false)
}

func (p *PGStore) SaveCompletion(ctx context.Context, c CompletionRecord) error {
	return p.saveDocument(ctx, c.WorkspaceID, c.WorkRef, "completion", c.Package, c.RoutedToReview)
}

func (p *PGStore) saveDocument(ctx context.Context, workspaceID, workRef, kind string, pkg any, routed bool) error {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	jsonPkg, err := json.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("encode work document: %w", err)
	}
	_, err = p.exec.Exec(ctx,
		"INSERT INTO work_document (workspace_id, work_ref, kind, package, routed_to_review) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (workspace_id, work_ref, kind) DO NOTHING",
		ws, workRef, kind, jsonPkg, routed)
	if err != nil {
		return fmt.Errorf("save work document: %w", err)
	}
	return nil
}

// InboxUpsert is one discovered-but-unregistered work entry to persist into
// the inbox (VC-05 discovery source). WorkRef is a synthetic identifier
// ("unregistered:<path>") until the entry is attached to a real project/issue.
type InboxUpsert struct {
	WorkRef string
	Path    string
	Branch  string
	Head    string
	Reason  string
}

// inboxUpserter is the optional capability the reconcile path uses to persist
// discovered unregistered work into the inbox.
type inboxUpserter interface {
	UpsertInboxItem(ctx context.Context, workspaceID string, item InboxUpsert) error
}

// RecordArtifactEvent persists a review verdict into the artifact_event ledger,
// looking up the candidate by lineage and computing the next sequence.
func (p *PGStore) RecordArtifactEvent(ctx context.Context, in ArtifactEventInput) error {
	ws, err := p.uuid(in.WorkspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	lineage, err := p.uuid(in.LineageID)
	if err != nil {
		return ErrInvalidRequest
	}
	// Look up the candidate by lineage to bind the verdict to its digest/ref.
	cands, err := p.queries.ListArtifactCandidatesByLineage(ctx, db.ListArtifactCandidatesByLineageParams{
		WorkspaceID: ws, LineageID: lineage,
	})
	if err != nil {
		return fmt.Errorf("lookup artifact candidate: %w", err)
	}
	if len(cands) == 0 {
		return ErrNotFound
	}
	cand := cands[0]
	var seq int32
	if err := p.exec.QueryRow(ctx,
		"SELECT COALESCE(MAX(sequence), 0) + 1 FROM artifact_event WHERE workspace_id = $1 AND lineage_id = $2",
		ws, lineage).Scan(&seq); err != nil {
		return fmt.Errorf("compute artifact event sequence: %w", err)
	}
	_, err = p.queries.InsertArtifactEvent(ctx, db.InsertArtifactEventParams{
		ID:                 pgtype.UUID{Bytes: uuid.New(), Valid: true},
		WorkspaceID:        ws,
		LineageID:          lineage,
		Sequence:           seq,
		EventType:          in.EventType,
		CandidateID:        cand.ID,
		CandidateRevision:  1,
		CandidateDigest:    cand.Digest,
		CandidateObjectRef: cand.DurableObjectRef,
		IdempotencyKey:     in.IdempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("insert artifact event: %w", err)
	}
	return nil
}

// UpsertInboxItem persists one unregistered work entry idempotently by
// (workspace_id, path).
func (p *PGStore) UpsertInboxItem(ctx context.Context, workspaceID string, item InboxUpsert) error {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	_, err = p.exec.Exec(ctx,
		"INSERT INTO work_inbox (workspace_id, work_ref, path, branch, head, reason) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (workspace_id, path) DO NOTHING",
		ws, item.WorkRef, nullString(item.Path), nullString(item.Branch), nullString(item.Head), nullString(item.Reason))
	if err != nil {
		return fmt.Errorf("upsert work inbox: %w", err)
	}
	return nil
}

func (p *PGStore) ListInbox(ctx context.Context, workspaceID string) ([]InboxItem, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	rows, err := p.exec.Query(ctx,
		"SELECT id, COALESCE(work_ref, '') FROM work_inbox WHERE workspace_id = $1 AND state = 'unclaimed' ORDER BY created_at",
		ws)
	if err != nil {
		return nil, fmt.Errorf("list work inbox: %w", err)
	}
	defer rows.Close()
	var out []InboxItem
	for rows.Next() {
		var id pgtype.UUID
		var ref string
		if err := rows.Scan(&id, &ref); err != nil {
			return nil, err
		}
		out = append(out, InboxItem{ID: util.UUIDToString(id), WorkspaceID: workspaceID, WorkRef: ref})
	}
	return out, rows.Err()
}

func (p *PGStore) AttachInbox(ctx context.Context, workspaceID, inboxID, projectID, issueID string) error {
	return p.setInboxState(ctx, workspaceID, inboxID, "attached", projectID, issueID)
}

func (p *PGStore) IgnoreInbox(ctx context.Context, workspaceID, inboxID, reason string) error {
	return p.setInboxState(ctx, workspaceID, inboxID, "ignored", "", "")
}

func (p *PGStore) setInboxState(ctx context.Context, workspaceID, inboxID, state, projectID, issueID string) error {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	id, err := p.uuid(inboxID)
	if err != nil {
		return ErrInvalidRequest
	}
	var projectUUID, issueUUID pgtype.UUID
	if projectID != "" {
		if projectUUID, err = p.uuid(projectID); err != nil {
			return ErrInvalidRequest
		}
	}
	if issueID != "" {
		if issueUUID, err = p.uuid(issueID); err != nil {
			return ErrInvalidRequest
		}
	}
	_, err = p.exec.Exec(ctx,
		"UPDATE work_inbox SET state = $3, project_id = $4, issue_id = $5 WHERE workspace_id = $1 AND id = $2 AND state = 'unclaimed'",
		ws, id, state, projectUUID, issueUUID)
	if err != nil {
		return fmt.Errorf("update work inbox: %w", err)
	}
	return nil
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
