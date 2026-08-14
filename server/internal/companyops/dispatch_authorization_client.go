package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const (
	HiveCosmDispatchAuthorizationEndpoint = "/api/company-ops/dispatch-authorization"
	HiveCosmDispatchAuthorizationSchema   = "hivecosm.dispatch-authorization-read.v1"
	dispatchAuthorizationMaxAge           = 15 * time.Minute
	dispatchAuthorizationFutureSkew       = 5 * time.Second
)

var (
	dispatchSafeID           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}$`)
	dispatchUUID             = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	dispatchWorkOrderRef     = regexp.MustCompile(`^hive://hivecosm/delivery/project/([A-Za-z0-9][A-Za-z0-9@._:-]{0,191})/work-order/([A-Za-z0-9][A-Za-z0-9@._:-]{0,191})$`)
	dispatchRevision         = regexp.MustCompile(`^(?:xmin:[1-9][0-9]*|revision:[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}|sha256:[a-f0-9]{64}|receipt:[A-Za-z0-9][A-Za-z0-9@._:-]{0,191})$`)
	dispatchOwnerDecisionRef = regexp.MustCompile(`^hive://owner-decisions/[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}$`)
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
	if lookup.TenantID != c.tenantID {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("dispatch authorization tenant does not match configured authority boundary"))
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
	if err := ValidateDispatchAuthorizationResponseAt(envelope, lookup, c.now().UTC()); err != nil {
		return DispatchAuthorizationResponse{}, dispatchAuthorizationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, err)
	}
	return envelope, nil
}
func (c *HiveCosmDispatchAuthorizationClient) validate(r DispatchAuthorizationResponse, lookup DispatchAuthorizationLookup) error {
	if r.TenantID != c.tenantID {
		return errors.New("dispatch authorization tenant does not match configured authority boundary")
	}
	return ValidateDispatchAuthorizationResponseAt(r, lookup, c.now().UTC())
}

// ValidateDispatchAuthorizationResponseAt applies the same complete,
// fail-closed Authority contract used by the HTTP client to an already
// received response. Dispatch gates use this entry point so an injected
// authorizer cannot weaken evidence, freshness, expiry, or provenance checks.
func ValidateDispatchAuthorizationResponseAt(r DispatchAuthorizationResponse, lookup DispatchAuthorizationLookup, now time.Time) error {
	if r.SchemaVersion != HiveCosmDispatchAuthorizationSchema || !r.OK || !r.ReadOnly || r.TenantID != lookup.TenantID || r.Request != lookup || r.ExecutionIdentity != lookup.ExecutionIdentity {
		return errors.New("dispatch authorization identity is not exact")
	}
	if err := validateSafeID(r.TenantID, "tenant_id"); err != nil {
		return err
	}
	if err := validateDispatchAuthorizationLookup(lookup); err != nil {
		return err
	}
	now = now.UTC()
	if err := validateScope(r.Scope, lookup.TenantID, now); err != nil {
		return err
	}
	if err := validateIssueLinkage(r.IssueLinkage, now); err != nil {
		return err
	}
	if r.Evidence == nil {
		return errors.New("dispatch authorization evidence is missing")
	}
	if !reflect.DeepEqual(r.Scope, r.Evidence.Scope) || !reflect.DeepEqual(r.IssueLinkage, r.Evidence.IssueLinkage) {
		return errors.New("dispatch authorization top-level and evidence identity drift")
	}
	if err := validateEvidence(*r.Evidence, lookup.TenantID, lookup, now); err != nil {
		return err
	}
	if err := validateAuthorizationDecision(r.Authorization.EventReconcile, "event_reconcile", *r.Evidence, now); err != nil {
		return err
	}
	if err := validateAuthorizationDecision(r.Authorization.RecoveryOnly, "recovery_only", *r.Evidence, now); err != nil {
		return err
	}
	if !r.Authorization.EventReconcile.Eligible && !r.Authorization.RecoveryOnly.Eligible {
		return errors.New("dispatch authorization is not eligible")
	}
	return nil
}
func validateScope(s DispatchAuthorizationScope, tenant string, now time.Time) error {
	if s.State != "OBSERVED" || s.Freshness != "current" || s.TenantID == nil || *s.TenantID != tenant || s.WorkspaceID == nil || s.WorkflowID == nil || s.GoalID == nil || s.WorkOrderID == nil || s.SourceRef == nil || s.SourceRevision == nil {
		return errors.New("dispatch authorization scope is incomplete")
	}
	if err := validateSafeID(*s.TenantID, "scope.tenant_id"); err != nil {
		return err
	}
	if !dispatchUUID.MatchString(*s.WorkspaceID) {
		return errors.New("dispatch authorization scope.workspace_id is not canonical UUID")
	}
	for n, v := range map[string]string{"scope.workflow_id": *s.WorkflowID, "scope.goal_id": *s.GoalID, "scope.work_order_id": *s.WorkOrderID} {
		if err := validateSafeID(v, n); err != nil {
			return err
		}
	}
	if !canonicalSourceRef(*s.SourceRef) || !canonicalRevision(*s.SourceRevision) || !matchesHivePath(*s.SourceRef, "scope", tenant, *s.WorkspaceID, *s.WorkflowID, *s.GoalID, *s.WorkOrderID) {
		return errors.New("dispatch authorization scope provenance is invalid")
	}
	return validateFreshTimes(s.ObservedAt, s.SourceGeneratedAt, s.ExpiresAt, now)
}
func validateIssueLinkage(l DispatchAuthorizationIssueLinkage, now time.Time) error {
	if l.State != "OBSERVED" || l.Freshness != "current" || l.IssueID == nil || l.ProjectID == nil || l.WorkOrderID == nil || l.SourceRef == nil || l.SourceRevision == nil {
		return errors.New("dispatch authorization issue linkage is incomplete")
	}
	if !dispatchUUID.MatchString(*l.IssueID) {
		return errors.New("dispatch authorization issue_linkage.issue_id is not canonical UUID")
	}
	for n, v := range map[string]string{"issue_linkage.project_id": *l.ProjectID, "issue_linkage.work_order_id": *l.WorkOrderID} {
		if err := validateSafeID(v, n); err != nil {
			return err
		}
	}
	if !canonicalSourceRef(*l.SourceRef) || !canonicalRevision(*l.SourceRevision) || !matchesHivePath(*l.SourceRef, "issues", *l.ProjectID, *l.WorkOrderID, *l.IssueID) {
		return errors.New("dispatch authorization issue linkage provenance is invalid")
	}
	return validateFreshTimes(l.ObservedAt, l.SourceGeneratedAt, l.ExpiresAt, now)
}
func validateEvidence(e DispatchAuthorizationEvidence, tenant string, l DispatchAuthorizationLookup, now time.Time) error {
	if err := validateScope(e.Scope, tenant, now); err != nil {
		return err
	}
	if err := validateIssueLinkage(e.IssueLinkage, now); err != nil {
		return err
	}
	projectID, workOrderID, ok := parseWorkOrderSourceRef(l.ExecutionIdentity.WorkOrderSourceRef)
	if !ok || e.WorkOrder.State != "OBSERVED" || e.WorkOrder.WorkOrderID == nil || e.WorkOrder.ProjectID == nil || e.WorkOrder.SourceRef == nil || e.WorkOrder.SourceRevision == nil || *e.WorkOrder.SourceRef != l.ExecutionIdentity.WorkOrderSourceRef || *e.WorkOrder.WorkOrderID != workOrderID || *e.WorkOrder.ProjectID != projectID {
		return errors.New("dispatch authorization work order evidence is unmapped")
	}
	if err := validateEvidenceRecord(e.WorkOrder, now); err != nil {
		return err
	}
	if e.IssueLinkage.ProjectID == nil || e.IssueLinkage.WorkOrderID == nil || e.Scope.WorkOrderID == nil || *e.IssueLinkage.ProjectID != projectID || *e.IssueLinkage.WorkOrderID != workOrderID || *e.Scope.WorkOrderID != workOrderID {
		return errors.New("dispatch authorization linkage and scope work order drift")
	}
	if e.Assignment.State != "OBSERVED" || e.Assignment.AssignmentID == nil || e.Assignment.EmployeeID == nil || e.Assignment.AgentID == nil || e.Assignment.WorkOrderID == nil || *e.Assignment.AssignmentID != l.ExecutionIdentity.AssignmentID || *e.Assignment.EmployeeID != l.ExecutionIdentity.EmployeeID || *e.Assignment.AgentID != l.ExecutionIdentity.AgentID || *e.Assignment.WorkOrderID != workOrderID || e.Assignment.SourceRef == nil || !matchesHivePath(*e.Assignment.SourceRef, "assignments", l.ExecutionIdentity.AssignmentID) {
		return errors.New("dispatch authorization assignment evidence is unmapped")
	}
	if e.IdentityBinding.State != "OBSERVED" || e.IdentityBinding.IdentityBindingID == nil || e.IdentityBinding.EmployeeID == nil || e.IdentityBinding.AgentID == nil || *e.IdentityBinding.IdentityBindingID != l.ExecutionIdentity.IdentityBindingID || *e.IdentityBinding.EmployeeID != l.ExecutionIdentity.EmployeeID || *e.IdentityBinding.AgentID != l.ExecutionIdentity.AgentID || !e.IdentityBinding.Active || e.IdentityBinding.SourceRef == nil || !matchesHivePath(*e.IdentityBinding.SourceRef, "identity-bindings", l.ExecutionIdentity.IdentityBindingID) {
		return errors.New("dispatch authorization identity binding evidence is unmapped")
	}
	if err := validateEvidenceRecord(e.Assignment.dispatchEvidenceRecord, now); err != nil {
		return err
	}
	if err := validateEvidenceRecord(e.IdentityBinding.dispatchEvidenceRecord, now); err != nil {
		return err
	}
	if e.Custody.State != "OBSERVED" || e.Custody.WorkOrderID == nil || e.Custody.AssignmentID == nil || e.Custody.EmployeeID == nil || e.Custody.AgentID == nil || *e.Custody.WorkOrderID != workOrderID || *e.Custody.AssignmentID != l.ExecutionIdentity.AssignmentID || *e.Custody.EmployeeID != l.ExecutionIdentity.EmployeeID || *e.Custody.AgentID != l.ExecutionIdentity.AgentID || len(e.Custody.Gaps) != 0 || len(e.Custody.Conflicts) != 0 || e.Custody.SourceRef == nil || !matchesHivePath(*e.Custody.SourceRef, "custody", workOrderID, l.ExecutionIdentity.AssignmentID) {
		return errors.New("dispatch authorization custody evidence is invalid")
	}
	if err := validateEvidenceRecord(e.Custody.dispatchEvidenceRecord, now); err != nil {
		return err
	}
	if e.ContinuousWorkflowAuthorization.State != "AUTHORIZED" || e.ContinuousWorkflowAuthorization.Freshness != "current" || e.ContinuousWorkflowAuthorization.Scope == nil || e.ContinuousWorkflowAuthorization.WorkflowID == nil || e.ContinuousWorkflowAuthorization.GoalID == nil || e.ContinuousWorkflowAuthorization.WorkOrderID == nil || e.ContinuousWorkflowAuthorization.OwnerDecisionRef == nil || e.ContinuousWorkflowAuthorization.SourceRef == nil || e.ContinuousWorkflowAuthorization.SourceRevision == nil || *e.ContinuousWorkflowAuthorization.WorkOrderID != workOrderID || *e.ContinuousWorkflowAuthorization.WorkflowID != *e.Scope.WorkflowID || *e.ContinuousWorkflowAuthorization.GoalID != *e.Scope.GoalID || !(*e.ContinuousWorkflowAuthorization.Scope == "event_reconcile" || *e.ContinuousWorkflowAuthorization.Scope == "recovery_only") || !dispatchOwnerDecisionRef.MatchString(*e.ContinuousWorkflowAuthorization.OwnerDecisionRef) || !canonicalRevision(*e.ContinuousWorkflowAuthorization.SourceRevision) || !matchesHivePathShape(*e.ContinuousWorkflowAuthorization.SourceRef, "authorizations", 1) {
		return errors.New("dispatch authorization workflow evidence is invalid")
	}
	if err := validateFreshTimes(e.ContinuousWorkflowAuthorization.ObservedAt, e.ContinuousWorkflowAuthorization.SourceGeneratedAt, e.ContinuousWorkflowAuthorization.ExpiresAt, now); err != nil {
		return errors.New("dispatch authorization workflow evidence is invalid")
	}
	if err := validateOwnerDecisionEvidence(e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority, *e.ContinuousWorkflowAuthorization.OwnerDecisionRef, now); err != nil {
		return err
	}
	if err := validateWorkflowOrGoalEvidence(e.WorkflowAuthority, *e.Scope.WorkflowID, true, now); err != nil {
		return err
	}
	if err := validateWorkflowOrGoalEvidence(e.GoalAuthority, *e.Scope.GoalID, false, now); err != nil {
		return err
	}
	return nil
}
func validateEvidenceRecord(r dispatchEvidenceRecord, now time.Time) error {
	if r.State != "OBSERVED" || r.Freshness != "current" || r.SourceRef == nil || r.SourceRevision == nil || !canonicalSourceRef(*r.SourceRef) || !canonicalRevision(*r.SourceRevision) {
		return errors.New("dispatch authorization evidence is stale or malformed")
	}
	return validateFreshTimes(r.ObservedAt, r.SourceGeneratedAt, r.ExpiresAt, now)
}

func validateOwnerDecisionEvidence(e dispatchOwnerDecisionEvidence, expected string, now time.Time) error {
	if e.State != "OBSERVED" || e.Freshness != "current" || e.OwnerDecisionRef == nil || *e.OwnerDecisionRef != expected || e.SourceRef == nil || e.SourceRevision == nil || !dispatchOwnerDecisionRef.MatchString(*e.OwnerDecisionRef) || !canonicalRevision(*e.SourceRevision) || *e.SourceRef != expected {
		return errors.New("dispatch authorization owner decision evidence is invalid")
	}
	return validateFreshTimes(e.ObservedAt, e.SourceGeneratedAt, e.ExpiresAt, now)
}

func validateWorkflowOrGoalEvidence(e dispatchAuthorityEvidenceRecord, expected string, workflow bool, now time.Time) error {
	host := "goals"
	if workflow {
		host = "workflows"
	}
	if e.State != "OBSERVED" || e.Freshness != "current" || e.SourceRef == nil || e.SourceRevision == nil || !canonicalRevision(*e.SourceRevision) || !matchesHivePath(*e.SourceRef, host, expected) {
		return errors.New("dispatch authorization workflow or goal evidence is invalid")
	}
	if workflow && (e.WorkflowID == nil || *e.WorkflowID != expected) {
		return errors.New("dispatch authorization workflow authority identity is invalid")
	}
	if !workflow && (e.GoalID == nil || *e.GoalID != expected) {
		return errors.New("dispatch authorization goal authority identity is invalid")
	}
	return validateFreshTimes(e.ObservedAt, e.SourceGeneratedAt, e.ExpiresAt, now)
}

func validateAuthorizationDecision(d DispatchAuthorizationDecision, wanted string, e DispatchAuthorizationEvidence, now time.Time) error {
	if d.ObservedAt == "" || d.Freshness != "current" || d.Freshness != e.ContinuousWorkflowAuthorization.Freshness || d.ExpiresAt == nil || e.ContinuousWorkflowAuthorization.ExpiresAt == nil || *d.ExpiresAt != *e.ContinuousWorkflowAuthorization.ExpiresAt || d.ObservedAt != e.ContinuousWorkflowAuthorization.ObservedAt {
		return errors.New("dispatch authorization decision freshness is invalid")
	}
	if err := validateFreshTimes(d.ObservedAt, nil, d.ExpiresAt, now); err != nil {
		return err
	}
	if len(d.SourceRefs) == 0 || len(d.SourceRevisions) == 0 {
		return errors.New("dispatch authorization decision provenance is missing")
	}
	for _, ref := range d.SourceRefs {
		if !canonicalSourceRef(ref) {
			return errors.New("dispatch authorization decision source ref is invalid")
		}
	}
	for _, revision := range d.SourceRevisions {
		if !canonicalRevision(revision) {
			return errors.New("dispatch authorization decision source revision is invalid")
		}
	}
	if !sameStringSet(d.SourceRefs, expectedDecisionSourceRefs(e)) || !sameStringSet(d.SourceRevisions, expectedDecisionSourceRevisions(e)) {
		return errors.New("dispatch authorization decision provenance does not match evidence")
	}
	if d.Eligible {
		if e.ContinuousWorkflowAuthorization.Scope == nil || *e.ContinuousWorkflowAuthorization.Scope != wanted || d.Reason != "eligible:all_required_authority_evidence_current" {
			return errors.New("dispatch authorization decision scope is invalid")
		}
		return nil
	}
	if !strings.HasPrefix(d.Reason, "blocked:") || e.ContinuousWorkflowAuthorization.Scope == nil || wanted == *e.ContinuousWorkflowAuthorization.Scope {
		return errors.New("dispatch authorization non-eligible decision is invalid")
	}
	return nil
}

func containsExact(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func expectedDecisionSourceRefs(e DispatchAuthorizationEvidence) []string {
	return requiredEvidenceStrings(
		e.Scope.SourceRef, e.IssueLinkage.SourceRef, e.WorkOrder.SourceRef, e.Assignment.SourceRef,
		e.IdentityBinding.SourceRef, e.Custody.SourceRef, e.ContinuousWorkflowAuthorization.SourceRef,
		e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.SourceRef, e.WorkflowAuthority.SourceRef, e.GoalAuthority.SourceRef,
	)
}
func expectedDecisionSourceRevisions(e DispatchAuthorizationEvidence) []string {
	return requiredEvidenceStrings(
		e.Scope.SourceRevision, e.IssueLinkage.SourceRevision, e.WorkOrder.SourceRevision, e.Assignment.SourceRevision,
		e.IdentityBinding.SourceRevision, e.Custody.SourceRevision, e.ContinuousWorkflowAuthorization.SourceRevision,
		e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.SourceRevision, e.WorkflowAuthority.SourceRevision, e.GoalAuthority.SourceRevision,
	)
}
func requiredEvidenceStrings(values ...*string) []string {
	unique := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil
		}
		if _, exists := unique[*value]; !exists {
			unique[*value] = struct{}{}
			result = append(result, *value)
		}
	}
	return result
}
func sameStringSet(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(expected))
	for _, value := range expected {
		seen[value] = struct{}{}
	}
	for _, value := range actual {
		if _, ok := seen[value]; !ok {
			return false
		}
		delete(seen, value)
	}
	return len(seen) == 0
}
func validateFreshTimes(observed string, generated, expires *string, now time.Time) error {
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
	if expires == nil {
		return errors.New("dispatch authorization expires_at is missing")
	}
	x, e := parseDispatchAuthorizationTime(*expires, now, false)
	if e != nil || !x.After(now) || !x.After(o) {
		return errors.New("dispatch authorization expires_at is invalid")
	}
	return nil
}
func validateDispatchAuthorizationLookup(l DispatchAuthorizationLookup) error {
	if err := validateSafeID(l.TenantID, "tenant_id"); err != nil {
		return err
	}
	if _, _, ok := parseWorkOrderSourceRef(l.ExecutionIdentity.WorkOrderSourceRef); !ok {
		return errors.New("dispatch authorization work_order_source_ref is not canonical")
	}
	for n, v := range map[string]string{"employee_id": l.ExecutionIdentity.EmployeeID, "identity_binding_id": l.ExecutionIdentity.IdentityBindingID, "assignment_id": l.ExecutionIdentity.AssignmentID} {
		if err := validateSafeID(v, n); err != nil {
			return err
		}
	}
	if !dispatchUUID.MatchString(l.ExecutionIdentity.AgentID) {
		return errors.New("dispatch authorization agent_id is not canonical UUID")
	}
	return nil
}
func validateSafeID(v, n string) error {
	if !dispatchSafeID.MatchString(v) || strings.TrimSpace(v) != v {
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
	return dispatchRevision.MatchString(v)
}
func parseWorkOrderSourceRef(value string) (string, string, bool) {
	matched := dispatchWorkOrderRef.FindStringSubmatch(value)
	if matched == nil {
		return "", "", false
	}
	return matched[1], matched[2], true
}
func matchesHivePath(ref, host string, expected ...string) bool {
	return matchesHivePathShape(ref, host, len(expected)) && exactHivePathSegments(ref, expected...)
}
func matchesHivePathShape(ref, host string, segmentCount int) bool {
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "hive" || u.Host != host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(u.Path, "/") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(segments) != segmentCount {
		return false
	}
	for _, segment := range segments {
		if !dispatchSafeID.MatchString(segment) {
			return false
		}
	}
	return true
}
func exactHivePathSegments(ref string, expected ...string) bool {
	u, err := url.Parse(ref)
	if err != nil {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(segments) != len(expected) {
		return false
	}
	for index, value := range expected {
		if segments[index] != value {
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
