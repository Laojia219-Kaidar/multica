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
	HiveCosmDispatchAuthorizationSchema   = "hivecosm.dispatch-authorization-read.v1"
	dispatchAuthorizationMaxAge           = 15 * time.Minute
	dispatchAuthorizationFutureSkew       = 5 * time.Second
)

// The five selectors are one indivisible execution identity. No local object
// (Agent, Issue, Task, Goal) may be used to fill or translate these fields.
type DispatchAuthorizationExecutionIdentity struct {
	WorkOrderSourceRef string `json:"work_order_source_ref"`
	EmployeeID         string `json:"employee_id"`
	IdentityBindingID  string `json:"identity_binding_id"`
	AgentID            string `json:"agent_id"`
	AssignmentID       string `json:"assignment_id"`
}
type DispatchAuthorizationLookup struct {
	TenantID          string                                 `json:"tenant_id"`
	ExecutionIdentity DispatchAuthorizationExecutionIdentity `json:"execution_identity"`
}
type DispatchAuthorizationScope struct {
	State             string  `json:"state"`
	TenantID          *string `json:"tenant_id"`
	WorkspaceID       *string `json:"workspace_id"`
	WorkflowID        *string `json:"workflow_id"`
	GoalID            *string `json:"goal_id"`
	WorkOrderID       *string `json:"work_order_id"`
	SourceRef         *string `json:"source_ref"`
	SourceRevision    *string `json:"source_revision"`
	ObservedAt        string  `json:"observed_at"`
	SourceGeneratedAt *string `json:"source_generated_at"`
	Freshness         string  `json:"freshness"`
	ExpiresAt         *string `json:"expires_at"`
}
type DispatchAuthorizationIssueLinkage struct {
	State             string  `json:"state"`
	IssueID           *string `json:"issue_id"`
	ProjectID         *string `json:"project_id"`
	WorkOrderID       *string `json:"work_order_id"`
	SourceRef         *string `json:"source_ref"`
	SourceRevision    *string `json:"source_revision"`
	ObservedAt        string  `json:"observed_at"`
	SourceGeneratedAt *string `json:"source_generated_at"`
	Freshness         string  `json:"freshness"`
	ExpiresAt         *string `json:"expires_at"`
}
type dispatchEvidenceRecord struct {
	State             string  `json:"state"`
	SourceRef         *string `json:"source_ref"`
	SourceRevision    *string `json:"source_revision"`
	WorkOrderID       *string `json:"work_order_id"`
	ProjectID         *string `json:"project_id"`
	Status            *string `json:"status"`
	ObservedAt        string  `json:"observed_at"`
	SourceGeneratedAt *string `json:"source_generated_at"`
	Freshness         string  `json:"freshness"`
	ExpiresAt         *string `json:"expires_at"`
}
type dispatchAssignmentEvidence struct {
	dispatchEvidenceRecord
	AssignmentID *string `json:"assignment_id"`
	EmployeeID   *string `json:"employee_id"`
	AgentID      *string `json:"agent_id"`
}
type dispatchBindingEvidence struct {
	dispatchEvidenceRecord
	IdentityBindingID *string `json:"identity_binding_id"`
	EmployeeID        *string `json:"employee_id"`
	AgentID           *string `json:"agent_id"`
	Active            bool    `json:"active"`
}
type dispatchCustodyEvidence struct {
	dispatchEvidenceRecord
	AssignmentID *string  `json:"assignment_id"`
	EmployeeID   *string  `json:"employee_id"`
	AgentID      *string  `json:"agent_id"`
	Gaps         []string `json:"gaps"`
	Conflicts    []string `json:"conflicts"`
}
type dispatchOwnerDecisionEvidence struct {
	State             string  `json:"state"`
	OwnerDecisionRef  *string `json:"owner_decision_ref"`
	SourceRef         *string `json:"source_ref"`
	SourceRevision    *string `json:"source_revision"`
	ObservedAt        string  `json:"observed_at"`
	SourceGeneratedAt *string `json:"source_generated_at"`
	Freshness         string  `json:"freshness"`
	ExpiresAt         *string `json:"expires_at"`
}
type dispatchWorkflowEvidence struct {
	State                  string                        `json:"state"`
	Scope                  *string                       `json:"scope"`
	WorkflowID             *string                       `json:"workflow_id"`
	GoalID                 *string                       `json:"goal_id"`
	WorkOrderID            *string                       `json:"work_order_id"`
	OwnerDecisionRef       *string                       `json:"owner_decision_ref"`
	SourceRef              *string                       `json:"source_ref"`
	SourceRevision         *string                       `json:"source_revision"`
	ObservedAt             string                        `json:"observed_at"`
	SourceGeneratedAt      *string                       `json:"source_generated_at"`
	Freshness              string                        `json:"freshness"`
	ExpiresAt              *string                       `json:"expires_at"`
	OwnerDecisionAuthority dispatchOwnerDecisionEvidence `json:"owner_decision_authority"`
}
type dispatchAuthorityEvidenceRecord struct {
	State             string  `json:"state"`
	WorkflowID        *string `json:"workflow_id"`
	GoalID            *string `json:"goal_id"`
	SourceRef         *string `json:"source_ref"`
	SourceRevision    *string `json:"source_revision"`
	ObservedAt        string  `json:"observed_at"`
	SourceGeneratedAt *string `json:"source_generated_at"`
	Freshness         string  `json:"freshness"`
	ExpiresAt         *string `json:"expires_at"`
}
type DispatchAuthorizationEvidence struct {
	Scope                           DispatchAuthorizationScope        `json:"scope"`
	IssueLinkage                    DispatchAuthorizationIssueLinkage `json:"issue_linkage"`
	WorkOrder                       dispatchEvidenceRecord            `json:"work_order"`
	Assignment                      dispatchAssignmentEvidence        `json:"assignment"`
	IdentityBinding                 dispatchBindingEvidence           `json:"identity_binding"`
	Custody                         dispatchCustodyEvidence           `json:"custody"`
	ContinuousWorkflowAuthorization dispatchWorkflowEvidence          `json:"continuous_workflow_authorization"`
	WorkflowAuthority               dispatchAuthorityEvidenceRecord   `json:"workflow_authority"`
	GoalAuthority                   dispatchAuthorityEvidenceRecord   `json:"goal_authority"`
}
type DispatchAuthorizationDecision struct {
	Eligible        bool     `json:"eligible"`
	Reason          string   `json:"reason"`
	SourceRefs      []string `json:"source_refs"`
	SourceRevisions []string `json:"source_revisions"`
	ObservedAt      string   `json:"observed_at"`
	Freshness       string   `json:"freshness"`
	ExpiresAt       *string  `json:"expires_at"`
}
type DispatchAuthorizationResponse struct {
	SchemaVersion     string                                 `json:"schema_version"`
	OK                bool                                   `json:"ok"`
	ReadOnly          bool                                   `json:"read_only"`
	TenantID          string                                 `json:"tenant_id"`
	Request           DispatchAuthorizationLookup            `json:"request"`
	ExecutionIdentity DispatchAuthorizationExecutionIdentity `json:"execution_identity"`
	Scope             DispatchAuthorizationScope             `json:"scope"`
	IssueLinkage      DispatchAuthorizationIssueLinkage      `json:"issue_linkage"`
	Authorization     struct {
		EventReconcile DispatchAuthorizationDecision `json:"event_reconcile"`
		RecoveryOnly   DispatchAuthorizationDecision `json:"recovery_only"`
	} `json:"authorization"`
	Evidence *DispatchAuthorizationEvidence `json:"evidence"`
}

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
	copy := *httpClient
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	httpClient = &copy
	return &HiveCosmDispatchAuthorizationClient{baseURL: u, httpClient: httpClient, tenantID: tenantID, now: time.Now}, nil
}
func (c *HiveCosmDispatchAuthorizationClient) Resolve(ctx context.Context, lookup DispatchAuthorizationLookup) (DispatchAuthorizationResponse, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("dispatch authorization source is not configured"))
	}
	if err := validateDispatchAuthorizationLookup(lookup); err != nil {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, err)
	}
	endpoint := *c.baseURL
	endpoint.Path = HiveCosmDispatchAuthorizationEndpoint
	endpoint.RawPath = ""
	q := endpoint.Query()
	q.Set("tenant_id", lookup.TenantID)
	q.Set("work_order_source_ref", lookup.ExecutionIdentity.WorkOrderSourceRef)
	q.Set("employee_id", lookup.ExecutionIdentity.EmployeeID)
	q.Set("identity_binding_id", lookup.ExecutionIdentity.IdentityBindingID)
	q.Set("agent_id", lookup.ExecutionIdentity.AgentID)
	q.Set("assignment_id", lookup.ExecutionIdentity.AssignmentID)
	endpoint.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("dispatch authorization request could not be created"))
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("dispatch authorization source is unreachable"))
	}
	defer resp.Body.Close()
	body, err := readCappedAuthorityBody(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("dispatch authorization source did not provide an eligible decision"))
	}
	if !isJSONMediaType(resp.Header.Get("Content-Type")) || !json.Valid(body) {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("dispatch authorization response is not JSON"))
	}
	var envelope DispatchAuthorizationResponse
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || ensureJSONEOF(decoder) != nil {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("dispatch authorization response shape is invalid"))
	}
	if err := c.validate(envelope, lookup); err != nil {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, err)
	}
	return envelope, nil
}
func (c *HiveCosmDispatchAuthorizationClient) validate(r DispatchAuthorizationResponse, lookup DispatchAuthorizationLookup) error {
	if r.SchemaVersion != HiveCosmDispatchAuthorizationSchema || !r.OK || !r.ReadOnly || r.TenantID != c.tenantID || r.Request != lookup || r.ExecutionIdentity != lookup.ExecutionIdentity {
		return errors.New("dispatch authorization identity is not exact")
	}
	if err := validateCanonicalID(r.TenantID, "tenant_id"); err != nil {
		return err
	}
	if err := validateScope(r.Scope, c.tenantID); err != nil {
		return err
	}
	if err := validateIssueLinkage(r.IssueLinkage); err != nil {
		return err
	}
	if r.Evidence == nil {
		return errors.New("dispatch authorization evidence is missing")
	}
	if err := validateEvidence(*r.Evidence, c.tenantID, lookup); err != nil {
		return err
	}
	if !r.Authorization.EventReconcile.Eligible && !r.Authorization.RecoveryOnly.Eligible {
		return errors.New("dispatch authorization is not eligible")
	}
	return nil
}
func validateScope(s DispatchAuthorizationScope, tenant string) error {
	if s.State != "OBSERVED" || s.Freshness != "current" || s.TenantID == nil || *s.TenantID != tenant || s.WorkspaceID == nil || s.WorkflowID == nil || s.GoalID == nil || s.WorkOrderID == nil || s.SourceRef == nil || s.SourceRevision == nil {
		return errors.New("dispatch authorization scope is incomplete")
	}
	for n, v := range map[string]string{"scope.tenant_id": *s.TenantID, "scope.workspace_id": *s.WorkspaceID, "scope.workflow_id": *s.WorkflowID, "scope.goal_id": *s.GoalID, "scope.work_order_id": *s.WorkOrderID} {
		if err := validateCanonicalID(v, n); err != nil {
			return err
		}
	}
	if !canonicalSourceRef(*s.SourceRef) || !canonicalRevision(*s.SourceRevision) || !refContainsAll(*s.SourceRef, tenant, *s.WorkspaceID, *s.WorkflowID, *s.GoalID, *s.WorkOrderID) {
		return errors.New("dispatch authorization scope provenance is invalid")
	}
	return validateFreshTimes(s.ObservedAt, s.SourceGeneratedAt, s.ExpiresAt)
}
func validateIssueLinkage(l DispatchAuthorizationIssueLinkage) error {
	if l.State != "OBSERVED" || l.Freshness != "current" || l.IssueID == nil || l.ProjectID == nil || l.WorkOrderID == nil || l.SourceRef == nil || l.SourceRevision == nil {
		return errors.New("dispatch authorization issue linkage is incomplete")
	}
	for n, v := range map[string]string{"issue_linkage.issue_id": *l.IssueID, "issue_linkage.project_id": *l.ProjectID, "issue_linkage.work_order_id": *l.WorkOrderID} {
		if err := validateCanonicalID(v, n); err != nil {
			return err
		}
	}
	if !canonicalSourceRef(*l.SourceRef) || !canonicalRevision(*l.SourceRevision) || !refContainsAll(*l.SourceRef, *l.ProjectID, *l.WorkOrderID, *l.IssueID) {
		return errors.New("dispatch authorization issue linkage provenance is invalid")
	}
	return validateFreshTimes(l.ObservedAt, l.SourceGeneratedAt, l.ExpiresAt)
}
func validateEvidence(e DispatchAuthorizationEvidence, tenant string, l DispatchAuthorizationLookup) error {
	if err := validateScope(e.Scope, tenant); err != nil {
		return err
	}
	if err := validateIssueLinkage(e.IssueLinkage); err != nil {
		return err
	}
	if e.WorkOrder.State != "OBSERVED" || e.WorkOrder.WorkOrderID == nil || e.WorkOrder.SourceRef == nil || e.WorkOrder.SourceRevision == nil || !canonicalSourceRef(*e.WorkOrder.SourceRef) || !canonicalRevision(*e.WorkOrder.SourceRevision) || !refContainsAll(*e.WorkOrder.SourceRef, *e.WorkOrder.WorkOrderID) || *e.WorkOrder.SourceRef != l.ExecutionIdentity.WorkOrderSourceRef {
		return errors.New("dispatch authorization work order evidence is unmapped")
	}
	if e.Assignment.State != "OBSERVED" || e.Assignment.AssignmentID == nil || e.Assignment.EmployeeID == nil || e.Assignment.AgentID == nil || *e.Assignment.AssignmentID != l.ExecutionIdentity.AssignmentID || *e.Assignment.EmployeeID != l.ExecutionIdentity.EmployeeID || *e.Assignment.AgentID != l.ExecutionIdentity.AgentID {
		return errors.New("dispatch authorization assignment evidence is unmapped")
	}
	if e.IdentityBinding.State != "OBSERVED" || e.IdentityBinding.IdentityBindingID == nil || e.IdentityBinding.EmployeeID == nil || e.IdentityBinding.AgentID == nil || *e.IdentityBinding.IdentityBindingID != l.ExecutionIdentity.IdentityBindingID || *e.IdentityBinding.EmployeeID != l.ExecutionIdentity.EmployeeID || *e.IdentityBinding.AgentID != l.ExecutionIdentity.AgentID || !e.IdentityBinding.Active {
		return errors.New("dispatch authorization identity binding evidence is unmapped")
	}
	if err := validateEvidenceRecord(e.WorkOrder); err != nil {
		return err
	}
	if err := validateEvidenceRecord(e.Assignment.dispatchEvidenceRecord); err != nil {
		return err
	}
	if err := validateEvidenceRecord(e.IdentityBinding.dispatchEvidenceRecord); err != nil {
		return err
	}
	if e.Custody.State != "OBSERVED" || e.Custody.WorkOrderID == nil || e.Custody.AssignmentID == nil || *e.Custody.WorkOrderID != *e.WorkOrder.WorkOrderID || *e.Custody.AssignmentID != l.ExecutionIdentity.AssignmentID {
		return errors.New("dispatch authorization custody evidence is invalid")
	}
	if err := validateEvidenceRecord(e.Custody.dispatchEvidenceRecord); err != nil {
		return err
	}
	if e.ContinuousWorkflowAuthorization.State != "AUTHORIZED" || e.ContinuousWorkflowAuthorization.WorkflowID == nil || e.ContinuousWorkflowAuthorization.GoalID == nil || e.ContinuousWorkflowAuthorization.WorkOrderID == nil {
		return errors.New("dispatch authorization workflow evidence is invalid")
	}
	if err := validateFreshTimes(e.ContinuousWorkflowAuthorization.ObservedAt, e.ContinuousWorkflowAuthorization.SourceGeneratedAt, e.ContinuousWorkflowAuthorization.ExpiresAt); err != nil {
		return errors.New("dispatch authorization workflow evidence is invalid")
	}
	return nil
}
func validateEvidenceRecord(r dispatchEvidenceRecord) error {
	if r.State != "OBSERVED" || r.Freshness != "current" || r.SourceRef == nil || r.SourceRevision == nil || !canonicalSourceRef(*r.SourceRef) || !canonicalRevision(*r.SourceRevision) {
		return errors.New("dispatch authorization evidence is stale or malformed")
	}
	return validateFreshTimes(r.ObservedAt, r.SourceGeneratedAt, r.ExpiresAt)
}
func validateFreshTimes(observed string, generated, expires *string) error {
	now := time.Now().UTC()
	o, err := parseDispatchAuthorizationTime(observed, now, true)
	if err != nil {
		return err
	}
	if generated != nil {
		g, e := parseDispatchAuthorizationTime(*generated, now, true)
		if e != nil || g.After(o) {
			return errors.New("dispatch authorization source_generated_at is invalid")
		}
	}
	if expires != nil {
		x, e := parseDispatchAuthorizationTime(*expires, now, false)
		if e != nil || !x.After(now) {
			return errors.New("dispatch authorization expires_at is invalid")
		}
	}
	return nil
}
func validateDispatchAuthorizationLookup(l DispatchAuthorizationLookup) error {
	if err := validateCanonicalID(l.TenantID, "tenant_id"); err != nil {
		return err
	}
	for n, v := range map[string]string{"work_order_source_ref": l.ExecutionIdentity.WorkOrderSourceRef, "employee_id": l.ExecutionIdentity.EmployeeID, "identity_binding_id": l.ExecutionIdentity.IdentityBindingID, "agent_id": l.ExecutionIdentity.AgentID, "assignment_id": l.ExecutionIdentity.AssignmentID} {
		if !canonicalNonblank(v) || len(v) > 256 || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("dispatch authorization %s is malformed", n)
		}
	}
	return nil
}
func validateCanonicalID(v, n string) error {
	if !canonicalNonblank(v) || len(v) > 256 || strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("dispatch authorization %s is malformed", n)
	}
	return nil
}
func canonicalSourceRef(v string) bool {
	if !canonicalNonblank(v) || strings.ContainsAny(v, "\r\n") {
		return false
	}
	if strings.HasPrefix(v, "/api/") {
		return !strings.ContainsAny(v, "?#")
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "hive" && u.Host != "" && u.RawQuery == "" && u.Fragment == ""
}
func canonicalRevision(v string) bool {
	return canonicalNonblank(v) && len(v) >= 8 && !strings.ContainsAny(v, " \t\r\n")
}
func refContainsAll(ref string, ids ...string) bool {
	for _, id := range ids {
		if !strings.Contains(ref, id) {
			return false
		}
	}
	return true
}
func parseDispatchAuthorizationTime(value string, now time.Time, fresh bool) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("timestamp is not canonical UTC RFC3339Nano")
	}
	if fresh && (parsed.After(now.Add(dispatchAuthorizationFutureSkew)) || now.Sub(parsed) > dispatchAuthorizationMaxAge) {
		return time.Time{}, errors.New("timestamp is outside freshness window")
	}
	return parsed, nil
}
func dispatchAuthorizationFailure(kind HiveCosmAuthorityErrorKind, status int, cause error) error {
	return &HiveCosmAuthorityError{Kind: kind, StatusCode: status, Cause: cause}
}
