package orcabridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Writeback contract errors. Each fails closed: an invalid, mismatched, or
// replayed-with-drift worker result is never appended as HiveCrew evidence.
var (
	// ErrNotWorkerDone means the observed message is not a worker_done.
	ErrNotWorkerDone = errors.New("orcabridge: message is not a worker_done result")
	// ErrInvalidWorkerResult means the worker_done payload failed validation.
	ErrInvalidWorkerResult = errors.New("orcabridge: invalid worker_done payload")
	// ErrUnmappedDispatch means no dispatch mapping exists for the Orca
	// dispatch the worker reported; HiveCrew never accepts results for runs
	// it did not authorize through the bridge.
	ErrUnmappedDispatch = errors.New("orcabridge: worker_done references an unmapped Orca dispatch")
	// ErrAmbiguousDispatch means more than one mapping claims the same Orca
	// dispatch id; control truth is ambiguous, so the writeback fails closed.
	ErrAmbiguousDispatch = errors.New("orcabridge: orca dispatch id maps to more than one HiveCrew workspace")
	// ErrResultIdentityMismatch means the worker_done identity fields do not
	// match the mapping frozen at dispatch time.
	ErrResultIdentityMismatch = errors.New("orcabridge: worker_done identity does not match the mapped dispatch")
	// ErrResultReceiptConflict means this dispatch already has committed
	// governed evidence with a different immutable result digest.
	ErrResultReceiptConflict = errors.New("orcabridge: worker_done replay conflicts with the committed result evidence")
	// ErrEvidenceConflict means the existing work-entry evidence for the same
	// idempotency key carries a different payload.
	ErrEvidenceConflict = errors.New("orcabridge: work-entry evidence conflict")
	// ErrMappingUnknown means the dispatch linkage is not (yet) known to this
	// bridge call and cannot authorize a writeback.
	ErrMappingUnknown = errors.New("orcabridge: no known dispatch linkage for the reported dispatch")
)

// WorkerOutcomes are the only terminal outcomes the contract accepts, per
// the Orca worker_done lifecycle.
var WorkerOutcomes = map[string]bool{"succeeded": true, "failed": true}

// WorkerResult is the normalized worker_done payload carried inside the
// Orca message `payload` JSON string.
type WorkerResult struct {
	TaskID        string   `json:"taskId"`
	DispatchID    string   `json:"dispatchId"`
	Outcome       string   `json:"outcome"`
	FilesModified []string `json:"filesModified"`
	ReportPath    string   `json:"reportPath"`
}

// ResultReceipt is the governed HiveCrew-side writeback of one worker_done:
// the Orca execution evidence mapped back onto the HiveCrew chain. It is
// append-only evidence for the HiveCrew execution lifecycle; it never mutates
// authority-owned company truth and never mutates Orca state.
type ResultReceipt struct {
	ID             string
	WorkspaceID    string
	ProjectID      string
	IssueID        string
	TaskID         string
	AssignmentID   string
	OrcaRunID      string
	OrcaTaskID     string
	OrcaDispatchID string
	Outcome        string
	Subject        string
	Body           string
	FilesModified  []string
	ReportPath     string
	ResultDigest   string
	OrcaMessageID  string
	WorkerTerminal string
	ObservedAt     time.Time
	CreatedAt      time.Time
}

// ParseWorkerResult normalizes one Orca message into a validated worker
// result. It accepts the payload as either a JSON-encoded string (the
// observed Orca message shape) or an embedded JSON object.
func ParseWorkerResult(message OrcaMessage) (WorkerResult, error) {
	if message.Type != "worker_done" {
		return WorkerResult{}, fmt.Errorf("%w: type %q", ErrNotWorkerDone, message.Type)
	}
	if err := ValidateOrcaMessageID(message.ID); err != nil {
		return WorkerResult{}, fmt.Errorf("%w: %v", ErrInvalidWorkerResult, err)
	}
	if message.Payload == "" {
		return WorkerResult{}, fmt.Errorf("%w: empty payload", ErrInvalidWorkerResult)
	}
	var result WorkerResult
	payload := []byte(strings.TrimSpace(message.Payload))
	if payload[0] == '"' {
		var encoded string
		if err := json.Unmarshal(payload, &encoded); err != nil {
			return WorkerResult{}, fmt.Errorf("%w: payload string is not JSON: %v", ErrInvalidWorkerResult, err)
		}
		payload = []byte(strings.TrimSpace(encoded))
	}
	if payload[0] != '{' {
		return WorkerResult{}, fmt.Errorf("%w: payload is not a JSON object", ErrInvalidWorkerResult)
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	// Tolerant alias decoding: `filesModified` may arrive as a comma-separated
	// string or an array.
	var raw struct {
		TaskID        string          `json:"taskId"`
		DispatchID    string          `json:"dispatchId"`
		Outcome       string          `json:"outcome"`
		FilesModified json.RawMessage `json:"filesModified"`
		ReportPath    string          `json:"reportPath"`
	}
	if err := decoder.Decode(&raw); err != nil {
		return WorkerResult{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidWorkerResult, err)
	}
	result.TaskID = raw.TaskID
	result.DispatchID = raw.DispatchID
	result.Outcome = strings.ToLower(strings.TrimSpace(raw.Outcome))
	result.ReportPath = strings.TrimSpace(raw.ReportPath)
	files, err := parseFilesModified(raw.FilesModified)
	if err != nil {
		return WorkerResult{}, fmt.Errorf("%w: %v", ErrInvalidWorkerResult, err)
	}
	result.FilesModified = files

	if err := ValidateOrcaTaskID(result.TaskID); err != nil {
		return WorkerResult{}, fmt.Errorf("%w: %v", ErrInvalidWorkerResult, err)
	}
	if err := ValidateOrcaDispatchID(result.DispatchID); err != nil {
		return WorkerResult{}, fmt.Errorf("%w: %v", ErrInvalidWorkerResult, err)
	}
	if !WorkerOutcomes[result.Outcome] {
		return WorkerResult{}, fmt.Errorf("%w: outcome %q is not one of succeeded|failed", ErrInvalidWorkerResult, result.Outcome)
	}
	for _, file := range result.FilesModified {
		if strings.TrimSpace(file) == "" {
			return WorkerResult{}, fmt.Errorf("%w: empty file entry in filesModified", ErrInvalidWorkerResult)
		}
	}
	if result.ReportPath != "" && strings.HasPrefix(result.ReportPath, "-") {
		return WorkerResult{}, fmt.Errorf("%w: report path %q looks like a CLI flag", ErrInvalidWorkerResult, result.ReportPath)
	}
	return result, nil
}

func parseFilesModified(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return []string{}, nil
	}
	if trimmed[0] == '[' {
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("filesModified array: %v", err)
		}
		return list, nil
	}
	var joined string
	if err := json.Unmarshal(raw, &joined); err != nil {
		return nil, fmt.Errorf("filesModified must be an array or a string: %v", err)
	}
	if strings.TrimSpace(joined) == "" {
		return []string{}, nil
	}
	parts := strings.Split(joined, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out, nil
}

// ResultDigestInput is the immutable content digest of one writeback. The
// identity fields (message/task/dispatch) are excluded so an exact replay of
// the same delivery produces the same digest and is accepted as idempotent,
// while any content drift conflicts.
type ResultDigestInput struct {
	OrcaTaskID     string   `json:"orca_task_id"`
	OrcaDispatchID string   `json:"orca_dispatch_id"`
	Outcome        string   `json:"outcome"`
	Subject        string   `json:"subject"`
	Body           string   `json:"body"`
	FilesModified  []string `json:"files_modified"`
	ReportPath     string   `json:"report_path"`
}

// WorkerResultDigest digests one worker result plus its message envelope.
func WorkerResultDigest(message OrcaMessage, result WorkerResult) (string, error) {
	return CanonicalDigest(ResultDigestInput{
		OrcaTaskID:     result.TaskID,
		OrcaDispatchID: result.DispatchID,
		Outcome:        result.Outcome,
		Subject:        message.Subject,
		Body:           message.Body,
		FilesModified:  normalizeFiles(result.FilesModified),
		ReportPath:     result.ReportPath,
	})
}

func normalizeFiles(files []string) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, strings.TrimSpace(file))
	}
	return out
}
