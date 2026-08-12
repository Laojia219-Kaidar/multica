package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

func CompanyOpsNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setCompanyOpsSecurityHeaders(w)
		next.ServeHTTP(w, r)
	})
}

// CompanyOpsSecurityHeaders runs before authentication so every CompanyOps
// response, including auth, membership, actor, 404 and 405 failures, remains
// non-cacheable and cannot be content-sniffed.
func CompanyOpsSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/company-ops" ||
			strings.HasPrefix(r.URL.Path, "/api/company-ops/") {
			setCompanyOpsSecurityHeaders(w)
		}
		next.ServeHTTP(w, r)
	})
}

func setCompanyOpsSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeCompanyOpsDirectoryError(w http.ResponseWriter, status int, reasonCode, message string) {
	setCompanyOpsSecurityHeaders(w)
	writeJSON(w, status, map[string]string{
		"error":       message,
		"reason_code": reasonCode,
	})
}

func writeCompanyOpsDirectoryServiceError(w http.ResponseWriter, err error) {
	setCompanyOpsSecurityHeaders(w)
	switch {
	case errors.Is(err, companyopsapi.ErrAdapterTenantMismatch):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":       "directory authority verification failed",
			"reason_code": "tenant_mismatch",
		})
	case errors.Is(err, companyopsapi.ErrAdapterWorkspaceMismatch):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":       "directory authority verification failed",
			"reason_code": "workspace_mismatch",
		})
	case errors.Is(err, service.ErrCompanyOpsEmployeeNotFound),
		errors.Is(err, companyopsapi.ErrAdapterNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":       "requested employee was not found",
			"reason_code": "not_found",
		})
	case errors.Is(err, companyopsapi.ErrAdapterSourceGap),
		errors.Is(err, companyopsapi.ErrAdapterMalformed):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":       "directory source is temporarily unavailable",
			"reason_code": "source_gap",
		})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":       "directory source is temporarily unavailable",
			"reason_code": "source_gap",
		})
	}
}

func (h *Handler) GetCompanyOpsOrganization(w http.ResponseWriter, r *http.Request) {
	setCompanyOpsSecurityHeaders(w)
	if len(r.URL.Query()) != 0 {
		writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "query parameters are not allowed")
		return
	}
	if h.CompanyOpsDirectory == nil {
		writeCompanyOpsDirectoryError(w, http.StatusServiceUnavailable, "source_gap", "organization directory service is unavailable")
		return
	}
	workspaceID, ok := resolveDirectoryWorkspace(w, r)
	if !ok {
		return
	}
	result, err := h.CompanyOpsDirectory.GetOrganization(r.Context(), workspaceID)
	if err != nil {
		writeCompanyOpsDirectoryServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companyopsapi.PublicOrganizationResponse{
		SchemaVersion: result.SchemaVersion,
		WorkspaceID:   result.WorkspaceID,
		Authority:     result.Authority,
		Departments:   result.Departments,
	})
}

var allowedEmployeeQueryParams = map[string]bool{
	"q":      true,
	"status": true,
	"limit":  true,
	"offset": true,
}

func (h *Handler) GetCompanyOpsEmployees(w http.ResponseWriter, r *http.Request) {
	setCompanyOpsSecurityHeaders(w)
	values := r.URL.Query()
	for key, items := range values {
		if !allowedEmployeeQueryParams[key] || len(items) != 1 {
			writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "query parameters must be known and singular")
			return
		}
	}
	q := ""
	if items, exists := values["q"]; exists {
		q = items[0]
		if q == "" || q != strings.TrimSpace(q) {
			writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "q must be canonical nonblank text")
			return
		}
	}
	statusFilter := ""
	if items, exists := values["status"]; exists {
		statusFilter = items[0]
		switch statusFilter {
		case companyopsapi.AvailabilityAvailable,
			companyopsapi.AvailabilityNone,
			companyopsapi.AvailabilityInactiveOnly,
			companyopsapi.AvailabilityMultiConflict,
			companyopsapi.AvailabilitySourceGap,
			companyopsapi.AvailabilityMissingOrInvalid:
		default:
			writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "unknown status filter")
			return
		}
	}
	limit := 50
	if items, exists := values["limit"]; exists {
		parsed, ok := parseCanonicalDecimal(items[0], false)
		if !ok || parsed < 1 || parsed > 500 {
			writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "limit must be a canonical integer from 1 to 500")
			return
		}
		limit = parsed
	}
	offset := 0
	if items, exists := values["offset"]; exists {
		parsed, ok := parseCanonicalDecimal(items[0], true)
		if !ok {
			writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "offset must be a canonical non-negative integer")
			return
		}
		offset = parsed
	}
	if h.CompanyOpsDirectory == nil {
		writeCompanyOpsDirectoryError(w, http.StatusServiceUnavailable, "source_gap", "organization directory service is unavailable")
		return
	}
	workspaceID, ok := resolveDirectoryWorkspace(w, r)
	if !ok {
		return
	}
	result, err := h.CompanyOpsDirectory.GetEmployees(r.Context(), workspaceID, q, statusFilter, limit, offset)
	if err != nil {
		writeCompanyOpsDirectoryServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companyopsapi.PublicEmployeesResponse{
		SchemaVersion: result.SchemaVersion,
		WorkspaceID:   result.WorkspaceID,
		Authority:     result.Authority,
		Items:         result.Items,
		Total:         result.Total,
		Limit:         result.Limit,
		Offset:        result.Offset,
	})
}

func (h *Handler) GetCompanyOpsEmployee(w http.ResponseWriter, r *http.Request) {
	setCompanyOpsSecurityHeaders(w)
	if len(r.URL.Query()) != 0 {
		writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "query parameters are not allowed")
		return
	}
	if h.CompanyOpsDirectory == nil {
		writeCompanyOpsDirectoryError(w, http.StatusServiceUnavailable, "source_gap", "organization directory service is unavailable")
		return
	}
	workspaceID, ok := resolveDirectoryWorkspace(w, r)
	if !ok {
		return
	}
	employeeID := r.PathValue("employeeId")
	if !isValidEmployeeID(employeeID) {
		writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "employeeId must be a canonical DE identifier")
		return
	}
	result, err := h.CompanyOpsDirectory.GetEmployee(r.Context(), workspaceID, employeeID)
	if err != nil {
		writeCompanyOpsDirectoryServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companyopsapi.PublicEmployeeDetailResponse{
		SchemaVersion:     result.SchemaVersion,
		WorkspaceID:       result.WorkspaceID,
		Authority:         result.Authority,
		Employee:          result.Employee,
		Bindings:          result.Bindings,
		DossierEnrichment: result.DossierEnrichment,
	})
}

func resolveDirectoryWorkspace(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	workspace := middleware.WorkspaceIDFromContext(r.Context())
	if workspace == "" {
		writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "workspace context is required")
		return pgtype.UUID{}, false
	}
	workspaceID, err := util.ParseUUID(workspace)
	if err != nil {
		writeCompanyOpsDirectoryError(w, http.StatusBadRequest, "invalid_request", "workspace ID is not a valid UUID")
		return pgtype.UUID{}, false
	}
	return workspaceID, true
}

func isValidEmployeeID(id string) bool {
	if !strings.HasPrefix(id, "DE-") {
		return false
	}
	rest := id[3:]
	if len(rest) < 2 || len(rest) > 127 {
		return false
	}
	for _, character := range rest {
		if !((character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' ||
			character == '-') {
			return false
		}
	}
	return true
}

func parseCanonicalDecimal(value string, allowZero bool) (int, bool) {
	if value == "" {
		return 0, false
	}
	if value == "0" {
		if allowZero {
			return 0, true
		}
		return 0, false
	}
	if value[0] == '0' {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
