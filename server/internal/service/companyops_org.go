package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrCompanyOpsEmployeeNotFound = errors.New("companyops employee not found")

// ErrCompanyOpsEmptyAuthoritativeWorkforce is the sanitized, request-local
// sentinel for an authoritative directory response whose employee array is
// explicitly empty. The wire-level distinction between an explicit empty
// array and omitted/null is enforced by the directory client; this sentinel
// carries the service classification without disclosing the raw validation
// error, response body, or authority identifiers.
var ErrCompanyOpsEmptyAuthoritativeWorkforce = errors.New("empty authoritative workforce")

// OrganizationSourceState is the frozen, sanitized classification of the
// CompanyOps organization (workforce authority) source. It is a wire-stable
// enum: values are constants with no interpolation of URLs, tenant or token
// source names, credential references, raw responses, raw errors or logs.
type OrganizationSourceState string

const (
	// OrganizationSourceBaseMissing: HIVECOSM_AUTHORITY_BASE_URL was absent at
	// startup, so no authority wiring was attempted.
	OrganizationSourceBaseMissing OrganizationSourceState = "base_missing"
	// OrganizationSourceBaseInvalid: the configured authority base URL or the
	// authority HTTP client built from it was rejected at startup.
	OrganizationSourceBaseInvalid OrganizationSourceState = "base_invalid"
	// OrganizationSourceTokenUnavailable: the authority bearer token could not
	// be resolved at startup. The token source itself is never named.
	OrganizationSourceTokenUnavailable OrganizationSourceState = "token_unavailable"
	// OrganizationSourceTenantMissing: the authority tenant selector was
	// missing, so the directory adapter could not be constructed.
	OrganizationSourceTenantMissing OrganizationSourceState = "tenant_missing"
	// OrganizationSourceDirectoryConstructorError: the directory adapter
	// constructor failed for a reason other than the tenant selector.
	OrganizationSourceDirectoryConstructorError OrganizationSourceState = "directory_constructor_error"
	// OrganizationSourceDirectoryRequestError: a request-time directory read
	// failed. The failure is request-local; no raw error is retained.
	OrganizationSourceDirectoryRequestError OrganizationSourceState = "directory_request_error"
	// OrganizationSourceEmptyAuthoritativeWorkforce: a request-time directory
	// read succeeded at the transport layer but the authoritative workforce
	// array was explicitly empty.
	OrganizationSourceEmptyAuthoritativeWorkforce OrganizationSourceState = "empty_authoritative_workforce"
	// OrganizationSourceHealthy: a request-time directory read returned a
	// valid, non-empty authoritative workforce.
	OrganizationSourceHealthy OrganizationSourceState = "healthy"
)

// ValidOrganizationSourceState reports whether the value is one of the eight
// frozen wire constants.
func ValidOrganizationSourceState(value OrganizationSourceState) bool {
	switch value {
	case OrganizationSourceBaseMissing,
		OrganizationSourceBaseInvalid,
		OrganizationSourceTokenUnavailable,
		OrganizationSourceTenantMissing,
		OrganizationSourceDirectoryConstructorError,
		OrganizationSourceDirectoryRequestError,
		OrganizationSourceEmptyAuthoritativeWorkforce,
		OrganizationSourceHealthy:
		return true
	}
	return false
}

// ClassifyOrganizationDirectoryOutcome maps one request-time directory read to
// a sanitized source state. err is classified only through sentinel identity;
// its text is never used, stored, or surfaced. A non-nil result with a
// non-empty workforce maps to healthy even when err is non-nil.
func ClassifyOrganizationDirectoryOutcome(result *EmployeesResult, err error) OrganizationSourceState {
	switch {
	case err != nil && errors.Is(err, ErrCompanyOpsEmptyAuthoritativeWorkforce):
		return OrganizationSourceEmptyAuthoritativeWorkforce
	case err != nil:
		return OrganizationSourceDirectoryRequestError
	case result == nil:
		// A present adapter returning no response object at all is a failed
		// request, not an authoritative empty workforce.
		return OrganizationSourceDirectoryRequestError
	case len(result.Items) == 0:
		return OrganizationSourceEmptyAuthoritativeWorkforce
	default:
		return OrganizationSourceHealthy
	}
}

// StartupOrganizationDirectoryState maps the startup directory construction
// outcome to a sanitized state. constructed reports whether the directory
// adapter was built; constructorErr is inspected only through sentinel
// identity (tenant missing) and is otherwise collapsed into the generic
// constructor classification so no configuration value can leak.
func StartupOrganizationDirectoryState(constructed bool, constructorErr error) OrganizationSourceState {
	if constructed {
		// A constructed adapter still starts degraded: the first successful
		// non-empty request read promotes the request-local state to healthy.
		return OrganizationSourceDirectoryRequestError
	}
	if constructorErr != nil && strings.Contains(constructorErr.Error(), "HIVECOSM_TENANT_ID") {
		return OrganizationSourceTenantMissing
	}
	return OrganizationSourceDirectoryConstructorError
}

type CompanyOpsDirectoryAdapter interface {
	GetOrganization(ctx context.Context, workspaceID string) (*companyopsapi.AdapterOrganizationResponse, error)
	GetEmployees(ctx context.Context, workspaceID string) (*companyopsapi.AdapterEmployeesResponse, error)
	GetEmployee(ctx context.Context, workspaceID, employeeID string) (*companyopsapi.AdapterEmployeeDetailResponse, error)
}

type CompanyOpsAgentLookup interface {
	GetAgentInWorkspace(ctx context.Context, arg db.GetAgentInWorkspaceParams) (db.Agent, error)
	GetAgentRuntimeForWorkspace(ctx context.Context, arg db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error)
}

type CompanyOpsDirectoryService struct {
	adapter     CompanyOpsDirectoryAdapter
	agentLookup CompanyOpsAgentLookup
}

func NewCompanyOpsDirectoryService(adapter CompanyOpsDirectoryAdapter, agentLookup CompanyOpsAgentLookup) *CompanyOpsDirectoryService {
	return &CompanyOpsDirectoryService{adapter: adapter, agentLookup: agentLookup}
}

type availabilityResult struct {
	status  string
	agent   *db.Agent
	runtime *db.AgentRuntime
}

type OrganizationResult struct {
	SchemaVersion string
	WorkspaceID   string
	Authority     companyopsapi.PublicAuthorityRef
	Departments   []companyopsapi.PublicOrganizationDepartment
}

type EmployeesResult struct {
	SchemaVersion string
	WorkspaceID   string
	Authority     companyopsapi.PublicAuthorityRef
	Items         []companyopsapi.PublicEmployeeSummary
	Total         int
	Limit         int
	Offset        int
}

type EmployeeDetailResult struct {
	SchemaVersion     string
	WorkspaceID       string
	Authority         companyopsapi.PublicAuthorityRef
	Employee          companyopsapi.PublicEmployeeSummary
	Bindings          []companyopsapi.PublicBindingDetail
	DossierEnrichment companyopsapi.AdapterDossierEnrichment
}

func (s *CompanyOpsDirectoryService) GetOrganization(ctx context.Context, workspaceID pgtype.UUID) (*OrganizationResult, error) {
	if s == nil || s.adapter == nil {
		return nil, fmt.Errorf("%w: companyops directory adapter is not configured", companyopsapi.ErrAdapterSourceGap)
	}
	workspace := util.UUIDToString(workspaceID)
	organization, err := s.adapter.GetOrganization(ctx, workspace)
	if err != nil {
		return nil, err
	}
	employees, err := s.adapter.GetEmployees(ctx, workspace)
	if err != nil {
		return nil, err
	}
	if err := organization.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", companyopsapi.ErrAdapterMalformed, err)
	}
	if err := employees.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", companyopsapi.ErrAdapterMalformed, err)
	}
	if !organization.Authority.SameGeneration(employees.Authority) {
		return nil, fmt.Errorf("%w: organization authority generation mismatch", companyopsapi.ErrAdapterMalformed)
	}
	if err := validateOrganizationEmployeeJoin(organization, employees); err != nil {
		return nil, fmt.Errorf("%w: %v", companyopsapi.ErrAdapterMalformed, err)
	}
	availabilityByEmployee, err := s.computeAvailabilityMap(ctx, workspaceID, employees.Employees)
	if err != nil {
		return nil, err
	}
	result := &OrganizationResult{
		SchemaVersion: companyopsapi.PublicOrganizationSchema,
		WorkspaceID:   workspace,
		Authority:     mapAuthority(organization.Authority),
		Departments:   make([]companyopsapi.PublicOrganizationDepartment, 0, len(organization.Departments)),
	}
	for _, department := range organization.Departments {
		publicDepartment := companyopsapi.PublicOrganizationDepartment{
			DepartmentID:   department.DepartmentID,
			DepartmentName: department.DepartmentName,
			Mission:        department.Mission,
			EmployeeCount:  department.EmployeeCount,
			Positions:      make([]companyopsapi.PublicOrganizationPosition, 0, len(department.Positions)),
		}
		for _, position := range department.Positions {
			publicPosition := companyopsapi.PublicOrganizationPosition{
				PositionID:    position.PositionID,
				PositionTitle: position.PositionTitle,
				EmployeeCount: position.EmployeeCount,
				EmployeeIDs:   append([]string(nil), position.EmployeeIDs...),
				Appointments:  make([]companyopsapi.PublicOrganizationAppointment, 0, len(position.Appointments)),
			}
			for _, appointment := range position.Appointments {
				availability := availabilityByEmployee[appointment.EmployeeID]
				publicPosition.Appointments = append(publicPosition.Appointments, companyopsapi.PublicOrganizationAppointment{
					AppointmentID:    appointment.AppointmentID,
					EmployeeID:       appointment.EmployeeID,
					WorkforceAgentID: appointment.WorkforceAgentID,
					Availability:     availability.status,
				})
			}
			publicDepartment.Positions = append(publicDepartment.Positions, publicPosition)
		}
		result.Departments = append(result.Departments, publicDepartment)
	}
	return result, nil
}

func (s *CompanyOpsDirectoryService) GetEmployees(
	ctx context.Context,
	workspaceID pgtype.UUID,
	q string,
	availabilityFilter string,
	limit int,
	offset int,
) (*EmployeesResult, error) {
	if s == nil || s.adapter == nil {
		// The authority adapter is optional at composition time; a runtime
		// without it must fail closed as a source gap instead of panicking
		// inside the shadow dispatch path.
		return nil, fmt.Errorf("%w: companyops directory adapter is not configured", companyopsapi.ErrAdapterSourceGap)
	}
	response, err := s.adapter.GetEmployees(ctx, util.UUIDToString(workspaceID))
	if err != nil {
		return nil, err
	}
	if len(response.Employees) == 0 {
		// The directory client has already distinguished an explicit empty
		// array from omitted/null on the wire; this exact shape carries the
		// sanitized sentinel and never reaches the generic malformed wrap.
		return nil, ErrCompanyOpsEmptyAuthoritativeWorkforce
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", companyopsapi.ErrAdapterMalformed, err)
	}
	availabilityByEmployee, err := s.computeAvailabilityMap(ctx, workspaceID, response.Employees)
	if err != nil {
		return nil, err
	}
	filtered := make([]companyopsapi.PublicEmployeeSummary, 0, len(response.Employees))
	for _, employee := range response.Employees {
		availability := availabilityByEmployee[employee.EmployeeID]
		if availabilityFilter != "" && availability.status != availabilityFilter {
			continue
		}
		if q != "" && !matchesQuery(employee, q) {
			continue
		}
		filtered = append(filtered, buildPublicSummary(employee, availability))
	}
	total := len(filtered)
	if offset >= len(filtered) {
		filtered = []companyopsapi.PublicEmployeeSummary{}
	} else {
		filtered = filtered[offset:]
		if limit < len(filtered) {
			filtered = filtered[:limit]
		}
	}
	return &EmployeesResult{
		SchemaVersion: companyopsapi.PublicEmployeesSchema,
		WorkspaceID:   util.UUIDToString(workspaceID),
		Authority:     mapAuthority(response.Authority),
		Items:         filtered,
		Total:         total,
		Limit:         limit,
		Offset:        offset,
	}, nil
}

func (s *CompanyOpsDirectoryService) GetEmployee(
	ctx context.Context,
	workspaceID pgtype.UUID,
	employeeID string,
) (*EmployeeDetailResult, error) {
	if s == nil || s.adapter == nil {
		return nil, fmt.Errorf("%w: companyops directory adapter is not configured", companyopsapi.ErrAdapterSourceGap)
	}
	workspace := util.UUIDToString(workspaceID)
	response, err := s.adapter.GetEmployee(ctx, workspace, employeeID)
	if err != nil {
		if errors.Is(err, companyopsapi.ErrAdapterNotFound) {
			return nil, ErrCompanyOpsEmployeeNotFound
		}
		return nil, err
	}
	availability, err := s.resolveAvailability(ctx, workspaceID, response.Employee)
	if err != nil {
		return nil, err
	}
	summary := buildPublicSummary(response.Employee, availability)
	bindings := []companyopsapi.PublicBindingDetail{}
	if availability.status == companyopsapi.AvailabilityAvailable {
		if availability.agent == nil {
			return nil, fmt.Errorf("%w: available employee lacks local Agent", companyopsapi.ErrAdapterMalformed)
		}
		agentID := util.UUIDToString(availability.agent.ID)
		for _, binding := range response.Bindings {
			if !binding.Active || binding.HiveCrewAgentID != agentID {
				continue
			}
			bindings = append(bindings, companyopsapi.PublicBindingDetail{
				IdentityBindingID: binding.IdentityBindingID,
				WorkforceAgentID:  binding.WorkforceAgentID,
				HiveCrewAgentID:   binding.HiveCrewAgentID,
				AgentRef:          binding.AgentRef,
				Active:            true,
				EffectiveFrom:     binding.EffectiveFrom,
				EffectiveTo:       binding.EffectiveTo,
				Authority:         binding.Authority,
			})
		}
		if len(bindings) != 1 {
			return nil, fmt.Errorf("%w: available employee lacks one exact active binding", companyopsapi.ErrAdapterMalformed)
		}
	}
	return &EmployeeDetailResult{
		SchemaVersion:     companyopsapi.PublicEmployeeSchema,
		WorkspaceID:       workspace,
		Authority:         mapAuthority(response.Authority),
		Employee:          summary,
		Bindings:          bindings,
		DossierEnrichment: response.DossierEnrichment,
	}, nil
}

// WorkforceBaseRuntimeRow is the strict one-to-one join of an Employee's
// current executable identity: Employee -> Agent -> Runtime -> Base (physical
// machine derived from runtime device_info). A row is emitted for every
// employee; only `available` rows carry the resolved HiveCrew agent, runtime,
// base machine, statuses and model. The identity binding is never invented:
// employees without a verified executable binding leave the join empty.
type WorkforceBaseRuntimeRow struct {
	EmployeeID       string `json:"employee_id"`
	WorkforceAgentID string `json:"workforce_agent_id"`
	HiveCrewAgentID  string `json:"hivecrew_agent_id,omitempty"`
	RuntimeID        string `json:"runtime_id,omitempty"`
	BaseMachineTitle string `json:"base_machine_title,omitempty"`
	AgentStatus      string `json:"agent_status,omitempty"`
	RuntimeStatus    string `json:"runtime_status,omitempty"`
	Model            string `json:"model,omitempty"`
}

// GetWorkforceBaseRuntimeJoin resolves the Employee/Agent/Runtime/Base join
// across the directory authority and the local Agent registry. It is the
// single read model used by the organization roster and the bases overview so
// the two surfaces can never disagree about where an employee executes.
func (s *CompanyOpsDirectoryService) GetWorkforceBaseRuntimeJoin(
	ctx context.Context,
	workspaceID pgtype.UUID,
) (companyopsapi.PublicAuthorityRef, []WorkforceBaseRuntimeRow, error) {
	if s == nil || s.adapter == nil {
		return companyopsapi.PublicAuthorityRef{}, nil, fmt.Errorf("%w: companyops directory adapter is not configured", companyopsapi.ErrAdapterSourceGap)
	}
	response, err := s.adapter.GetEmployees(ctx, util.UUIDToString(workspaceID))
	if err != nil {
		return companyopsapi.PublicAuthorityRef{}, nil, err
	}
	if err := response.Validate(); err != nil {
		return companyopsapi.PublicAuthorityRef{}, nil, fmt.Errorf("%w: %v", companyopsapi.ErrAdapterMalformed, err)
	}
	availabilityByEmployee, err := s.computeAvailabilityMap(ctx, workspaceID, response.Employees)
	if err != nil {
		return companyopsapi.PublicAuthorityRef{}, nil, err
	}
	rows := make([]WorkforceBaseRuntimeRow, 0, len(response.Employees))
	for _, employee := range response.Employees {
		availability := availabilityByEmployee[employee.EmployeeID]
		row := WorkforceBaseRuntimeRow{
			EmployeeID:       employee.EmployeeID,
			WorkforceAgentID: employee.WorkforceAgentID,
		}
		if availability.status != companyopsapi.AvailabilityAvailable ||
			availability.agent == nil ||
			availability.runtime == nil {
			rows = append(rows, row)
			continue
		}
		row.HiveCrewAgentID = util.UUIDToString(availability.agent.ID)
		row.RuntimeID = util.UUIDToString(availability.runtime.ID)
		row.BaseMachineTitle = machineTitle(availability.runtime.DeviceInfo)
		row.AgentStatus = availability.agent.Status
		row.RuntimeStatus = availability.runtime.Status
		if availability.agent.Model.Valid {
			row.Model = availability.agent.Model.String
		}
		rows = append(rows, row)
	}
	return mapAuthority(response.Authority), rows, nil
}

func (s *CompanyOpsDirectoryService) computeAvailabilityMap(
	ctx context.Context,
	workspaceID pgtype.UUID,
	employees []companyopsapi.AdapterEmployeeSummary,
) (map[string]availabilityResult, error) {
	result := make(map[string]availabilityResult, len(employees))
	for _, employee := range employees {
		if _, exists := result[employee.EmployeeID]; exists {
			return nil, fmt.Errorf("%w: duplicate employee identity", companyopsapi.ErrAdapterMalformed)
		}
		availability, err := s.resolveAvailability(ctx, workspaceID, employee)
		if err != nil {
			return nil, err
		}
		result[employee.EmployeeID] = availability
	}
	return result, nil
}

func (s *CompanyOpsDirectoryService) resolveAvailability(
	ctx context.Context,
	workspaceID pgtype.UUID,
	employee companyopsapi.AdapterEmployeeSummary,
) (availabilityResult, error) {
	if err := employee.Validate(); err != nil {
		return availabilityResult{}, fmt.Errorf("%w: employee summary: %v", companyopsapi.ErrAdapterMalformed, err)
	}
	mapped := companyopsapi.MapBindingStateToAvailability(employee.BindingState)
	if employee.BindingState != companyopsapi.BindingStateUniqueActiveCandidate {
		return availabilityResult{status: mapped}, nil
	}
	if employee.Binding.HiveCrewAgentID == nil {
		return availabilityResult{status: companyopsapi.AvailabilityMissingOrInvalid}, nil
	}
	agentID, err := util.ParseUUID(*employee.Binding.HiveCrewAgentID)
	if err != nil {
		return availabilityResult{status: companyopsapi.AvailabilityMissingOrInvalid}, nil
	}
	agent, err := s.agentLookup.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return availabilityResult{status: companyopsapi.AvailabilityMissingOrInvalid}, nil
	}
	if !uuidEqual(agent.ID, agentID) ||
		!uuidEqual(agent.WorkspaceID, workspaceID) ||
		agent.Kind != "user" ||
		agent.ArchivedAt.Valid ||
		!isExecutableStatus(agent.Status) ||
		!agent.RuntimeID.Valid {
		return availabilityResult{status: companyopsapi.AvailabilityMissingOrInvalid}, nil
	}
	runtime, err := s.agentLookup.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID:          agent.RuntimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return availabilityResult{status: companyopsapi.AvailabilityMissingOrInvalid}, nil
	}
	if !uuidEqual(runtime.ID, agent.RuntimeID) ||
		!uuidEqual(runtime.WorkspaceID, workspaceID) ||
		runtime.Status != "online" ||
		runtime.RuntimeMode != agent.RuntimeMode {
		return availabilityResult{status: companyopsapi.AvailabilityMissingOrInvalid}, nil
	}
	agentCopy := agent
	runtimeCopy := runtime
	return availabilityResult{
		status:  companyopsapi.AvailabilityAvailable,
		agent:   &agentCopy,
		runtime: &runtimeCopy,
	}, nil
}

func validateOrganizationEmployeeJoin(
	organization *companyopsapi.AdapterOrganizationResponse,
	employees *companyopsapi.AdapterEmployeesResponse,
) error {
	employeesByID := make(map[string]companyopsapi.AdapterEmployeeSummary, len(employees.Employees))
	for _, employee := range employees.Employees {
		if err := employee.Validate(); err != nil {
			return err
		}
		if _, exists := employeesByID[employee.EmployeeID]; exists {
			return fmt.Errorf("employees response contains duplicate employee_id")
		}
		employeesByID[employee.EmployeeID] = employee
	}
	seen := make(map[string]struct{}, len(employeesByID))
	for _, department := range organization.Departments {
		for _, position := range department.Positions {
			for _, appointment := range position.Appointments {
				employee, exists := employeesByID[appointment.EmployeeID]
				if !exists ||
					employee.WorkforceAgentID != appointment.WorkforceAgentID ||
					employee.DepartmentID != department.DepartmentID ||
					employee.DepartmentName != department.DepartmentName ||
					employee.PositionID != position.PositionID ||
					employee.PositionTitle != position.PositionTitle {
					return fmt.Errorf("organization appointment conflicts with employee roster")
				}
				if _, duplicate := seen[employee.EmployeeID]; duplicate {
					return fmt.Errorf("organization contains duplicate employee appointment")
				}
				seen[employee.EmployeeID] = struct{}{}
			}
		}
	}
	if len(seen) != len(employeesByID) {
		return fmt.Errorf("organization and employee roster sets differ")
	}
	return nil
}

func buildPublicSummary(
	employee companyopsapi.AdapterEmployeeSummary,
	availability availabilityResult,
) companyopsapi.PublicEmployeeSummary {
	result := companyopsapi.PublicEmployeeSummary{
		EmployeeID:            employee.EmployeeID,
		WorkforceAgentID:      employee.WorkforceAgentID,
		DisplayName:           employee.DisplayName,
		EmployeeContractState: employee.EmployeeContractState,
		DepartmentID:          employee.DepartmentID,
		DepartmentName:        employee.DepartmentName,
		PositionID:            employee.PositionID,
		PositionTitle:         employee.PositionTitle,
		BindingState:          employee.BindingState,
		Binding: companyopsapi.PublicBindingProjection{
			State:                 employee.Binding.State,
			CandidateOnly:         true,
			ExecutabilityVerified: false,
		},
		Availability: availability.status,
	}
	if availability.status != companyopsapi.AvailabilityAvailable ||
		availability.agent == nil ||
		availability.runtime == nil {
		return result
	}
	agentID := util.UUIDToString(availability.agent.ID)
	result.HiveCrewAgentID = agentID
	result.Binding.ExecutabilityVerified = true
	result.Binding.HiveCrewAgentID = &agentID
	var model *string
	if availability.agent.Model.Valid {
		value := availability.agent.Model.String
		model = &value
	}
	result.LocalAgent = &companyopsapi.PublicLocalAgent{
		ID:            agentID,
		Name:          availability.agent.Name,
		Kind:          availability.agent.Kind,
		Status:        availability.agent.Status,
		RuntimeID:     util.UUIDToString(availability.runtime.ID),
		RuntimeMode:   availability.runtime.RuntimeMode,
		RuntimeStatus: availability.runtime.Status,
		Model:         model,
	}
	return result
}

func mapAuthority(authority companyopsapi.AdapterAuthorityRef) companyopsapi.PublicAuthorityRef {
	return authority
}

// machineTitle extracts the physical machine title from a runtime's
// device_info ("HiveCosm Mac mini · 2.1.221 (Claude Code)"). It is the
// observed execution location — a read-model base key, not a company-owned
// home/fallback base assignment.
func machineTitle(deviceInfo string) string {
	machine := strings.TrimSpace(deviceInfo)
	if i := strings.Index(machine, " · "); i >= 0 {
		machine = strings.TrimSpace(machine[:i])
	}
	if machine == "" {
		return "unknown"
	}
	return machine
}

func matchesQuery(employee companyopsapi.AdapterEmployeeSummary, q string) bool {
	lower := strings.ToLower(q)
	return strings.Contains(strings.ToLower(employee.DisplayName), lower) ||
		strings.Contains(strings.ToLower(employee.EmployeeID), lower) ||
		strings.Contains(strings.ToLower(employee.WorkforceAgentID), lower)
}

func isExecutableStatus(status string) bool {
	return status == "idle" || status == "working"
}

func uuidEqual(left, right pgtype.UUID) bool {
	return left.Valid && right.Valid && left.Bytes == right.Bytes
}
