package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// HiveCosmOwnerWorkContextEndpoint is the only upstream route this client
	// accepts as an exact owner-work-context authority lookup.
	HiveCosmOwnerWorkContextEndpoint = "/api/company-ops/owner-work-context"

	// HiveCosmOwnerWorkContextSchemaVersion prevents an unrelated JSON response
	// or a differently shaped projection from being interpreted as authority.
	HiveCosmOwnerWorkContextSchemaVersion = "hivecosm.owner-work-context.authority.v1"

	defaultHiveCosmAuthorityTimeout = 10 * time.Second
	maxHiveCosmAuthorityBodySize    = 1 << 20
)

var hiveCosmWorkOrderSourceRefPattern = regexp.MustCompile(
	`^hive://hivecosm/delivery/project/[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}/work-order/[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}$`,
)

// HiveCosmAuthorityErrorKind is a stable, fail-closed classification for an
// exact owner-work-context authority lookup.
type HiveCosmAuthorityErrorKind string

const (
	HiveCosmAuthorityNotFound    HiveCosmAuthorityErrorKind = "not_found"
	HiveCosmAuthoritySourceGap   HiveCosmAuthorityErrorKind = "source_gap"
	HiveCosmAuthorityUnsupported HiveCosmAuthorityErrorKind = "unsupported"
	HiveCosmAuthorityInvalid     HiveCosmAuthorityErrorKind = "invalid"
	HiveCosmAuthorityConflict    HiveCosmAuthorityErrorKind = "conflict"
)

// HiveCosmAuthorityError preserves a machine-readable failure kind without
// making callers parse error text.
type HiveCosmAuthorityError struct {
	Kind       HiveCosmAuthorityErrorKind
	StatusCode int
	Cause      error
}

func (e *HiveCosmAuthorityError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Cause)
}

func (e *HiveCosmAuthorityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// HiveCosmAuthorityLookup names the exact company-owned objects to resolve.
// AgentID is a HiveCrew-local UUID carried only as the right side of the
// IdentityBinding; it does not authorize HiveCosm to return an Agent snapshot.
type HiveCosmAuthorityLookup struct {
	WorkOrderSourceRef string
	EmployeeID         string
	IdentityBindingID  string
	AgentID            string
}

// HiveCosmAuthorityBundle is the validated three-object company authority
// result. The caller must resolve RequestedAgentID against HiveCrew's local
// Agent authority before invoking ValidateAndFreezeExecutionTarget.
type HiveCosmAuthorityBundle struct {
	WorkOrder        AuthoritySnapshot
	Employee         AuthoritySnapshot
	IdentityBinding  IdentityBinding
	RequestedAgentID string
}

// HiveCosmAuthorityClient performs only the governed read described by
// HiveCosmOwnerWorkContextEndpoint.
type HiveCosmAuthorityClient struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// NewHiveCosmAuthorityClient constructs a strict read-only authority client.
func NewHiveCosmAuthorityClient(baseURL string, httpClient *http.Client) (*HiveCosmAuthorityClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("HiveCosm base URL must be an absolute HTTP(S) URL"))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("HiveCosm base URL must use HTTP(S)"))
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("HiveCosm base URL must not contain userinfo, query, or fragment"))
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHiveCosmAuthorityTimeout}
	}
	return &HiveCosmAuthorityClient{baseURL: parsed, httpClient: httpClient}, nil
}

// ResolveOwnerWorkContext fetches and validates the exact WorkOrder, Employee,
// and IdentityBinding authority objects. It never sends a session identifier,
// accepts a list projection, or treats a HiveCosm Agent snapshot as authority.
func (c *HiveCosmAuthorityClient) ResolveOwnerWorkContext(ctx context.Context, lookup HiveCosmAuthorityLookup) (HiveCosmAuthorityBundle, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("HiveCosm authority client is not configured"))
	}
	if err := validateHiveCosmAuthorityLookup(lookup); err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, 0, err)
	}

	endpoint := *c.baseURL
	endpoint.Path = HiveCosmOwnerWorkContextEndpoint
	endpoint.RawPath = ""
	query := make(url.Values, 4)
	query.Set("work_order_source_ref", lookup.WorkOrderSourceRef)
	query.Set("employee_id", lookup.EmployeeID)
	query.Set("identity_binding_id", lookup.IdentityBindingID)
	query.Set("agent_id", lookup.AgentID)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, 0, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthoritySourceGap, 0, fmt.Errorf("HiveCosm authority transport: %w", err))
	}
	defer resp.Body.Close()

	body, err := readCappedAuthorityBody(resp.Body)
	if err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("HiveCosm authority authentication is unavailable"))
	}

	if resp.StatusCode == http.StatusNotFound && (!isJSONMediaType(resp.Header.Get("Content-Type")) || !json.Valid(body)) {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityUnsupported, resp.StatusCode, errors.New("owner work-context endpoint is unavailable"))
	}
	if !isJSONMediaType(resp.Header.Get("Content-Type")) || !json.Valid(body) {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("HiveCosm authority response is not JSON"))
	}

	var envelope hiveCosmAuthorityEnvelope
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityUnsupported, resp.StatusCode, errors.New("owner work-context endpoint did not return its authority schema"))
		}
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, resp.StatusCode, fmt.Errorf("decode authority contract: %w", err))
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, resp.StatusCode, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return HiveCosmAuthorityBundle{}, classifyStructuredNotFound(envelope, lookup, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNotImplemented {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityUnsupported, resp.StatusCode, errors.New("owner work-context endpoint is unsupported"))
	}
	if resp.StatusCode >= 500 {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("HiveCosm authority upstream is unavailable"))
	}
	if resp.StatusCode != http.StatusOK {
		return HiveCosmAuthorityBundle{}, classifyStructuredFailure(envelope, lookup, resp.StatusCode)
	}

	return validateHiveCosmAuthorityEnvelope(envelope, lookup)
}

type hiveCosmAuthorityRequestEcho struct {
	WorkOrderSourceRef string `json:"work_order_source_ref"`
	EmployeeID         string `json:"employee_id"`
	IdentityBindingID  string `json:"identity_binding_id"`
	AgentID            string `json:"agent_id"`
}

type hiveCosmAuthorityWireSnapshot struct {
	Kind          string `json:"kind"`
	SourceRef     string `json:"source_ref"`
	Revision      string `json:"revision"`
	ContentDigest string `json:"content_digest"`
	Freshness     string `json:"freshness"`
	DisplayName   string `json:"display_name,omitempty"`
	Model         string `json:"model,omitempty"`
}

func (s hiveCosmAuthorityWireSnapshot) authoritySnapshot() AuthoritySnapshot {
	return AuthoritySnapshot{
		Kind:          s.Kind,
		SourceRef:     s.SourceRef,
		Revision:      s.Revision,
		ContentDigest: s.ContentDigest,
		Freshness:     s.Freshness,
		DisplayName:   s.DisplayName,
		Model:         s.Model,
	}
}

type hiveCosmWorkOrderObject struct {
	Authority hiveCosmAuthorityWireSnapshot `json:"authority"`
}

type hiveCosmEmployeeObject struct {
	EmployeeID string                        `json:"employee_id"`
	Authority  hiveCosmAuthorityWireSnapshot `json:"authority"`
}

type hiveCosmIdentityBindingObject struct {
	IdentityBindingID string                        `json:"identity_binding_id"`
	EmployeeID        string                        `json:"employee_id"`
	AgentID           string                        `json:"agent_id"`
	EmployeeRef       string                        `json:"employee_ref"`
	AgentRef          string                        `json:"agent_ref"`
	Active            bool                          `json:"active"`
	Authority         hiveCosmAuthorityWireSnapshot `json:"authority"`
}

type hiveCosmAuthorityWireError struct {
	Code           string `json:"code"`
	ObjectKind     string `json:"object_kind,omitempty"`
	RequestedValue string `json:"requested_value,omitempty"`
}

type hiveCosmAuthorityEnvelope struct {
	SchemaVersion    string                          `json:"schema_version"`
	LookupMode       string                          `json:"lookup_mode"`
	Complete         bool                            `json:"complete"`
	OK               bool                            `json:"ok"`
	Request          hiveCosmAuthorityRequestEcho    `json:"request"`
	WorkOrders       []hiveCosmWorkOrderObject       `json:"work_orders,omitempty"`
	Employees        []hiveCosmEmployeeObject        `json:"employees,omitempty"`
	IdentityBindings []hiveCosmIdentityBindingObject `json:"identity_bindings,omitempty"`
	Error            *hiveCosmAuthorityWireError     `json:"error,omitempty"`
}

func validateHiveCosmAuthorityLookup(lookup HiveCosmAuthorityLookup) error {
	if !hiveCosmWorkOrderSourceRefPattern.MatchString(lookup.WorkOrderSourceRef) {
		return errors.New("work_order_source_ref must be hive://hivecosm/delivery/project/{project}/work-order/{work-order}")
	}
	if err := validateOpaqueAuthorityID("employee_id", lookup.EmployeeID); err != nil {
		return err
	}
	if err := validateOpaqueAuthorityID("identity_binding_id", lookup.IdentityBindingID); err != nil {
		return err
	}
	parsedAgentID, err := uuid.Parse(lookup.AgentID)
	if err != nil || parsedAgentID.String() != lookup.AgentID {
		return errors.New("agent_id must be a canonical UUID")
	}
	return nil
}

func validateOpaqueAuthorityID(field, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is missing or non-canonical", field)
	}
	return nil
}

func validateHiveCosmAuthorityEnvelope(envelope hiveCosmAuthorityEnvelope, lookup HiveCosmAuthorityLookup) (HiveCosmAuthorityBundle, error) {
	if err := validateEnvelopeIdentity(envelope, lookup); err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, err)
	}
	if !envelope.OK || envelope.Error != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("successful authority response must have ok=true and no error"))
	}
	if !envelope.Complete {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("authority response is incomplete"))
	}
	if len(envelope.WorkOrders) > 1 || len(envelope.Employees) > 1 || len(envelope.IdentityBindings) > 1 {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityConflict, http.StatusOK, errors.New("exact authority lookup returned duplicate or conflicting objects"))
	}
	if len(envelope.WorkOrders) != 1 || len(envelope.Employees) != 1 || len(envelope.IdentityBindings) != 1 {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("exact authority lookup must return one WorkOrder, Employee, and IdentityBinding"))
	}

	workOrder := envelope.WorkOrders[0].Authority.authoritySnapshot()
	employeeRecord := envelope.Employees[0]
	employee := employeeRecord.Authority.authoritySnapshot()
	bindingRecord := envelope.IdentityBindings[0]
	binding := IdentityBinding{
		Authority:   bindingRecord.Authority.authoritySnapshot(),
		EmployeeRef: bindingRecord.EmployeeRef,
		AgentRef:    bindingRecord.AgentRef,
		Active:      bindingRecord.Active,
	}

	if err := validateAuthoritySnapshot(workOrder, authorityKindWorkOrder); err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, fmt.Errorf("work order authority: %w", err))
	}
	if workOrder.SourceRef != lookup.WorkOrderSourceRef {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("WorkOrder source_ref does not match the exact request"))
	}
	if employeeRecord.EmployeeID != lookup.EmployeeID {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("Employee ID does not match the exact request"))
	}
	if err := validateAuthoritySnapshot(employee, authorityKindEmployee); err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, fmt.Errorf("employee authority: %w", err))
	}
	if bindingRecord.IdentityBindingID != lookup.IdentityBindingID || bindingRecord.EmployeeID != lookup.EmployeeID || bindingRecord.AgentID != lookup.AgentID {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("IdentityBinding IDs do not match the exact request"))
	}
	if err := validateAuthoritySnapshot(binding.Authority, authorityKindIdentityBinding); err != nil {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, fmt.Errorf("identity binding authority: %w", err))
	}
	if !binding.Active {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("IdentityBinding is not active"))
	}
	if binding.EmployeeRef != employee.SourceRef {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("IdentityBinding employee_ref does not match the exact Employee authority"))
	}
	expectedAgentRef := "/api/agents/" + lookup.AgentID
	if binding.AgentRef != expectedAgentRef {
		return HiveCosmAuthorityBundle{}, authorityFailure(HiveCosmAuthorityInvalid, http.StatusOK, errors.New("IdentityBinding agent_ref does not match the exact HiveCrew Agent API reference"))
	}

	return HiveCosmAuthorityBundle{
		WorkOrder:        workOrder,
		Employee:         employee,
		IdentityBinding:  binding,
		RequestedAgentID: lookup.AgentID,
	}, nil
}

func validateEnvelopeIdentity(envelope hiveCosmAuthorityEnvelope, lookup HiveCosmAuthorityLookup) error {
	if envelope.SchemaVersion == "" {
		return errors.New("schema_version is required")
	}
	if envelope.SchemaVersion != HiveCosmOwnerWorkContextSchemaVersion {
		return fmt.Errorf("unsupported schema_version %q", envelope.SchemaVersion)
	}
	if envelope.LookupMode != "exact" {
		return fmt.Errorf("lookup_mode %q is not exact", envelope.LookupMode)
	}
	if envelope.Request.WorkOrderSourceRef != lookup.WorkOrderSourceRef ||
		envelope.Request.EmployeeID != lookup.EmployeeID ||
		envelope.Request.IdentityBindingID != lookup.IdentityBindingID ||
		envelope.Request.AgentID != lookup.AgentID {
		return errors.New("authority response request echo does not exactly match the request")
	}
	return nil
}

func classifyStructuredNotFound(envelope hiveCosmAuthorityEnvelope, lookup HiveCosmAuthorityLookup, status int) error {
	if err := validateEnvelopeIdentity(envelope, lookup); err != nil {
		return authorityFailure(HiveCosmAuthorityUnsupported, status, errors.New("owner work-context endpoint did not return an exact structured object error"))
	}
	if envelope.Error == nil || envelope.Error.Code != string(HiveCosmAuthorityNotFound) {
		return authorityFailure(HiveCosmAuthorityUnsupported, status, errors.New("owner work-context endpoint is unavailable"))
	}
	if !matchesNotFoundObject(*envelope.Error, lookup) {
		return authorityFailure(HiveCosmAuthorityInvalid, status, errors.New("structured not_found does not identify an exact requested HiveCosm object"))
	}
	return authorityFailure(HiveCosmAuthorityNotFound, status, fmt.Errorf("%s %q was not found", envelope.Error.ObjectKind, envelope.Error.RequestedValue))
}

func matchesNotFoundObject(wireError hiveCosmAuthorityWireError, lookup HiveCosmAuthorityLookup) bool {
	switch wireError.ObjectKind {
	case "work_order":
		return wireError.RequestedValue == lookup.WorkOrderSourceRef
	case "employee":
		return wireError.RequestedValue == lookup.EmployeeID
	case "identity_binding":
		return wireError.RequestedValue == lookup.IdentityBindingID
	default:
		return false
	}
}

func classifyStructuredFailure(envelope hiveCosmAuthorityEnvelope, lookup HiveCosmAuthorityLookup, status int) error {
	if err := validateEnvelopeIdentity(envelope, lookup); err != nil {
		return authorityFailure(HiveCosmAuthorityInvalid, status, err)
	}
	if envelope.Error == nil {
		return authorityFailure(HiveCosmAuthorityInvalid, status, fmt.Errorf("unexpected authority HTTP status %d", status))
	}
	switch {
	case status == http.StatusConflict && envelope.Error.Code == string(HiveCosmAuthorityConflict):
		return authorityFailure(HiveCosmAuthorityConflict, status, errors.New("HiveCosm reported conflicting authority objects"))
	case (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) && envelope.Error.Code == string(HiveCosmAuthorityInvalid):
		return authorityFailure(HiveCosmAuthorityInvalid, status, errors.New("HiveCosm rejected the exact authority lookup"))
	default:
		return authorityFailure(HiveCosmAuthorityInvalid, status, fmt.Errorf("unexpected authority error %q with HTTP status %d", envelope.Error.Code, status))
	}
}

func readCappedAuthorityBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxHiveCosmAuthorityBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read HiveCosm authority response: %w", err)
	}
	if len(data) > maxHiveCosmAuthorityBodySize {
		return nil, errors.New("HiveCosm authority response exceeded the body limit")
	}
	return data, nil
}

func isJSONMediaType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("authority response contains multiple JSON values")
		}
		return fmt.Errorf("read authority response tail: %w", err)
	}
	return nil
}

func authorityFailure(kind HiveCosmAuthorityErrorKind, status int, cause error) error {
	return &HiveCosmAuthorityError{Kind: kind, StatusCode: status, Cause: cause}
}
