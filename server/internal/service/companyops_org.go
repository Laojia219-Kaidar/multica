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
	response, err := s.adapter.GetEmployees(ctx, util.UUIDToString(workspaceID))
	if err != nil {
		return nil, err
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
