// Package companyops contains the authority-boundary primitives used to turn
// governed company objects into HiveCrew execution state without copying their
// lifecycle authority into HiveCrew.
package companyops

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	authorityKindWorkOrder       = "WorkOrder"
	authorityKindEmployee        = "Employee"
	authorityKindIdentityBinding = "IdentityBinding"
	authorityKindAgent           = "Agent"
	currentFreshness             = "current"
)

var hiveCrewAgentRefPattern = regexp.MustCompile(`^/api/agents/[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// AuthoritySnapshot is an observation of one object from its authoritative
// owner. DisplayName and Model are presentation hints only: target resolution
// never consults them and always follows exact source references.
type AuthoritySnapshot struct {
	Kind          string
	SourceRef     string
	Revision      string
	ContentDigest string
	Freshness     string
	DisplayName   string
	Model         string
}

// IdentityBinding is an authority-backed Employee -> Agent edge. Historical
// inactive edges may remain in the input for auditability; only complete,
// current active edges participate in uniqueness checks and resolution.
type IdentityBinding struct {
	Authority   AuthoritySnapshot
	EmployeeRef string
	AgentRef    string
	Active      bool
}

// ExecutionTargetSnapshot is the immutable evidence envelope carried into a
// Run. It intentionally contains exact references, revisions, and digests only;
// mutable display/model hints cannot become fallback target selectors.
type ExecutionTargetSnapshot struct {
	WorkOrderRef      string
	WorkOrderRevision string
	WorkOrderDigest   string
	InputDigest       string

	EmployeeRef      string
	EmployeeRevision string
	EmployeeDigest   string

	BindingRef      string
	BindingRevision string
	BindingDigest   string

	AgentRef      string
	AgentRevision string
	AgentDigest   string
}

// ValidateAndFreezeExecutionTarget validates the complete authoritative chain
// in dependency order and returns a value snapshot suitable for a Run:
//
//	WorkOrder -> Employee -> active IdentityBinding -> exact Agent
//
// It fails closed on invalid/stale authority, malformed digests, incomplete or
// ambiguous active bindings, duplicate Agent snapshots, or a missing exact
// AgentRef. It never falls back to display name, model, list position, or any
// other non-authoritative hint.
func ValidateAndFreezeExecutionTarget(
	workOrder AuthoritySnapshot,
	inputDigest string,
	employee AuthoritySnapshot,
	bindings []IdentityBinding,
	agents []AuthoritySnapshot,
) (ExecutionTargetSnapshot, error) {
	if err := validateAuthoritySnapshot(workOrder, authorityKindWorkOrder); err != nil {
		return ExecutionTargetSnapshot{}, fmt.Errorf("work order authority: %w", err)
	}
	if err := validateSHA256Digest(inputDigest); err != nil {
		return ExecutionTargetSnapshot{}, fmt.Errorf("input digest: %w", err)
	}
	if err := validateAuthoritySnapshot(employee, authorityKindEmployee); err != nil {
		return ExecutionTargetSnapshot{}, fmt.Errorf("employee authority: %w", err)
	}

	binding, err := resolveActiveIdentityBinding(employee.SourceRef, bindings)
	if err != nil {
		return ExecutionTargetSnapshot{}, err
	}

	agent, err := resolveExactAgent(binding.AgentRef, agents)
	if err != nil {
		return ExecutionTargetSnapshot{}, err
	}

	return ExecutionTargetSnapshot{
		WorkOrderRef:      workOrder.SourceRef,
		WorkOrderRevision: workOrder.Revision,
		WorkOrderDigest:   workOrder.ContentDigest,
		InputDigest:       inputDigest,

		EmployeeRef:      employee.SourceRef,
		EmployeeRevision: employee.Revision,
		EmployeeDigest:   employee.ContentDigest,

		BindingRef:      binding.Authority.SourceRef,
		BindingRevision: binding.Authority.Revision,
		BindingDigest:   binding.Authority.ContentDigest,

		AgentRef:      agent.SourceRef,
		AgentRevision: agent.Revision,
		AgentDigest:   agent.ContentDigest,
	}, nil
}

func resolveActiveIdentityBinding(employeeRef string, bindings []IdentityBinding) (IdentityBinding, error) {
	employeeOwners := make(map[string]string)
	agentOwners := make(map[string]string)
	bindingRefs := make(map[string]struct{})

	var target IdentityBinding
	targetFound := false
	for i, binding := range bindings {
		if !binding.Active {
			continue
		}
		if err := validateAuthoritySnapshot(binding.Authority, authorityKindIdentityBinding); err != nil {
			return IdentityBinding{}, fmt.Errorf("active identity binding %d authority: %w", i, err)
		}
		if err := validateSourceRef(binding.EmployeeRef); err != nil {
			return IdentityBinding{}, fmt.Errorf("active identity binding %q employee_ref: %w", binding.Authority.SourceRef, err)
		}
		if err := validateHiveCrewAgentRef(binding.AgentRef); err != nil {
			return IdentityBinding{}, fmt.Errorf("active identity binding %q agent_ref: %w", binding.Authority.SourceRef, err)
		}

		if _, duplicate := bindingRefs[binding.Authority.SourceRef]; duplicate {
			return IdentityBinding{}, fmt.Errorf("duplicate active identity binding source_ref %q", binding.Authority.SourceRef)
		}
		bindingRefs[binding.Authority.SourceRef] = struct{}{}

		if priorBindingRef, duplicate := employeeOwners[binding.EmployeeRef]; duplicate {
			return IdentityBinding{}, fmt.Errorf(
				"employee_ref %q has multiple active bindings %q and %q",
				binding.EmployeeRef,
				priorBindingRef,
				binding.Authority.SourceRef,
			)
		}
		employeeOwners[binding.EmployeeRef] = binding.Authority.SourceRef

		if priorBindingRef, duplicate := agentOwners[binding.AgentRef]; duplicate {
			return IdentityBinding{}, fmt.Errorf(
				"agent_ref %q has multiple active bindings %q and %q",
				binding.AgentRef,
				priorBindingRef,
				binding.Authority.SourceRef,
			)
		}
		agentOwners[binding.AgentRef] = binding.Authority.SourceRef

		if binding.EmployeeRef == employeeRef {
			target = binding
			targetFound = true
		}
	}

	if !targetFound {
		return IdentityBinding{}, fmt.Errorf("employee_ref %q has no complete current active identity binding", employeeRef)
	}
	return target, nil
}

func resolveExactAgent(agentRef string, agents []AuthoritySnapshot) (AuthoritySnapshot, error) {
	seen := make(map[string]struct{}, len(agents))
	var target AuthoritySnapshot
	targetFound := false

	for i, agent := range agents {
		if err := validateAuthoritySnapshot(agent, authorityKindAgent); err != nil {
			return AuthoritySnapshot{}, fmt.Errorf("agent authority %d: %w", i, err)
		}
		if _, duplicate := seen[agent.SourceRef]; duplicate {
			return AuthoritySnapshot{}, fmt.Errorf("duplicate agent authority source_ref %q", agent.SourceRef)
		}
		seen[agent.SourceRef] = struct{}{}

		if agent.SourceRef == agentRef {
			target = agent
			targetFound = true
		}
	}

	if !targetFound {
		return AuthoritySnapshot{}, fmt.Errorf("active identity binding agent_ref %q has no exact current Agent authority", agentRef)
	}
	return target, nil
}

func validateAuthoritySnapshot(snapshot AuthoritySnapshot, expectedKind string) error {
	if snapshot.Kind != expectedKind {
		return fmt.Errorf("kind %q does not match required kind %q", snapshot.Kind, expectedKind)
	}
	var sourceRefErr error
	if expectedKind == authorityKindAgent {
		sourceRefErr = validateHiveCrewAgentRef(snapshot.SourceRef)
	} else if expectedKind == authorityKindWorkOrder {
		sourceRefErr = validateHiveCosmWorkOrderSourceRef(snapshot.SourceRef)
	} else {
		sourceRefErr = validateSourceRef(snapshot.SourceRef)
	}
	if sourceRefErr != nil {
		return fmt.Errorf("source_ref: %w", sourceRefErr)
	}
	if snapshot.Revision == "" || strings.TrimSpace(snapshot.Revision) != snapshot.Revision {
		return fmt.Errorf("revision is missing or non-canonical")
	}
	if err := validateSHA256Digest(snapshot.ContentDigest); err != nil {
		return fmt.Errorf("content_digest: %w", err)
	}
	if snapshot.Freshness != currentFreshness {
		return fmt.Errorf("freshness %q is not %q", snapshot.Freshness, currentFreshness)
	}
	return nil
}

func validateHiveCosmWorkOrderSourceRef(sourceRef string) error {
	if !hiveCosmWorkOrderSourceRefPattern.MatchString(sourceRef) {
		return fmt.Errorf("must be hive://hivecosm/delivery/project/{project}/work-order/{work-order}")
	}
	return nil
}

func validateHiveCrewAgentRef(agentRef string) error {
	if !hiveCrewAgentRefPattern.MatchString(agentRef) {
		return fmt.Errorf("must be an exact canonical /api/agents/{uuid} reference")
	}
	return nil
}

func validateSourceRef(sourceRef string) error {
	if sourceRef == "" || strings.TrimSpace(sourceRef) != sourceRef {
		return fmt.Errorf("is missing or non-canonical")
	}
	parsed, err := url.Parse(sourceRef)
	if err != nil {
		return fmt.Errorf("is not a URI: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" || strings.Trim(parsed.EscapedPath(), "/") == "" {
		return fmt.Errorf("must contain scheme, authority, and object path")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must not contain userinfo, query, or fragment")
	}
	return nil
}

func validateSHA256Digest(digest string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) {
		return fmt.Errorf("must use sha256:<64hex>")
	}
	hexDigest := strings.TrimPrefix(digest, prefix)
	if len(hexDigest) != 64 {
		return fmt.Errorf("must use sha256:<64hex>")
	}
	decoded, err := hex.DecodeString(hexDigest)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("must use sha256:<64hex>")
	}
	return nil
}
