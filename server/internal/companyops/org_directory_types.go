package companyops

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	HiveCrewOrganizationSchema = "hivecosm.organization.v1"
	HiveCrewEmployeesSchema    = "hivecosm.employees.v1"
	HiveCrewEmployeeSchema     = "hivecosm.employee.v1"

	PublicOrganizationSchema = "hivecrew.organization.v1"
	PublicEmployeesSchema    = "hivecrew.employees.v1"
	PublicEmployeeSchema     = "hivecrew.employee.v1"

	BindingStateNone                  = "none"
	BindingStateInactiveOnly          = "inactive_only"
	BindingStateUniqueActiveCandidate = "unique_active_candidate"
	BindingStateMultiConflict         = "multiple_active_conflict"
	BindingStateSourceGap             = "source_gap"

	AvailabilityAvailable        = "available"
	AvailabilityNone             = "none"
	AvailabilityInactiveOnly     = "inactive_only"
	AvailabilityMultiConflict    = "multiple_active_conflict"
	AvailabilitySourceGap        = "source_gap"
	AvailabilityMissingOrInvalid = "local_agent_missing_or_invalid"

	AdapterAuthoritySourceRef     = "/api/sovereign-workbench/snapshot#company_workforce"
	AdapterAuthoritySourceVersion = "CompanyWorkforceReadModelV1"
	AdapterAuthorityFreshness     = "observed"

	MaxAdapterBodyBytes = 4 * 1024 * 1024

	authorityMaxAge    = 5 * time.Minute
	authorityFutureGap = 5 * time.Second
	canonicalTimeForm  = "2006-01-02T15:04:05.000Z"
)

var (
	canonicalTimestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}[.][0-9]{3}Z$`)
	sha256Pattern             = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	employeeIDPattern         = regexp.MustCompile(`^DE-[A-Z0-9][A-Z0-9_-]{1,126}$`)
	workforceIDPattern        = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{1,126}$`)
	bindableWorkforcePattern  = regexp.MustCompile(`^(?:KT|EXT)-[A-Z0-9]+(?:-[A-Z0-9]+)*$`)
	uuidPattern               = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	bindingIDPattern          = regexp.MustCompile(`^IB-[A-F0-9]{24}$`)
)

var validBindingStates = map[string]bool{
	BindingStateNone:                  true,
	BindingStateInactiveOnly:          true,
	BindingStateUniqueActiveCandidate: true,
	BindingStateMultiConflict:         true,
	BindingStateSourceGap:             true,
}

type AdapterAuthorityRef struct {
	SourceRef         string `json:"source_ref"`
	SourceVersion     string `json:"source_version"`
	SourceRevision    string `json:"source_revision"`
	ContentDigest     string `json:"content_digest"`
	ObservedAt        string `json:"observed_at"`
	SourceGeneratedAt string `json:"source_generated_at"`
	Freshness         string `json:"freshness"`
	ReadModelOnly     bool   `json:"read_model_only"`
}

func (a AdapterAuthorityRef) Validate(now time.Time) error {
	if a.SourceRef != AdapterAuthoritySourceRef {
		return fmt.Errorf("authority source_ref mismatch")
	}
	if a.SourceVersion != AdapterAuthoritySourceVersion {
		return fmt.Errorf("authority source_version mismatch")
	}
	if !sha256Pattern.MatchString(a.SourceRevision) || a.ContentDigest != a.SourceRevision {
		return fmt.Errorf("authority digest mismatch")
	}
	observedAt, err := parseCanonicalFreshTimestamp(a.ObservedAt, now)
	if err != nil {
		return fmt.Errorf("authority observed_at: %w", err)
	}
	sourceGeneratedAt, err := parseCanonicalFreshTimestamp(a.SourceGeneratedAt, now)
	if err != nil {
		return fmt.Errorf("authority source_generated_at: %w", err)
	}
	if sourceGeneratedAt.After(observedAt) {
		return fmt.Errorf("authority source_generated_at is later than observed_at")
	}
	if a.Freshness != AdapterAuthorityFreshness {
		return fmt.Errorf("authority freshness mismatch")
	}
	if !a.ReadModelOnly {
		return fmt.Errorf("authority read_model_only must be true")
	}
	return nil
}

func (a AdapterAuthorityRef) SameGeneration(other AdapterAuthorityRef) bool {
	return a == other
}

func parseCanonicalFreshTimestamp(value string, now time.Time) (time.Time, error) {
	if !canonicalTimestampPattern.MatchString(value) {
		return time.Time{}, fmt.Errorf("timestamp is not canonical UTC milliseconds")
	}
	parsed, err := time.Parse(canonicalTimeForm, value)
	if err != nil || parsed.UTC().Format(canonicalTimeForm) != value {
		return time.Time{}, fmt.Errorf("timestamp is not a real canonical instant")
	}
	now = now.UTC()
	if parsed.After(now.Add(authorityFutureGap)) {
		return time.Time{}, fmt.Errorf("timestamp is in the future")
	}
	if now.Sub(parsed) > authorityMaxAge {
		return time.Time{}, fmt.Errorf("timestamp is stale")
	}
	return parsed, nil
}

func parseCanonicalTimestamp(value string) (time.Time, error) {
	if !canonicalTimestampPattern.MatchString(value) {
		return time.Time{}, fmt.Errorf("timestamp is not canonical UTC milliseconds")
	}
	parsed, err := time.Parse(canonicalTimeForm, value)
	if err != nil || parsed.UTC().Format(canonicalTimeForm) != value {
		return time.Time{}, fmt.Errorf("timestamp is not a real canonical instant")
	}
	return parsed, nil
}

func canonicalNonblank(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

type AdapterOrganizationResponse struct {
	SchemaVersion string                          `json:"schema_version"`
	OK            bool                            `json:"ok"`
	TenantID      string                          `json:"tenant_id"`
	WorkspaceID   string                          `json:"workspace_id"`
	Authority     AdapterAuthorityRef             `json:"authority"`
	Departments   []AdapterOrganizationDepartment `json:"departments"`
}

type AdapterOrganizationDepartment struct {
	DepartmentID   string                        `json:"department_id"`
	DepartmentName string                        `json:"department_name"`
	Mission        string                        `json:"mission"`
	EmployeeCount  int                           `json:"employee_count"`
	Positions      []AdapterOrganizationPosition `json:"positions"`
}

type AdapterOrganizationPosition struct {
	PositionID    string                           `json:"position_id"`
	PositionTitle string                           `json:"position_title"`
	EmployeeCount int                              `json:"employee_count"`
	EmployeeIDs   []string                         `json:"employee_ids"`
	Appointments  []AdapterOrganizationAppointment `json:"appointments"`
}

type AdapterOrganizationAppointment struct {
	AppointmentID    string `json:"appointment_id"`
	EmployeeID       string `json:"employee_id"`
	WorkforceAgentID string `json:"workforce_agent_id"`
}

func (r AdapterOrganizationResponse) Validate() error {
	if len(r.Departments) == 0 {
		return fmt.Errorf("organization departments are required and nonempty")
	}
	departments := make(map[string]struct{}, len(r.Departments))
	positions := make(map[string]struct{})
	appointments := make(map[string]struct{})
	for _, department := range r.Departments {
		if !canonicalNonblank(department.DepartmentID) ||
			!canonicalNonblank(department.DepartmentName) ||
			!canonicalNonblank(department.Mission) ||
			department.EmployeeCount < 0 {
			return fmt.Errorf("organization department is malformed")
		}
		if department.Positions == nil {
			return fmt.Errorf("organization positions are required")
		}
		if _, exists := departments[department.DepartmentID]; exists {
			return fmt.Errorf("organization contains duplicate department")
		}
		departments[department.DepartmentID] = struct{}{}
		departmentCount := 0
		for _, position := range department.Positions {
			if !canonicalNonblank(position.PositionID) ||
				!canonicalNonblank(position.PositionTitle) ||
				position.EmployeeCount < 0 ||
				position.EmployeeCount != len(position.EmployeeIDs) ||
				position.EmployeeCount != len(position.Appointments) {
				return fmt.Errorf("organization position is malformed")
			}
			if position.EmployeeIDs == nil || position.Appointments == nil {
				return fmt.Errorf("organization position arrays are required")
			}
			if _, exists := positions[position.PositionID]; exists {
				return fmt.Errorf("organization contains duplicate position")
			}
			positions[position.PositionID] = struct{}{}
			employeeIDs := make(map[string]struct{}, len(position.EmployeeIDs))
			for _, employeeID := range position.EmployeeIDs {
				if !employeeIDPattern.MatchString(employeeID) {
					return fmt.Errorf("organization employee_id is malformed")
				}
				if _, exists := employeeIDs[employeeID]; exists {
					return fmt.Errorf("organization contains duplicate employee appointment")
				}
				employeeIDs[employeeID] = struct{}{}
			}
			for _, appointment := range position.Appointments {
				if !canonicalNonblank(appointment.AppointmentID) ||
					!employeeIDPattern.MatchString(appointment.EmployeeID) ||
					!workforceIDPattern.MatchString(appointment.WorkforceAgentID) {
					return fmt.Errorf("organization appointment is malformed")
				}
				if _, exists := appointments[appointment.AppointmentID]; exists {
					return fmt.Errorf("organization contains duplicate appointment")
				}
				appointments[appointment.AppointmentID] = struct{}{}
				if _, exists := employeeIDs[appointment.EmployeeID]; !exists {
					return fmt.Errorf("organization appointment is outside position employee_ids")
				}
			}
			departmentCount += position.EmployeeCount
		}
		if departmentCount != department.EmployeeCount {
			return fmt.Errorf("organization department count mismatch")
		}
	}
	return nil
}

type AdapterEmployeesResponse struct {
	SchemaVersion string                   `json:"schema_version"`
	OK            bool                     `json:"ok"`
	TenantID      string                   `json:"tenant_id"`
	WorkspaceID   string                   `json:"workspace_id"`
	Authority     AdapterAuthorityRef      `json:"authority"`
	Employees     []AdapterEmployeeSummary `json:"employees"`
}

type AdapterEmployeeSummary struct {
	EmployeeID            string                   `json:"employee_id"`
	WorkforceAgentID      string                   `json:"workforce_agent_id"`
	DisplayName           string                   `json:"display_name"`
	EmployeeContractState string                   `json:"employee_contract_state"`
	DepartmentID          string                   `json:"department_id"`
	DepartmentName        string                   `json:"department_name"`
	PositionID            string                   `json:"position_id"`
	PositionTitle         string                   `json:"position_title"`
	BindingState          string                   `json:"binding_state"`
	Binding               AdapterBindingProjection `json:"binding"`
}

type AdapterBindingProjection struct {
	State                 string  `json:"state"`
	CandidateOnly         bool    `json:"candidate_only"`
	ExecutabilityVerified bool    `json:"executability_verified"`
	HiveCrewAgentID       *string `json:"hivecrew_agent_id,omitempty"`
}

func (e AdapterEmployeeSummary) Validate() error {
	if !employeeIDPattern.MatchString(e.EmployeeID) ||
		!workforceIDPattern.MatchString(e.WorkforceAgentID) ||
		!canonicalNonblank(e.DisplayName) ||
		(e.EmployeeContractState != "existing_digital_employee_contract" &&
			e.EmployeeContractState != "agent_universe_projection_gap") ||
		!canonicalNonblank(e.DepartmentID) ||
		!canonicalNonblank(e.DepartmentName) ||
		!canonicalNonblank(e.PositionID) ||
		!canonicalNonblank(e.PositionTitle) ||
		!validBindingStates[e.BindingState] ||
		e.Binding.State != e.BindingState ||
		!e.Binding.CandidateOnly ||
		e.Binding.ExecutabilityVerified {
		return fmt.Errorf("employee summary is malformed")
	}
	if e.BindingState == BindingStateUniqueActiveCandidate {
		if e.Binding.HiveCrewAgentID == nil || !uuidPattern.MatchString(*e.Binding.HiveCrewAgentID) {
			return fmt.Errorf("unique binding lacks canonical HiveCrew agent UUID")
		}
	} else if e.Binding.HiveCrewAgentID != nil {
		return fmt.Errorf("non-unique binding exposes HiveCrew agent UUID")
	}
	if !bindableWorkforcePattern.MatchString(e.WorkforceAgentID) &&
		e.BindingState != BindingStateSourceGap {
		return fmt.Errorf("legacy workforce identity must remain source_gap")
	}
	return nil
}

func (r AdapterEmployeesResponse) Validate() error {
	if len(r.Employees) == 0 {
		return fmt.Errorf("employees array is required and nonempty")
	}
	employees := make(map[string]struct{}, len(r.Employees))
	workforce := make(map[string]struct{}, len(r.Employees))
	for _, employee := range r.Employees {
		if err := employee.Validate(); err != nil {
			return err
		}
		if _, exists := employees[employee.EmployeeID]; exists {
			return fmt.Errorf("employees response contains duplicate employee_id")
		}
		if _, exists := workforce[employee.WorkforceAgentID]; exists {
			return fmt.Errorf("employees response contains duplicate workforce_agent_id")
		}
		employees[employee.EmployeeID] = struct{}{}
		workforce[employee.WorkforceAgentID] = struct{}{}
	}
	return nil
}

type AdapterEmployeeDetailResponse struct {
	SchemaVersion     string                   `json:"schema_version"`
	OK                bool                     `json:"ok"`
	TenantID          string                   `json:"tenant_id"`
	WorkspaceID       string                   `json:"workspace_id"`
	Authority         AdapterAuthorityRef      `json:"authority"`
	Employee          AdapterEmployeeSummary   `json:"employee"`
	Bindings          []AdapterBindingDetail   `json:"bindings"`
	DossierEnrichment AdapterDossierEnrichment `json:"dossier_enrichment"`
}

type AdapterBindingDetail struct {
	IdentityBindingID string                  `json:"identity_binding_id"`
	WorkforceAgentID  string                  `json:"workforce_agent_id"`
	HiveCrewAgentID   string                  `json:"hivecrew_agent_id"`
	AgentRef          string                  `json:"agent_ref"`
	Active            bool                    `json:"active"`
	EffectiveFrom     string                  `json:"effective_from"`
	EffectiveTo       *string                 `json:"effective_to"`
	Authority         AdapterBindingAuthority `json:"authority"`
}

type AdapterBindingAuthority struct {
	SourceRef     string `json:"source_ref"`
	Revision      string `json:"revision"`
	ContentDigest string `json:"content_digest"`
	CandidateOnly bool   `json:"candidate_only"`
}

func (b AdapterBindingDetail) Validate(employee AdapterEmployeeSummary, observedAt time.Time) error {
	if !bindingIDPattern.MatchString(b.IdentityBindingID) ||
		b.WorkforceAgentID != employee.WorkforceAgentID ||
		!uuidPattern.MatchString(b.HiveCrewAgentID) ||
		b.AgentRef != "/api/agents/"+b.HiveCrewAgentID {
		return fmt.Errorf("identity binding detail is malformed")
	}
	from, err := parseCanonicalTimestamp(b.EffectiveFrom)
	if err != nil {
		return fmt.Errorf("identity binding effective_from is malformed")
	}
	var effectiveTo *time.Time
	if b.EffectiveTo != nil {
		to, err := parseCanonicalTimestamp(*b.EffectiveTo)
		if err != nil || !to.After(from) {
			return fmt.Errorf("identity binding effective_to is malformed")
		}
		effectiveTo = &to
	}
	if b.Active && (from.After(observedAt) ||
		(effectiveTo != nil && !observedAt.Before(*effectiveTo))) {
		return fmt.Errorf("active identity binding is outside its effective window")
	}
	if b.Authority.SourceRef != "hivecosm://identity-bindings/"+b.IdentityBindingID ||
		!sha256Pattern.MatchString(b.Authority.Revision) ||
		b.Authority.ContentDigest != b.Authority.Revision ||
		!b.Authority.CandidateOnly {
		return fmt.Errorf("identity binding authority is malformed")
	}
	return nil
}

func (r AdapterEmployeeDetailResponse) Validate(requestedEmployeeID string, now time.Time) error {
	if err := r.Employee.Validate(); err != nil {
		return err
	}
	if r.Employee.EmployeeID != requestedEmployeeID {
		return fmt.Errorf("employee detail identity mismatch")
	}
	if r.Bindings == nil {
		return fmt.Errorf("bindings array is required")
	}
	observedAt, err := parseCanonicalTimestamp(r.Authority.ObservedAt)
	if err != nil {
		return fmt.Errorf("employee detail authority observed_at is malformed")
	}
	activeBindings := 0
	bindingIDs := make(map[string]struct{}, len(r.Bindings))
	bindingRevision := ""
	for _, binding := range r.Bindings {
		if err := binding.Validate(r.Employee, observedAt); err != nil {
			return err
		}
		if _, exists := bindingIDs[binding.IdentityBindingID]; exists {
			return fmt.Errorf("employee detail contains duplicate binding")
		}
		bindingIDs[binding.IdentityBindingID] = struct{}{}
		if bindingRevision == "" {
			bindingRevision = binding.Authority.Revision
		} else if binding.Authority.Revision != bindingRevision {
			return fmt.Errorf("employee detail binding authority revision drift")
		}
		if binding.Active {
			activeBindings++
			if r.Employee.BindingState == BindingStateUniqueActiveCandidate &&
				(r.Employee.Binding.HiveCrewAgentID == nil ||
					binding.HiveCrewAgentID != *r.Employee.Binding.HiveCrewAgentID) {
				return fmt.Errorf("active binding conflicts with employee summary")
			}
		}
	}
	switch r.Employee.BindingState {
	case BindingStateNone, BindingStateSourceGap:
		if len(r.Bindings) != 0 {
			return fmt.Errorf("binding state requires no binding details")
		}
	case BindingStateInactiveOnly:
		if len(r.Bindings) == 0 || activeBindings != 0 {
			return fmt.Errorf("inactive binding state conflicts with binding details")
		}
	case BindingStateUniqueActiveCandidate:
		if activeBindings != 1 {
			return fmt.Errorf("unique binding state conflicts with binding details")
		}
	case BindingStateMultiConflict:
		if activeBindings < 1 {
			return fmt.Errorf("conflict binding state lacks an active related binding")
		}
	}
	if err := r.DossierEnrichment.Validate(now); err != nil {
		return err
	}
	bindable := bindableWorkforcePattern.MatchString(r.Employee.WorkforceAgentID)
	if bindable && r.DossierEnrichment.State == "source_gap" {
		return fmt.Errorf("bindable workforce identity has source_gap dossier")
	}
	if !bindable && r.DossierEnrichment.State != "source_gap" {
		return fmt.Errorf("legacy workforce identity has promoted dossier")
	}
	return nil
}

type AdapterDossierEnrichment struct {
	State     string                   `json:"-"`
	Available *AdapterDossierAvailable `json:"-"`
	SourceGap *AdapterDossierSourceGap `json:"-"`
}

type AdapterDossierAvailable struct {
	State           string                        `json:"state"`
	SourceVersion   string                        `json:"source_version"`
	GeneratedAt     string                        `json:"generated_at"`
	WorkContext     AdapterDossierWorkContext     `json:"work_context"`
	ModelDriver     AdapterDossierModelDriver     `json:"model_driver"`
	ExecutionBridge AdapterDossierExecutionBridge `json:"execution_bridge"`
	Boundaries      AdapterDossierBoundaries      `json:"boundaries"`
}

type AdapterDossierSourceGap struct {
	State         string  `json:"state"`
	SourceVersion *string `json:"source_version"`
	Reason        string  `json:"reason"`
}

type AdapterDossierWorkContext struct {
	RequestedWorkOrderID    *string `json:"requested_work_order_id"`
	WorkOrderCount          int     `json:"work_order_count"`
	AssignmentCount         int     `json:"assignment_count"`
	ConversationThreadCount int     `json:"conversation_thread_count"`
	SourceGap               bool    `json:"source_gap"`
}

type AdapterDossierModelDriver struct {
	AssignmentPresent      bool `json:"assignment_present"`
	ProposalCount          int  `json:"proposal_count"`
	ProposalWriteAvailable bool `json:"proposal_write_available"`
	ModelCallPerformed     bool `json:"model_call_performed"`
	SecretValuesExposed    bool `json:"secret_values_exposed"`
}

type AdapterDossierExecutionBridge struct {
	Source          string   `json:"source"`
	State           string   `json:"state"`
	Configured      bool     `json:"configured"`
	Reachable       bool     `json:"reachable"`
	ProjectID       string   `json:"project_id"`
	GoalRunID       *string  `json:"goal_run_id"`
	TaskIDs         []string `json:"task_ids"`
	SourceGapReason *string  `json:"source_gap_reason"`
	WritePerformed  bool     `json:"write_performed"`
}

type AdapterDossierBoundaries struct {
	ReadModelOnly                   bool `json:"read_model_only"`
	BrowserIdentityTrusted          bool `json:"browser_identity_trusted"`
	ProductionMutationAllowed       bool `json:"production_mutation_allowed"`
	ProviderCallPerformed           bool `json:"provider_call_performed"`
	ParallelEmployeeRegistryCreated bool `json:"parallel_employee_registry_created"`
}

func (d *AdapterDossierEnrichment) UnmarshalJSON(data []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	var state string
	if err := json.Unmarshal(object["state"], &state); err != nil {
		return fmt.Errorf("dossier state is required")
	}
	switch state {
	case "available":
		if !hasExactJSONKeys(object, "state", "source_version", "generated_at", "work_context", "model_driver", "execution_bridge", "boundaries") {
			return fmt.Errorf("available dossier has unexpected keys")
		}
		if !rawObjectHasExactKeys(object["work_context"], "requested_work_order_id", "work_order_count", "assignment_count", "conversation_thread_count", "source_gap") ||
			!rawObjectHasExactKeys(object["model_driver"], "assignment_present", "proposal_count", "proposal_write_available", "model_call_performed", "secret_values_exposed") ||
			!rawObjectHasExactKeys(object["execution_bridge"], "source", "state", "configured", "reachable", "project_id", "goal_run_id", "task_ids", "source_gap_reason", "write_performed") ||
			!rawObjectHasExactKeys(object["boundaries"], "read_model_only", "browser_identity_trusted", "production_mutation_allowed", "provider_call_performed", "parallel_employee_registry_created") {
			return fmt.Errorf("available dossier nested object has unexpected keys")
		}
		var available AdapterDossierAvailable
		if err := json.Unmarshal(data, &available); err != nil {
			return err
		}
		d.State = state
		d.Available = &available
		d.SourceGap = nil
		return nil
	case "source_gap":
		if !hasExactJSONKeys(object, "state", "source_version", "reason") {
			return fmt.Errorf("source_gap dossier has unexpected keys")
		}
		var sourceGap AdapterDossierSourceGap
		if err := json.Unmarshal(data, &sourceGap); err != nil {
			return err
		}
		d.State = state
		d.Available = nil
		d.SourceGap = &sourceGap
		return nil
	default:
		return fmt.Errorf("unknown dossier state")
	}
}

func (d AdapterDossierEnrichment) MarshalJSON() ([]byte, error) {
	switch d.State {
	case "available":
		if d.Available == nil {
			return nil, fmt.Errorf("available dossier is missing")
		}
		return json.Marshal(d.Available)
	case "source_gap":
		if d.SourceGap == nil {
			return nil, fmt.Errorf("source_gap dossier is missing")
		}
		return json.Marshal(d.SourceGap)
	default:
		return nil, fmt.Errorf("unknown dossier state")
	}
}

func (d AdapterDossierEnrichment) Validate(now time.Time) error {
	switch d.State {
	case "available":
		if d.Available == nil || d.SourceGap != nil {
			return fmt.Errorf("available dossier union is malformed")
		}
		v := d.Available
		if v.State != "available" ||
			v.SourceVersion != "EmployeeOperatingViewV2" {
			return fmt.Errorf("available dossier identity is malformed")
		}
		if _, err := parseCanonicalFreshTimestamp(v.GeneratedAt, now); err != nil {
			return fmt.Errorf("available dossier generated_at: %w", err)
		}
		if (v.WorkContext.RequestedWorkOrderID != nil && !canonicalNonblank(*v.WorkContext.RequestedWorkOrderID)) ||
			v.WorkContext.WorkOrderCount < 0 ||
			v.WorkContext.AssignmentCount < 0 ||
			v.WorkContext.ConversationThreadCount < 0 {
			return fmt.Errorf("available dossier work_context is malformed")
		}
		if v.ModelDriver.ProposalCount < 0 ||
			v.ModelDriver.ProposalWriteAvailable ||
			v.ModelDriver.ModelCallPerformed ||
			v.ModelDriver.SecretValuesExposed {
			return fmt.Errorf("available dossier model_driver is unsafe")
		}
		if err := v.ExecutionBridge.Validate(); err != nil {
			return err
		}
		if v.ExecutionBridge.TaskIDs == nil {
			return fmt.Errorf("available dossier task_ids are required")
		}
		if !v.Boundaries.ReadModelOnly ||
			v.Boundaries.BrowserIdentityTrusted ||
			v.Boundaries.ProductionMutationAllowed ||
			v.Boundaries.ProviderCallPerformed ||
			v.Boundaries.ParallelEmployeeRegistryCreated {
			return fmt.Errorf("available dossier boundaries are unsafe")
		}
		return nil
	case "source_gap":
		if d.SourceGap == nil || d.Available != nil ||
			d.SourceGap.State != "source_gap" ||
			d.SourceGap.SourceVersion != nil ||
			d.SourceGap.Reason != "workforce_identity_not_bindable" {
			return fmt.Errorf("source_gap dossier is malformed")
		}
		return nil
	default:
		return fmt.Errorf("unknown dossier state")
	}
}

func (e AdapterDossierExecutionBridge) Validate() error {
	validState := map[string]bool{
		"hq06_not_configured":       true,
		"hq06_unreachable":          true,
		"hq06_project_not_found":    true,
		"hq06_workorder_not_linked": true,
		"hq06_goal_run_linked":      true,
	}
	if e.Source != "HQ-06" ||
		!validState[e.State] ||
		e.ProjectID != "PRJ-HCW-V2" ||
		e.WritePerformed ||
		(e.GoalRunID != nil && !canonicalNonblank(*e.GoalRunID)) ||
		(e.SourceGapReason != nil && !canonicalNonblank(*e.SourceGapReason)) {
		return fmt.Errorf("available dossier execution_bridge is malformed")
	}
	tasks := make(map[string]struct{}, len(e.TaskIDs))
	for _, taskID := range e.TaskIDs {
		if !canonicalNonblank(taskID) {
			return fmt.Errorf("available dossier task_id is malformed")
		}
		if _, exists := tasks[taskID]; exists {
			return fmt.Errorf("available dossier contains duplicate task_id")
		}
		tasks[taskID] = struct{}{}
	}
	return nil
}

func hasExactJSONKeys(object map[string]json.RawMessage, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, exists := object[key]; !exists {
			return false
		}
	}
	return true
}

func rawObjectHasExactKeys(raw json.RawMessage, keys ...string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	return hasExactJSONKeys(object, keys...)
}

type PublicAuthorityRef = AdapterAuthorityRef

type PublicOrganizationResponse struct {
	SchemaVersion string                         `json:"schema_version"`
	WorkspaceID   string                         `json:"workspace_id"`
	Authority     PublicAuthorityRef             `json:"authority"`
	Departments   []PublicOrganizationDepartment `json:"departments"`
}

type PublicOrganizationDepartment struct {
	DepartmentID   string                       `json:"department_id"`
	DepartmentName string                       `json:"department_name"`
	Mission        string                       `json:"mission"`
	EmployeeCount  int                          `json:"employee_count"`
	Positions      []PublicOrganizationPosition `json:"positions"`
}

type PublicOrganizationPosition struct {
	PositionID    string                          `json:"position_id"`
	PositionTitle string                          `json:"position_title"`
	EmployeeCount int                             `json:"employee_count"`
	EmployeeIDs   []string                        `json:"employee_ids"`
	Appointments  []PublicOrganizationAppointment `json:"appointments"`
}

type PublicOrganizationAppointment struct {
	AppointmentID    string `json:"appointment_id"`
	EmployeeID       string `json:"employee_id"`
	WorkforceAgentID string `json:"workforce_agent_id"`
	Availability     string `json:"availability"`
}

type PublicEmployeesResponse struct {
	SchemaVersion string                  `json:"schema_version"`
	WorkspaceID   string                  `json:"workspace_id"`
	Authority     PublicAuthorityRef      `json:"authority"`
	Items         []PublicEmployeeSummary `json:"items"`
	Total         int                     `json:"total"`
	Limit         int                     `json:"limit"`
	Offset        int                     `json:"offset"`
}

type PublicEmployeeSummary struct {
	EmployeeID            string                  `json:"employee_id"`
	WorkforceAgentID      string                  `json:"workforce_agent_id"`
	DisplayName           string                  `json:"display_name"`
	EmployeeContractState string                  `json:"employee_contract_state"`
	DepartmentID          string                  `json:"department_id"`
	DepartmentName        string                  `json:"department_name"`
	PositionID            string                  `json:"position_id"`
	PositionTitle         string                  `json:"position_title"`
	BindingState          string                  `json:"binding_state"`
	Binding               PublicBindingProjection `json:"binding"`
	Availability          string                  `json:"availability"`
	HiveCrewAgentID       string                  `json:"hivecrew_agent_id,omitempty"`
	LocalAgent            *PublicLocalAgent       `json:"local_agent,omitempty"`
}

type PublicBindingProjection struct {
	State                 string  `json:"state"`
	CandidateOnly         bool    `json:"candidate_only"`
	ExecutabilityVerified bool    `json:"executability_verified"`
	HiveCrewAgentID       *string `json:"hivecrew_agent_id,omitempty"`
}

type PublicLocalAgent struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	Status        string  `json:"status"`
	RuntimeID     string  `json:"runtime_id"`
	RuntimeMode   string  `json:"runtime_mode"`
	RuntimeStatus string  `json:"runtime_status"`
	Model         *string `json:"model,omitempty"`
}

type PublicEmployeeDetailResponse struct {
	SchemaVersion     string                   `json:"schema_version"`
	WorkspaceID       string                   `json:"workspace_id"`
	Authority         PublicAuthorityRef       `json:"authority"`
	Employee          PublicEmployeeSummary    `json:"employee"`
	Bindings          []PublicBindingDetail    `json:"bindings"`
	DossierEnrichment AdapterDossierEnrichment `json:"dossier_enrichment"`
}

type PublicBindingDetail struct {
	IdentityBindingID string                  `json:"identity_binding_id"`
	WorkforceAgentID  string                  `json:"workforce_agent_id"`
	HiveCrewAgentID   string                  `json:"hivecrew_agent_id"`
	AgentRef          string                  `json:"agent_ref"`
	Active            bool                    `json:"active"`
	EffectiveFrom     string                  `json:"effective_from"`
	EffectiveTo       *string                 `json:"effective_to"`
	Authority         AdapterBindingAuthority `json:"authority"`
}

func MapBindingStateToAvailability(bindingState string) string {
	switch bindingState {
	case BindingStateNone:
		return AvailabilityNone
	case BindingStateInactiveOnly:
		return AvailabilityInactiveOnly
	case BindingStateMultiConflict:
		return AvailabilityMultiConflict
	case BindingStateSourceGap:
		return AvailabilitySourceGap
	case BindingStateUniqueActiveCandidate:
		return AvailabilityMissingOrInvalid
	default:
		return AvailabilityMissingOrInvalid
	}
}
