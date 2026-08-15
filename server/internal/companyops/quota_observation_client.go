package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/routescore"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// HiveCosmQuotaObservationEndpoint is a read-only Authority projection. It
	// is deliberately not a HiveCrew-local usage endpoint: quota truth belongs
	// to the provider/account owner and must be observed from HiveCosm.
	HiveCosmQuotaObservationEndpoint = "/api/company-ops/quota-observation"
	HiveCosmQuotaObservationSchema   = "hivecosm.quota-observation.v1"

	quotaObservationMaxAge     = 15 * time.Minute
	quotaObservationFutureSkew = 5 * time.Second
)

// QuotaObservationLookup is the exact binding requested from Authority.
// Provider, plan, account, and key are never supplied by the caller: they are
// returned by Authority for the exact agent/runtime pair.
type QuotaObservationLookup struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	RuntimeID   string `json:"runtime_id"`
}

// QuotaObservationWindow describes one provider quota period. ResetAt is the
// provider's next reset boundary, not a locally inferred timestamp.
type QuotaObservationWindow struct {
	Kind     string `json:"kind"`
	StartsAt string `json:"starts_at"`
	ResetAt  string `json:"reset_at"`
}

// QuotaObservation is a read-only, provider/account-scoped quota fact. The
// key_ref is a reference (for example keychain:...) and never a secret.
type QuotaObservation struct {
	AgentID           string                 `json:"agent_id"`
	RuntimeID         string                 `json:"runtime_id"`
	Provider          string                 `json:"provider"`
	Plan              string                 `json:"plan"`
	Model             string                 `json:"model"`
	BillingAccountRef string                 `json:"billing_account_ref"`
	KeyRef            string                 `json:"key_ref"`
	Window            QuotaObservationWindow `json:"window"`
	Unit              string                 `json:"unit"`
	Limit             int64                  `json:"limit"`
	Used              int64                  `json:"used"`
	Remaining         int64                  `json:"remaining"`
	Ratio             float64                `json:"ratio"`
	ObservedAt        string                 `json:"observed_at"`
	ExpiresAt         string                 `json:"expires_at"`
	EvidenceState     string                 `json:"evidence_state"`
	EvidenceRef       string                 `json:"evidence_ref"`
	SourceRef         string                 `json:"source_ref"`
	SourceRevision    string                 `json:"source_revision"`
	AuthorityState    string                 `json:"authority_state"`
}

// HiveCosmQuotaObservationResponse is the only wire shape accepted by this
// consumer. DisallowUnknownFields below applies recursively to Window too.
type HiveCosmQuotaObservationResponse struct {
	SchemaVersion string                 `json:"schema_version"`
	OK            bool                   `json:"ok"`
	TenantID      string                 `json:"tenant_id"`
	WorkspaceID   string                 `json:"workspace_id"`
	Request       QuotaObservationLookup `json:"request"`
	Observation   QuotaObservation       `json:"observation"`
}

// QuotaObservationSnapshot is the package-local adapter shape. A future
// service adapter can map State/CheckedAt/AccountRef to
// service.ShadowQuotaSnapshot without importing service into companyops.
type QuotaObservationSnapshot struct {
	State       routescore.QuotaState
	CheckedAt   time.Time
	AccountRef  string
	Observation QuotaObservation
}

// HiveCosmQuotaObservationClient performs one authenticated GET and no
// writes. The caller supplies the existing origin-pinned CompanyOps HTTP
// client; this type never reads, stores, or prints a bearer value.
type HiveCosmQuotaObservationClient struct {
	baseURL    *url.URL
	httpClient *http.Client
	tenantID   string
	now        func() time.Time
}

// NewHiveCosmQuotaObservationClient constructs a strict read-only client.
func NewHiveCosmQuotaObservationClient(baseURL string, httpClient *http.Client, tenantID string) (*HiveCosmQuotaObservationClient, error) {
	if !canonicalNonblank(tenantID) || len(tenantID) > 256 || strings.ContainsAny(tenantID, "\r\n") {
		return nil, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("HIVECOSM_TENANT_ID is required"))
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("invalid adapter base URL"))
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHiveCosmAuthorityTimeout}
	}
	// Preserve the caller's origin-pinned transport and timeout, but never
	// follow a Location header to a different host. A quota observation is an
	// Authority fact for this configured origin only; redirect handling could
	// otherwise leak the injected Authorization header across an origin change.
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &HiveCosmQuotaObservationClient{baseURL: u, httpClient: &clientCopy, tenantID: tenantID, now: time.Now}, nil
}

// Lookup validates the local Agent -> Runtime binding, then reads the exact
// Authority observation. It is intentionally shape-compatible in method name
// and inputs with ContinuousDispatchQuotaSource; the return type remains
// companyops-owned so this boundary does not create a package cycle.
func (c *HiveCosmQuotaObservationClient) Lookup(ctx context.Context, agent db.Agent, runtime db.AgentRuntime) (QuotaObservationSnapshot, error) {
	lookup, provider, model, err := localQuotaBinding(agent, runtime)
	if err != nil {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, 0, err)
	}
	return c.resolve(ctx, lookup, provider, model)
}

func (c *HiveCosmQuotaObservationClient) resolve(ctx context.Context, lookup QuotaObservationLookup, provider, model string) (QuotaObservationSnapshot, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("quota observation source is not configured"))
	}
	if err := validateQuotaObservationLookup(lookup); err != nil {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, 0, err)
	}
	endpoint := *c.baseURL
	endpoint.Path = HiveCosmQuotaObservationEndpoint
	endpoint.RawPath = ""
	query := endpoint.Query()
	query.Set("workspace_id", lookup.WorkspaceID)
	query.Set("agent_id", lookup.AgentID)
	query.Set("runtime_id", lookup.RuntimeID)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("quota observation request could not be created"))
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, 0, errors.New("quota observation source is unreachable"))
	}
	defer resp.Body.Close()
	body, err := readCappedAuthorityBody(resp.Body)
	if err != nil {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("quota observation response is unavailable"))
	}
	// Never parse non-200 bodies. Provider error bodies may contain bearer
	// material or other secrets and are not part of this contract.
	if resp.StatusCode != http.StatusOK {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("quota observation source did not provide an eligible observation"))
	}
	if !isJSONMediaType(resp.Header.Get("Content-Type")) || !json.Valid(body) {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("quota observation response is not JSON"))
	}
	var envelope HiveCosmQuotaObservationResponse
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || ensureJSONEOF(decoder) != nil {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("quota observation response shape is invalid"))
	}
	now := c.now().UTC()
	if err := validateQuotaObservationEnvelope(envelope, lookup, provider, model, c.tenantID, now); err != nil {
		return QuotaObservationSnapshot{}, quotaObservationFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, err)
	}
	observedAt, _ := parseQuotaObservationTime(envelope.Observation.ObservedAt, now, true)
	state := routescore.QuotaFresh
	if envelope.Observation.Remaining == 0 {
		state = routescore.QuotaExhausted
	}
	return QuotaObservationSnapshot{State: state, CheckedAt: observedAt, AccountRef: envelope.Observation.BillingAccountRef, Observation: envelope.Observation}, nil
}

func localQuotaBinding(agent db.Agent, runtime db.AgentRuntime) (QuotaObservationLookup, string, string, error) {
	agentID, err := canonicalDBUUID(agent.ID)
	if err != nil {
		return QuotaObservationLookup{}, "", "", errors.New("agent identity is invalid")
	}
	runtimeID, err := canonicalDBUUID(runtime.ID)
	if err != nil {
		return QuotaObservationLookup{}, "", "", errors.New("runtime identity is invalid")
	}
	workspaceID, err := canonicalDBUUID(agent.WorkspaceID)
	if err != nil {
		return QuotaObservationLookup{}, "", "", errors.New("agent workspace identity is invalid")
	}
	runtimeWorkspaceID, err := canonicalDBUUID(runtime.WorkspaceID)
	if err != nil || runtimeWorkspaceID != workspaceID {
		return QuotaObservationLookup{}, "", "", errors.New("agent and runtime workspace binding is not exact")
	}
	if !agent.RuntimeID.Valid || agent.RuntimeID != runtime.ID {
		return QuotaObservationLookup{}, "", "", errors.New("agent and runtime binding is not exact")
	}
	if agent.ArchivedAt.Valid {
		return QuotaObservationLookup{}, "", "", errors.New("archived agent is not quota eligible")
	}
	provider := strings.TrimSpace(runtime.Provider)
	if !canonicalNonblank(provider) || len(provider) > 256 || strings.ContainsAny(provider, "\r\n") {
		return QuotaObservationLookup{}, "", "", errors.New("runtime provider is invalid")
	}
	if !agent.Model.Valid {
		return QuotaObservationLookup{}, "", "", errors.New("agent model is unavailable")
	}
	model := agent.Model.String
	if !canonicalNonblank(model) || len(model) > 256 || strings.ContainsAny(model, "\r\n") {
		return QuotaObservationLookup{}, "", "", errors.New("agent model is invalid")
	}
	return QuotaObservationLookup{WorkspaceID: workspaceID, AgentID: agentID, RuntimeID: runtimeID}, provider, model, nil
}

func validateQuotaObservationEnvelope(response HiveCosmQuotaObservationResponse, lookup QuotaObservationLookup, expectedProvider, expectedModel, tenantID string, now time.Time) error {
	if response.SchemaVersion != HiveCosmQuotaObservationSchema || !response.OK || response.TenantID != tenantID || response.WorkspaceID != lookup.WorkspaceID || response.Request != lookup {
		return errors.New("quota observation identity is not exact")
	}
	o := response.Observation
	if o.AgentID != lookup.AgentID || o.RuntimeID != lookup.RuntimeID || o.Provider != expectedProvider || (expectedModel != "" && o.Model != expectedModel) {
		return errors.New("quota observation agent/runtime/provider binding is not exact")
	}
	for name, value := range map[string]string{"provider": o.Provider, "plan": o.Plan, "model": o.Model} {
		if err := validateQuotaLabel(name, value); err != nil {
			return err
		}
	}
	if err := validateQuotaReference("billing_account_ref", o.BillingAccountRef, "billing:", "account:"); err != nil {
		return err
	}
	if err := validateQuotaReference("key_ref", o.KeyRef, "keychain:", "env:", "vault:"); err != nil {
		return err
	}
	// The governed quota dimensions are request-count caps from the agent
	// model assignment registry; token-denominated provider quotas may join
	// later as an additional unit, not a replacement.
	if (o.Unit != "tokens" && o.Unit != "requests") || o.Limit <= 0 || o.Used < 0 || o.Remaining < 0 || o.Used > o.Limit || o.Remaining != o.Limit-o.Used || math.IsNaN(o.Ratio) || math.IsInf(o.Ratio, 0) || o.Ratio < 0 || o.Ratio > 1 {
		return errors.New("quota observation amounts are invalid")
	}
	expectedRatio := float64(o.Used) / float64(o.Limit)
	if math.Abs(expectedRatio-o.Ratio) > 1e-9 {
		return errors.New("quota observation ratio does not match amounts")
	}
	if !validQuotaWindowKind(o.Window.Kind) {
		return errors.New("quota observation window kind is invalid")
	}
	startsAt, err := parseQuotaObservationTime(o.Window.StartsAt, now, false)
	if err != nil || startsAt.After(now.Add(quotaObservationFutureSkew)) {
		return errors.New("quota observation window starts_at is invalid")
	}
	resetAt, err := parseQuotaObservationTime(o.Window.ResetAt, now, false)
	if err != nil || !resetAt.After(now) {
		return errors.New("quota observation reset_at is invalid or expired")
	}
	observedAt, err := parseQuotaObservationTime(o.ObservedAt, now, true)
	if err != nil || observedAt.Before(startsAt) || observedAt.After(resetAt) {
		return errors.New("quota observation observed_at is invalid")
	}
	expiresAt, err := parseQuotaObservationTime(o.ExpiresAt, now, false)
	if err != nil || !expiresAt.After(now) || !expiresAt.After(observedAt) || expiresAt.After(resetAt) {
		return errors.New("quota observation expires_at is invalid or expired")
	}
	if o.EvidenceState != "verified" || o.AuthorityState != "authoritative" {
		return errors.New("quota observation evidence is not authoritative")
	}
	if err := validateQuotaSourceRef("evidence_ref", o.EvidenceRef); err != nil {
		return err
	}
	if err := validateQuotaSourceRef("source_ref", o.SourceRef); err != nil {
		return err
	}
	if err := validateSHA256Digest(o.SourceRevision); err != nil {
		return errors.New("quota observation source_revision is invalid")
	}
	return nil
}

func validateQuotaObservationLookup(lookup QuotaObservationLookup) error {
	for name, value := range map[string]string{"workspace_id": lookup.WorkspaceID, "agent_id": lookup.AgentID, "runtime_id": lookup.RuntimeID} {
		if !canonicalNonblank(value) || len(value) > 256 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("quota observation %s is malformed", name)
		}
	}
	return nil
}

func validateQuotaLabel(name, value string) error {
	if !canonicalNonblank(value) || len(value) > 256 || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("quota observation %s is malformed", name)
	}
	return nil
}

func validateQuotaReference(name, value string, prefixes ...string) error {
	if !canonicalNonblank(value) || len(value) > 512 || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("quota observation %s is malformed", name)
	}
	matched := false
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			matched = true
			break
		}
	}
	if !matched || strings.Contains(strings.ToLower(value), "bearer") || strings.Contains(strings.ToLower(value), "token=") || strings.Contains(value, "sk-") {
		return fmt.Errorf("quota observation %s must be a non-secret reference", name)
	}
	return nil
}

func validateQuotaSourceRef(name, value string) error {
	if err := validateSourceRef(value); err != nil {
		return fmt.Errorf("quota observation %s is invalid", name)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "hive" {
		return fmt.Errorf("quota observation %s is invalid", name)
	}
	return nil
}

func validQuotaWindowKind(kind string) bool {
	switch kind {
	case "hour", "day", "7d", "month", "billing_cycle", "rolling_7d":
		return true
	default:
		return false
	}
}

func parseQuotaObservationTime(value string, now time.Time, fresh bool) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("timestamp is not canonical UTC RFC3339Nano")
	}
	if fresh && parsed.After(now.Add(quotaObservationFutureSkew)) {
		return time.Time{}, errors.New("timestamp is in the future")
	}
	if fresh && now.Sub(parsed) > quotaObservationMaxAge {
		return time.Time{}, errors.New("timestamp is stale")
	}
	return parsed, nil
}

func canonicalDBUUID(value pgtype.UUID) (string, error) {
	if !value.Valid {
		return "", errors.New("uuid is not valid")
	}
	parsed, err := uuid.FromBytes(value.Bytes[:])
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func quotaObservationFailure(kind HiveCosmAuthorityErrorKind, status int, cause error) error {
	return &HiveCosmAuthorityError{Kind: kind, StatusCode: status, Cause: cause}
}
