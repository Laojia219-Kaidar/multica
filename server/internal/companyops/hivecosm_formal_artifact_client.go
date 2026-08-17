package companyops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const (
	HiveCosmFormalArtifactPromotionEndpoint   = "/api/company-ops/formal-artifacts/promotions"
	HiveCosmFormalArtifactReadEndpointPrefix  = "/api/company-ops/formal-artifacts/"
	HiveCosmFormalArtifactPromotionCommandV1  = "hivecrew.formal-artifact-promotion.command.v1"
	HiveCosmFormalArtifactPromotionReceiptV1  = "hivecrew.formal-artifact-promotion.receipt.v1"
	HiveCosmFormalArtifactAuthorityV1         = "hivecosm.formal-artifact.authority.v1"
	HiveCosmFormalArtifactContentTypeMarkdown = "text/markdown"
)

var (
	formalArtifactDigestPattern         = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	formalArtifactIDPattern             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}$`)
	formalArtifactWorkOrderScopePattern = regexp.MustCompile(
		`^hive://hivecosm/delivery/project/([A-Za-z0-9][A-Za-z0-9@._:-]{0,191})/work-order/([A-Za-z0-9][A-Za-z0-9@._:-]{0,191})$`,
	)
)

// FormalArtifactManifestID returns Authority v1's stable manifest identity.
func FormalArtifactManifestID(promotionID string) (string, error) {
	parsed, err := uuid.Parse(promotionID)
	if err != nil || parsed.String() != promotionID {
		return "", errors.New("promotion_id must be a canonical UUID")
	}
	return "FA-HCW-" + strings.ToUpper(promotionID), nil
}

type HiveCosmFormalArtifactCandidate struct {
	ID               string
	Revision         int
	DurableObjectRef string
	ContentDigest    string
	ApprovalEventID  string
}

type HiveCosmFormalArtifactPromotionRequest struct {
	PromotionID     string
	Lookup          HiveCosmAuthorityLookup
	WorkOrder       AuthoritySnapshot
	Employee        AuthoritySnapshot
	IdentityBinding AuthoritySnapshot
	Candidate       HiveCosmFormalArtifactCandidate
}

type HiveCosmFormalArtifact struct {
	FormalArtifactRef   string
	Revision            string
	ContentDigest       string
	ProjectID           string
	WorkOrderID         string
	AssignmentID        string
	EmployeeID          string
	AgentID             string
	IdentityBindingID   string
	ArtifactManifestID  string
	ContentObjectID     string
	ContentRef          string
	CandidateID         string
	CandidateRevision   int
	CandidateDigest     string
	ReviewDecisionID    string
	ReviewerID          string
	ApprovalEventID     string
	WorkOrderTransition *HiveCosmWorkOrderTransitionProof
}

type HiveCosmAuthorityTransitionSnapshot struct {
	Revision      string
	ContentDigest string
}

// HiveCosmWorkOrderTransitionProof is the authority-issued bridge between the
// immutable WorkOrder observation linked by HiveCrew and the current WorkOrder
// observation after Formal Artifact Promotion self-advances that authority.
// It is returned only by the Formal Artifact GET readback and never mutates the
// local external_work_order_link.
type HiveCosmWorkOrderTransitionProof struct {
	WorkOrderSourceRef string
	PreviousAuthority  HiveCosmAuthorityTransitionSnapshot
	ResultingAuthority HiveCosmAuthorityTransitionSnapshot
	PromotionID        string
	CandidateID        string
	ApprovalEventID    string
	FormalArtifactRef  string
}

type HiveCosmFormalArtifactPromotionReceipt struct {
	PromotionID    string
	WritePerformed bool
	Artifact       HiveCosmFormalArtifact
}

type formalArtifactExpectedAuthorityWire struct {
	Revision      string `json:"revision"`
	ContentDigest string `json:"content_digest"`
}

type formalArtifactPromotionCommandWire struct {
	SchemaVersion string                           `json:"schema_version"`
	PromotionID   string                           `json:"promotion_id"`
	Request       hiveCosmAuthorityRequestEcho     `json:"request"`
	Expected      formalArtifactExpectedBundleWire `json:"expected_authority"`
	Artifact      formalArtifactCandidateWire      `json:"temporary_artifact"`
}

type formalArtifactExpectedBundleWire struct {
	WorkOrder       formalArtifactExpectedAuthorityWire `json:"work_order"`
	Employee        formalArtifactExpectedAuthorityWire `json:"employee"`
	IdentityBinding formalArtifactExpectedAuthorityWire `json:"identity_binding"`
}

type formalArtifactCandidateWire struct {
	CandidateID      string `json:"candidate_id"`
	Revision         int    `json:"revision"`
	DurableObjectRef string `json:"durable_object_ref"`
	ContentDigest    string `json:"content_digest"`
	ContentType      string `json:"content_type"`
	ApprovalEventID  string `json:"approval_event_id"`
}

type formalArtifactTemporaryWire struct {
	CandidateID   string `json:"candidate_id"`
	Revision      int    `json:"revision"`
	ContentDigest string `json:"content_digest"`
}

type formalArtifactOwnerReviewWire struct {
	ReviewDecisionID string `json:"review_decision_id"`
	ReviewerID       string `json:"reviewer_id"`
	Decision         string `json:"decision"`
	ApprovalEventID  string `json:"approval_event_id"`
}

type formalArtifactWorkOrderTransitionWire struct {
	WorkOrderSourceRef string                              `json:"work_order_source_ref"`
	PreviousAuthority  formalArtifactExpectedAuthorityWire `json:"previous_authority"`
	ResultingAuthority formalArtifactExpectedAuthorityWire `json:"resulting_authority"`
	PromotionID        string                              `json:"promotion_id"`
	CandidateID        string                              `json:"candidate_id"`
	ApprovalEventID    string                              `json:"approval_event_id"`
	FormalArtifactRef  string                              `json:"formal_artifact_ref"`
}

type formalArtifactAuthorityWire struct {
	SchemaVersion      string                        `json:"schema_version"`
	FormalArtifactRef  string                        `json:"formal_artifact_ref"`
	Revision           string                        `json:"revision"`
	ContentDigest      string                        `json:"content_digest"`
	Freshness          string                        `json:"freshness"`
	Status             string                        `json:"status"`
	ProjectID          string                        `json:"project_id"`
	WorkOrderID        string                        `json:"work_order_id"`
	AssignmentID       string                        `json:"assignment_id"`
	EmployeeID         string                        `json:"employee_id"`
	AgentID            string                        `json:"agent_id"`
	IdentityBindingID  string                        `json:"identity_binding_id"`
	ArtifactManifestID string                        `json:"artifact_manifest_id"`
	ContentObjectID    string                        `json:"content_object_id"`
	ContentRef         string                        `json:"content_ref"`
	TemporaryArtifact  formalArtifactTemporaryWire   `json:"temporary_artifact"`
	OwnerReview        formalArtifactOwnerReviewWire `json:"owner_review"`
}

type formalArtifactPromotionEnvelope struct {
	SchemaVersion  string                      `json:"schema_version"`
	PromotionID    string                      `json:"promotion_id"`
	WritePerformed bool                        `json:"write_performed"`
	Artifact       formalArtifactAuthorityWire `json:"formal_artifact"`
	OK             *bool                       `json:"ok,omitempty"`
	Error          *hiveCosmAuthorityWireError `json:"error,omitempty"`
}

type formalArtifactReadbackEnvelope struct {
	SchemaVersion       string                                 `json:"schema_version"`
	LookupMode          string                                 `json:"lookup_mode"`
	Complete            bool                                   `json:"complete"`
	OK                  bool                                   `json:"ok"`
	Request             hiveCosmAuthorityRequestEcho           `json:"request"`
	Artifact            formalArtifactAuthorityWire            `json:"formal_artifact"`
	WorkOrderTransition *formalArtifactWorkOrderTransitionWire `json:"work_order_transition"`
	Error               *hiveCosmAuthorityWireError            `json:"error,omitempty"`
}

func (c *HiveCosmAuthorityClient) PromoteFormalArtifact(
	ctx context.Context,
	input HiveCosmFormalArtifactPromotionRequest,
) (HiveCosmFormalArtifactPromotionReceipt, error) {
	if err := validateFormalArtifactPromotionInput(input); err != nil {
		return HiveCosmFormalArtifactPromotionReceipt{}, authorityFailure(HiveCosmAuthorityInvalid, 0, err)
	}
	wire := formalArtifactPromotionCommandWire{
		SchemaVersion: HiveCosmFormalArtifactPromotionCommandV1,
		PromotionID:   input.PromotionID,
		Request:       formalArtifactLookupWire(input.Lookup),
		Expected: formalArtifactExpectedBundleWire{
			WorkOrder:       formalArtifactExpectedWire(input.WorkOrder),
			Employee:        formalArtifactExpectedWire(input.Employee),
			IdentityBinding: formalArtifactExpectedWire(input.IdentityBinding),
		},
		Artifact: formalArtifactCandidateWire{
			CandidateID:      input.Candidate.ID,
			Revision:         input.Candidate.Revision,
			DurableObjectRef: input.Candidate.DurableObjectRef,
			ContentDigest:    input.Candidate.ContentDigest,
			ContentType:      HiveCosmFormalArtifactContentTypeMarkdown,
			ApprovalEventID:  input.Candidate.ApprovalEventID,
		},
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return HiveCosmFormalArtifactPromotionReceipt{}, authorityFailure(HiveCosmAuthorityInvalid, 0, err)
	}
	status, responseBody, err := c.doFormalArtifactRequest(ctx, http.MethodPost, HiveCosmFormalArtifactPromotionEndpoint, nil, body)
	if err != nil {
		return HiveCosmFormalArtifactPromotionReceipt{}, err
	}
	var envelope formalArtifactPromotionEnvelope
	if err := decodeExactJSON(responseBody, &envelope); err != nil {
		return HiveCosmFormalArtifactPromotionReceipt{}, authorityFailure(HiveCosmAuthorityInvalid, status, fmt.Errorf("decode formal Artifact Promotion receipt: %w", err))
	}
	if status != http.StatusOK {
		return HiveCosmFormalArtifactPromotionReceipt{}, classifyFormalArtifactFailure(status, envelope.Error)
	}
	if envelope.SchemaVersion != HiveCosmFormalArtifactPromotionReceiptV1 || envelope.PromotionID != input.PromotionID || envelope.Error != nil {
		return HiveCosmFormalArtifactPromotionReceipt{}, authorityFailure(HiveCosmAuthorityInvalid, status, errors.New("formal Artifact Promotion receipt identity is invalid"))
	}
	expectedManifestID, manifestErr := FormalArtifactManifestID(input.PromotionID)
	if manifestErr != nil {
		return HiveCosmFormalArtifactPromotionReceipt{}, authorityFailure(HiveCosmAuthorityInvalid, status, manifestErr)
	}
	artifact, err := validateFormalArtifactAuthority(envelope.Artifact, input.Lookup, input.Candidate, expectedManifestID)
	if err != nil {
		return HiveCosmFormalArtifactPromotionReceipt{}, authorityFailure(HiveCosmAuthorityInvalid, status, err)
	}
	return HiveCosmFormalArtifactPromotionReceipt{
		PromotionID:    envelope.PromotionID,
		WritePerformed: envelope.WritePerformed,
		Artifact:       artifact,
	}, nil
}

func (c *HiveCosmAuthorityClient) ReadFormalArtifact(
	ctx context.Context,
	lookup HiveCosmAuthorityLookup,
	expectedCandidate HiveCosmFormalArtifactCandidate,
	artifactManifestID string,
) (HiveCosmFormalArtifact, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("HiveCosm authority client is not configured"))
	}
	if err := validateHiveCosmAuthorityLookup(lookup); err != nil {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, 0, err)
	}
	if err := validateFormalArtifactCandidate(expectedCandidate); err != nil {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, 0, err)
	}
	if !formalArtifactIDPattern.MatchString(artifactManifestID) {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("formal Artifact ID is not canonical"))
	}
	query := make(url.Values, 4)
	query.Set("work_order_source_ref", lookup.WorkOrderSourceRef)
	query.Set("employee_id", lookup.EmployeeID)
	query.Set("identity_binding_id", lookup.IdentityBindingID)
	query.Set("agent_id", lookup.AgentID)
	status, responseBody, err := c.doFormalArtifactRequest(
		ctx,
		http.MethodGet,
		HiveCosmFormalArtifactReadEndpointPrefix+url.PathEscape(artifactManifestID),
		query,
		nil,
	)
	if err != nil {
		return HiveCosmFormalArtifact{}, err
	}
	var envelope formalArtifactReadbackEnvelope
	if err := decodeExactJSON(responseBody, &envelope); err != nil {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, status, fmt.Errorf("decode formal Artifact readback: %w", err))
	}
	if status != http.StatusOK {
		return HiveCosmFormalArtifact{}, classifyFormalArtifactFailure(status, envelope.Error)
	}
	if envelope.SchemaVersion != HiveCosmFormalArtifactAuthorityV1 || envelope.LookupMode != "exact" || !envelope.Complete || !envelope.OK || envelope.Error != nil {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, status, errors.New("formal Artifact readback envelope is invalid"))
	}
	if envelope.Request != formalArtifactLookupWire(lookup) {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, status, errors.New("formal Artifact readback request echo changed"))
	}
	artifact, err := validateFormalArtifactAuthority(envelope.Artifact, lookup, expectedCandidate, artifactManifestID)
	if err != nil {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, status, err)
	}
	transition, err := validateFormalArtifactWorkOrderTransition(envelope.WorkOrderTransition, lookup, expectedCandidate, artifact.FormalArtifactRef)
	if err != nil {
		return HiveCosmFormalArtifact{}, authorityFailure(HiveCosmAuthorityInvalid, status, err)
	}
	artifact.WorkOrderTransition = &transition
	return artifact, nil
}

func (c *HiveCosmAuthorityClient) doFormalArtifactRequest(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body []byte,
) (int, []byte, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return 0, nil, authorityFailure(HiveCosmAuthorityInvalid, 0, errors.New("HiveCosm authority client is not configured"))
	}
	endpoint := *c.baseURL
	endpoint.Path = path
	endpoint.RawPath = ""
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, authorityFailure(HiveCosmAuthorityInvalid, 0, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, authorityFailure(HiveCosmAuthoritySourceGap, 0, fmt.Errorf("HiveCosm formal Artifact transport: %w", err))
	}
	defer resp.Body.Close()
	responseBody, err := readCappedAuthorityBody(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, authorityFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, nil, authorityFailure(HiveCosmAuthoritySourceGap, resp.StatusCode, errors.New("HiveCosm formal Artifact authentication is unavailable"))
	}
	if !isJSONMediaType(resp.Header.Get("Content-Type")) || !json.Valid(responseBody) {
		kind := HiveCosmAuthoritySourceGap
		if resp.StatusCode == http.StatusNotFound {
			kind = HiveCosmAuthorityUnsupported
		}
		return resp.StatusCode, nil, authorityFailure(kind, resp.StatusCode, errors.New("HiveCosm formal Artifact response is not JSON"))
	}
	return resp.StatusCode, responseBody, nil
}

func formalArtifactLookupWire(lookup HiveCosmAuthorityLookup) hiveCosmAuthorityRequestEcho {
	return hiveCosmAuthorityRequestEcho{
		WorkOrderSourceRef: lookup.WorkOrderSourceRef,
		EmployeeID:         lookup.EmployeeID,
		IdentityBindingID:  lookup.IdentityBindingID,
		AgentID:            lookup.AgentID,
	}
}

func formalArtifactExpectedWire(snapshot AuthoritySnapshot) formalArtifactExpectedAuthorityWire {
	return formalArtifactExpectedAuthorityWire{Revision: snapshot.Revision, ContentDigest: snapshot.ContentDigest}
}

func validateFormalArtifactPromotionInput(input HiveCosmFormalArtifactPromotionRequest) error {
	if err := validateHiveCosmAuthorityLookup(input.Lookup); err != nil {
		return err
	}
	if parsed, err := uuid.Parse(input.PromotionID); err != nil || parsed.String() != input.PromotionID {
		return errors.New("promotion_id must be a canonical UUID")
	}
	if err := validateFormalArtifactCandidate(input.Candidate); err != nil {
		return err
	}
	for kind, snapshot := range map[string]AuthoritySnapshot{
		"WorkOrder":       input.WorkOrder,
		"Employee":        input.Employee,
		"IdentityBinding": input.IdentityBinding,
	} {
		if !formalArtifactDigestPattern.MatchString(snapshot.Revision) || !formalArtifactDigestPattern.MatchString(snapshot.ContentDigest) {
			return fmt.Errorf("%s expected authority digest is invalid", kind)
		}
	}
	return nil
}

func validateFormalArtifactCandidate(candidate HiveCosmFormalArtifactCandidate) error {
	if parsed, err := uuid.Parse(candidate.ID); err != nil || parsed.String() != candidate.ID {
		return errors.New("candidate_id must be a canonical UUID")
	}
	if parsed, err := uuid.Parse(candidate.ApprovalEventID); err != nil || parsed.String() != candidate.ApprovalEventID {
		return errors.New("approval_event_id must be a canonical UUID")
	}
	if candidate.Revision < 1 || strings.TrimSpace(candidate.DurableObjectRef) == "" || strings.TrimSpace(candidate.DurableObjectRef) != candidate.DurableObjectRef {
		return errors.New("temporary Artifact revision or object reference is invalid")
	}
	if !formalArtifactDigestPattern.MatchString(candidate.ContentDigest) {
		return errors.New("temporary Artifact content digest is invalid")
	}
	return nil
}

func validateFormalArtifactAuthority(
	wire formalArtifactAuthorityWire,
	lookup HiveCosmAuthorityLookup,
	expectedCandidate HiveCosmFormalArtifactCandidate,
	expectedArtifactID string,
) (HiveCosmFormalArtifact, error) {
	parsedWorkOrder := formalArtifactWorkOrderScopePattern.FindStringSubmatch(lookup.WorkOrderSourceRef)
	if len(parsedWorkOrder) != 3 {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact WorkOrder lookup is invalid")
	}
	if wire.SchemaVersion != HiveCosmFormalArtifactAuthorityV1 || wire.Freshness != "current" || wire.Status != "formal" {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact authority state is invalid")
	}
	if !formalArtifactIDPattern.MatchString(wire.ArtifactManifestID) || (expectedArtifactID != "" && wire.ArtifactManifestID != expectedArtifactID) {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact manifest identity changed")
	}
	expectedRef := lookup.WorkOrderSourceRef + "/formal-artifact/" + wire.ArtifactManifestID
	if wire.FormalArtifactRef != expectedRef || wire.ProjectID != parsedWorkOrder[1] || wire.WorkOrderID != parsedWorkOrder[2] || wire.EmployeeID != lookup.EmployeeID || wire.AgentID != lookup.AgentID || wire.IdentityBindingID != lookup.IdentityBindingID {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact authority scope changed")
	}
	if !formalArtifactDigestPattern.MatchString(wire.Revision) || wire.Revision != wire.ContentDigest {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact authority revision or digest is invalid")
	}
	if wire.AssignmentID == "" || wire.ContentObjectID == "" || wire.ContentRef == "" || wire.OwnerReview.ReviewDecisionID == "" || wire.OwnerReview.ReviewerID == "" || wire.OwnerReview.Decision != "accept" {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact authority references are incomplete")
	}
	if parsed, err := uuid.Parse(wire.TemporaryArtifact.CandidateID); err != nil || parsed.String() != wire.TemporaryArtifact.CandidateID || wire.TemporaryArtifact.Revision < 1 || !formalArtifactDigestPattern.MatchString(wire.TemporaryArtifact.ContentDigest) {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact temporary provenance is invalid")
	}
	if parsed, err := uuid.Parse(wire.OwnerReview.ApprovalEventID); err != nil || parsed.String() != wire.OwnerReview.ApprovalEventID {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact approval provenance is invalid")
	}
	if expectedCandidate.ID != "" && (wire.TemporaryArtifact.CandidateID != expectedCandidate.ID ||
		wire.TemporaryArtifact.Revision != expectedCandidate.Revision ||
		wire.TemporaryArtifact.ContentDigest != expectedCandidate.ContentDigest ||
		wire.ContentRef != expectedCandidate.DurableObjectRef ||
		wire.OwnerReview.ApprovalEventID != expectedCandidate.ApprovalEventID) {
		return HiveCosmFormalArtifact{}, errors.New("formal Artifact does not match the approved temporary Artifact")
	}
	return HiveCosmFormalArtifact{
		FormalArtifactRef:  wire.FormalArtifactRef,
		Revision:           wire.Revision,
		ContentDigest:      wire.ContentDigest,
		ProjectID:          wire.ProjectID,
		WorkOrderID:        wire.WorkOrderID,
		AssignmentID:       wire.AssignmentID,
		EmployeeID:         wire.EmployeeID,
		AgentID:            wire.AgentID,
		IdentityBindingID:  wire.IdentityBindingID,
		ArtifactManifestID: wire.ArtifactManifestID,
		ContentObjectID:    wire.ContentObjectID,
		ContentRef:         wire.ContentRef,
		CandidateID:        wire.TemporaryArtifact.CandidateID,
		CandidateRevision:  wire.TemporaryArtifact.Revision,
		CandidateDigest:    wire.TemporaryArtifact.ContentDigest,
		ReviewDecisionID:   wire.OwnerReview.ReviewDecisionID,
		ReviewerID:         wire.OwnerReview.ReviewerID,
		ApprovalEventID:    wire.OwnerReview.ApprovalEventID,
	}, nil
}

func validateFormalArtifactWorkOrderTransition(
	wire *formalArtifactWorkOrderTransitionWire,
	lookup HiveCosmAuthorityLookup,
	expectedCandidate HiveCosmFormalArtifactCandidate,
	expectedFormalArtifactRef string,
) (HiveCosmWorkOrderTransitionProof, error) {
	if wire == nil {
		return HiveCosmWorkOrderTransitionProof{}, errors.New("formal Artifact readback is missing the WorkOrder transition proof")
	}
	if wire.WorkOrderSourceRef != lookup.WorkOrderSourceRef {
		return HiveCosmWorkOrderTransitionProof{}, errors.New("formal Artifact WorkOrder transition identity changed")
	}
	for name, snapshot := range map[string]formalArtifactExpectedAuthorityWire{
		"previous":  wire.PreviousAuthority,
		"resulting": wire.ResultingAuthority,
	} {
		if !formalArtifactDigestPattern.MatchString(snapshot.Revision) || !formalArtifactDigestPattern.MatchString(snapshot.ContentDigest) {
			return HiveCosmWorkOrderTransitionProof{}, fmt.Errorf("formal Artifact WorkOrder transition %s authority is invalid", name)
		}
	}
	if parsed, err := uuid.Parse(wire.PromotionID); err != nil || parsed.String() != wire.PromotionID {
		return HiveCosmWorkOrderTransitionProof{}, errors.New("formal Artifact WorkOrder transition promotion_id is invalid")
	}
	if wire.CandidateID != expectedCandidate.ID || wire.ApprovalEventID != expectedCandidate.ApprovalEventID || wire.FormalArtifactRef != expectedFormalArtifactRef {
		return HiveCosmWorkOrderTransitionProof{}, errors.New("formal Artifact WorkOrder transition provenance changed")
	}
	return HiveCosmWorkOrderTransitionProof{
		WorkOrderSourceRef: wire.WorkOrderSourceRef,
		PreviousAuthority: HiveCosmAuthorityTransitionSnapshot{
			Revision:      wire.PreviousAuthority.Revision,
			ContentDigest: wire.PreviousAuthority.ContentDigest,
		},
		ResultingAuthority: HiveCosmAuthorityTransitionSnapshot{
			Revision:      wire.ResultingAuthority.Revision,
			ContentDigest: wire.ResultingAuthority.ContentDigest,
		},
		PromotionID:       wire.PromotionID,
		CandidateID:       wire.CandidateID,
		ApprovalEventID:   wire.ApprovalEventID,
		FormalArtifactRef: wire.FormalArtifactRef,
	}, nil
}

func classifyFormalArtifactFailure(status int, wireError *hiveCosmAuthorityWireError) error {
	if wireError == nil {
		return authorityFailure(HiveCosmAuthorityInvalid, status, fmt.Errorf("unexpected formal Artifact HTTP status %d", status))
	}
	switch {
	case status == http.StatusNotFound && wireError.Code == string(HiveCosmAuthorityNotFound):
		return authorityFailure(HiveCosmAuthorityNotFound, status, errors.New("HiveCosm formal Artifact or source object was not found"))
	case status == http.StatusConflict && wireError.Code == string(HiveCosmAuthorityConflict):
		return authorityFailure(HiveCosmAuthorityConflict, status, errors.New("HiveCosm formal Artifact authority conflicts"))
	case status == http.StatusBadRequest && wireError.Code == string(HiveCosmAuthorityInvalid):
		return authorityFailure(HiveCosmAuthorityInvalid, status, errors.New("HiveCosm rejected the formal Artifact command"))
	case status >= 500:
		return authorityFailure(HiveCosmAuthoritySourceGap, status, errors.New("HiveCosm formal Artifact authority is unavailable"))
	default:
		return authorityFailure(HiveCosmAuthorityInvalid, status, fmt.Errorf("unexpected formal Artifact error %q", wireError.Code))
	}
}

func decodeExactJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}
