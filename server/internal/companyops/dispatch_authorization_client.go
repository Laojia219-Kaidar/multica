package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	HiveCosmDispatchAuthorizationEndpoint = "/api/company-ops/dispatch-authorization"
	HiveCosmDispatchAuthorizationSchema   = "hivecosm.dispatch-authorization.v1"

	dispatchAuthorizationMaxAge     = 15 * time.Minute
	dispatchAuthorizationFutureSkew = 5 * time.Second
)

// DispatchAuthorizationLookup is the exact work slice for which Authority
// must grant permission. None of these values are inferred from local state.
type DispatchAuthorizationLookup struct {
	WorkspaceID string `json:"workspace_id"`
	WorkflowID  string `json:"workflow_id"`
	GoalID      string `json:"goal_id"`
	WorkOrderID string `json:"work_order_id"`
}

// DispatchAuthorization is an Authority-owned, read-only decision. It is
// deliberately smaller than a work item: it grants permission to consider a
// bounded slice, but never selects an agent or creates a Task.
type DispatchAuthorization struct {
	State             string `json:"state"`
	Scope             string `json:"scope"`
	WorkflowID        string `json:"workflow_id"`
	GoalID            string `json:"goal_id"`
	WorkOrderID       string `json:"work_order_id"`
	OwnerDecisionRef  string `json:"owner_decision_ref"`
	SourceRef         string `json:"source_ref"`
	SourceRevision    string `json:"source_revision"`
	ObservedAt        string `json:"observed_at"`
	SourceGeneratedAt string `json:"source_generated_at"`
	Freshness         string `json:"freshness"`
	ExpiresAt         string `json:"expires_at"`
}

// HiveCosmDispatchAuthorizationResponse is the only response shape accepted
// by this adapter. Unknown JSON fields are rejected by the decoder below.
type HiveCosmDispatchAuthorizationResponse struct {
	SchemaVersion string                      `json:"schema_version"`
	OK            bool                        `json:"ok"`
	TenantID      string                      `json:"tenant_id"`
	WorkspaceID   string                      `json:"workspace_id"`
	Request       DispatchAuthorizationLookup `json:"request"`
	Authorization DispatchAuthorization       `json:"authorization"`
}

// HiveCosmDispatchAuthorizationClient performs one authenticated GET and no
// writes. The bearer is normally injected by the existing CompanyOps origin-
// pinned transport in cmd/server; tests may use a transport that does so.
type HiveCosmDispatchAuthorizationClient struct {
	baseURL    *url.URL
	httpClient *http.Client
	tenantID   string
	now        func() time.Time
}

func NewHiveCosmDispatchAuthorizationClient(baseURL string, httpClient *http.Client, tenantID string) (*HiveCosmDispatchAuthorizationClient, error) {
	if !canonicalNonblank(tenantID) {
		return nil, fmt.Errorf("HIVECOSM_TENANT_ID is required")
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid adapter base URL")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHiveCosmAuthorityTimeout}
	}
	return &HiveCosmDispatchAuthorizationClient{baseURL: u, httpClient: httpClient, tenantID: tenantID, now: time.Now}, nil
}

// Resolve reads the exact Authority decision for lookup. No local Shadow,
// GoalRun, Checklist, or quota projection can satisfy this method.
func (c *HiveCosmDispatchAuthorizationClient) Resolve(ctx context.Context, lookup DispatchAuthorizationLookup) (DispatchAuthorization, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return DispatchAuthorization{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("dispatch authorization source is not configured"))
	}
	if err := validateDispatchAuthorizationLookup(lookup); err != nil {
		return DispatchAuthorization{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, err)
	}
	endpoint := *c.baseURL
	endpoint.Path = HiveCosmDispatchAuthorizationEndpoint
	endpoint.RawPath = ""
	query := endpoint.Query()
	query.Set("workspace_id", lookup.WorkspaceID)
	query.Set("workflow_id", lookup.WorkflowID)
	query.Set("goal_id", lookup.GoalID)
	query.Set("work_order_id", lookup.WorkOrderID)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return DispatchAuthorization{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("dispatch authorization request could not be created"))
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DispatchAuthorization{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("dispatch authorization source is unreachable"))
	}
	defer resp.Body.Close()
	body, err := readCappedAuthorityBody(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return DispatchAuthorization{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("dispatch authorization source did not provide an eligible decision"))
	}
	if !isJSONMediaType(resp.Header.Get("Content-Type")) || !json.Valid(body) {
		return DispatchAuthorization{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("dispatch authorization response is not JSON"))
	}
	var envelope HiveCosmDispatchAuthorizationResponse
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || ensureJSONEOF(decoder) != nil {
		return DispatchAuthorization{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("dispatch authorization response shape is invalid"))
	}
	if err := c.validate(envelope, lookup); err != nil {
		return DispatchAuthorization{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, err)
	}
	return envelope.Authorization, nil
}

func (c *HiveCosmDispatchAuthorizationClient) validate(response HiveCosmDispatchAuthorizationResponse, lookup DispatchAuthorizationLookup) error {
	if response.SchemaVersion != HiveCosmDispatchAuthorizationSchema || !response.OK || response.TenantID != c.tenantID || response.WorkspaceID != lookup.WorkspaceID {
		return errors.New("dispatch authorization identity is not exact")
	}
	if response.Request != lookup {
		return errors.New("dispatch authorization request echo does not match")
	}
	a := response.Authorization
	if a.State != "authorized" || (a.Scope != "event_reconcile" && a.Scope != "recovery_only") || a.WorkflowID != lookup.WorkflowID || a.GoalID != lookup.GoalID || a.WorkOrderID != lookup.WorkOrderID || a.Freshness != "fresh" {
		return errors.New("dispatch authorization is not eligible")
	}
	for name, value := range map[string]string{
		"owner_decision_ref": a.OwnerDecisionRef,
		"source_ref":         a.SourceRef,
		"source_revision":    a.SourceRevision,
	} {
		if !canonicalNonblank(value) || len(value) > 512 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("dispatch authorization %s is malformed", name)
		}
	}
	now := c.now().UTC()
	observed, err := parseDispatchAuthorizationTime(a.ObservedAt, now, true)
	if err != nil {
		return fmt.Errorf("dispatch authorization observed_at: %w", err)
	}
	sourceGenerated, err := parseDispatchAuthorizationTime(a.SourceGeneratedAt, now, true)
	if err != nil || sourceGenerated.After(observed) {
		return errors.New("dispatch authorization source_generated_at is invalid")
	}
	expires, err := parseDispatchAuthorizationTime(a.ExpiresAt, now, false)
	if err != nil || !expires.After(now) {
		return errors.New("dispatch authorization expires_at is invalid or expired")
	}
	if now.Sub(observed) > dispatchAuthorizationMaxAge {
		return errors.New("dispatch authorization is stale")
	}
	return nil
}

func validateDispatchAuthorizationLookup(lookup DispatchAuthorizationLookup) error {
	for name, value := range map[string]string{"workspace_id": lookup.WorkspaceID, "workflow_id": lookup.WorkflowID, "goal_id": lookup.GoalID, "work_order_id": lookup.WorkOrderID} {
		if !canonicalNonblank(value) || len(value) > 256 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("dispatch authorization %s is malformed", name)
		}
	}
	return nil
}

func parseDispatchAuthorizationTime(value string, now time.Time, fresh bool) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("timestamp is not canonical UTC RFC3339Nano")
	}
	if fresh && parsed.After(now.Add(dispatchAuthorizationFutureSkew)) {
		return time.Time{}, errors.New("timestamp is in the future")
	}
	if fresh && now.Sub(parsed) > dispatchAuthorizationMaxAge {
		return time.Time{}, errors.New("timestamp is stale")
	}
	return parsed, nil
}

func dispatchAuthorizationFailure(kind HiveCosmAuthorityErrorKind, status int, cause error) error {
	return &HiveCosmAuthorityError{Kind: kind, StatusCode: status, Cause: cause}
}
