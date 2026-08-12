package companyops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrAdapterSourceGap         = errors.New("adapter source_gap")
	ErrAdapterTenantMismatch    = errors.New("adapter tenant mismatch")
	ErrAdapterWorkspaceMismatch = errors.New("adapter workspace mismatch")
	ErrAdapterMalformed         = errors.New("adapter response malformed")
	ErrAdapterNotFound          = errors.New("adapter not_found")
)

type HiveCrewDirectoryClient struct {
	baseURL    *url.URL
	httpClient *http.Client
	tenantID   string
	now        func() time.Time
}

func NewHiveCrewDirectoryClient(baseURL string, httpClient *http.Client, tenantID string) (*HiveCrewDirectoryClient, error) {
	if !canonicalNonblank(tenantID) {
		return nil, fmt.Errorf("HIVECOSM_TENANT_ID is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("invalid adapter base URL")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &HiveCrewDirectoryClient{
		baseURL:    u,
		httpClient: httpClient,
		tenantID:   tenantID,
		now:        time.Now,
	}, nil
}

func (c *HiveCrewDirectoryClient) GetOrganization(ctx context.Context, workspaceID string) (*AdapterOrganizationResponse, error) {
	body, err := c.doGet(ctx, "/api/company-ops/organization", workspaceID)
	if err != nil {
		return nil, err
	}
	var result AdapterOrganizationResponse
	if err := strictDecode(body, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterMalformed, err)
	}
	if err := c.validateCommon(result.SchemaVersion, result.OK, result.TenantID, result.WorkspaceID, HiveCrewOrganizationSchema, workspaceID); err != nil {
		return nil, err
	}
	now := c.now().UTC()
	if err := result.Authority.Validate(now); err != nil {
		return nil, fmt.Errorf("%w: authority: %v", ErrAdapterMalformed, err)
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("%w: organization: %v", ErrAdapterMalformed, err)
	}
	return &result, nil
}

func (c *HiveCrewDirectoryClient) GetEmployees(ctx context.Context, workspaceID string) (*AdapterEmployeesResponse, error) {
	body, err := c.doGet(ctx, "/api/company-ops/employees", workspaceID)
	if err != nil {
		return nil, err
	}
	var result AdapterEmployeesResponse
	if err := strictDecode(body, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterMalformed, err)
	}
	if err := c.validateCommon(result.SchemaVersion, result.OK, result.TenantID, result.WorkspaceID, HiveCrewEmployeesSchema, workspaceID); err != nil {
		return nil, err
	}
	now := c.now().UTC()
	if err := result.Authority.Validate(now); err != nil {
		return nil, fmt.Errorf("%w: authority: %v", ErrAdapterMalformed, err)
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("%w: employees: %v", ErrAdapterMalformed, err)
	}
	return &result, nil
}

func (c *HiveCrewDirectoryClient) GetEmployee(ctx context.Context, workspaceID, employeeID string) (*AdapterEmployeeDetailResponse, error) {
	if !employeeIDPattern.MatchString(employeeID) {
		return nil, fmt.Errorf("%w: employee identity is malformed", ErrAdapterMalformed)
	}
	body, err := c.doGet(ctx, "/api/company-ops/employees/"+url.PathEscape(employeeID), workspaceID)
	if err != nil {
		if errors.Is(err, ErrAdapterSourceGap) && strings.Contains(err.Error(), "HTTP 404") {
			return nil, fmt.Errorf("%w: %s", ErrAdapterNotFound, employeeID)
		}
		return nil, err
	}
	var result AdapterEmployeeDetailResponse
	if err := strictDecode(body, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterMalformed, err)
	}
	if err := c.validateCommon(result.SchemaVersion, result.OK, result.TenantID, result.WorkspaceID, HiveCrewEmployeeSchema, workspaceID); err != nil {
		return nil, err
	}
	now := c.now().UTC()
	if err := result.Authority.Validate(now); err != nil {
		return nil, fmt.Errorf("%w: authority: %v", ErrAdapterMalformed, err)
	}
	if err := result.Validate(employeeID, now); err != nil {
		return nil, fmt.Errorf("%w: employee detail: %v", ErrAdapterMalformed, err)
	}
	return &result, nil
}

func (c *HiveCrewDirectoryClient) doGet(ctx context.Context, path, workspaceID string) ([]byte, error) {
	if !uuidPattern.MatchString(workspaceID) {
		return nil, fmt.Errorf("%w: workspace identity is malformed", ErrAdapterMalformed)
	}
	rawURL := c.buildURL(path, workspaceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterSourceGap, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterSourceGap, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrAdapterSourceGap, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(MaxAdapterBodyBytes+1)))
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %v", ErrAdapterSourceGap, err)
	}
	if len(body) > MaxAdapterBodyBytes {
		return nil, fmt.Errorf("%w: body exceeds max size", ErrAdapterMalformed)
	}
	return body, nil
}

func (c *HiveCrewDirectoryClient) validateCommon(schemaVersion string, ok bool, tenantID, workspaceID, expectedSchema, expectedWorkspace string) error {
	if schemaVersion != expectedSchema {
		return fmt.Errorf("%w: schema mismatch", ErrAdapterMalformed)
	}
	if !ok {
		return fmt.Errorf("%w: ok is false", ErrAdapterMalformed)
	}
	if tenantID != c.tenantID {
		return fmt.Errorf("%w: tenant mismatch", ErrAdapterTenantMismatch)
	}
	if workspaceID != expectedWorkspace {
		return fmt.Errorf("%w: workspace mismatch", ErrAdapterWorkspaceMismatch)
	}
	return nil
}

func (c *HiveCrewDirectoryClient) buildURL(path, workspaceID string) string {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + path
	q := u.Query()
	q.Set("workspace_id", workspaceID)
	u.RawQuery = q.Encode()
	return u.String()
}

func strictDecode(body []byte, target any) error {
	if len(body) > MaxAdapterBodyBytes {
		return fmt.Errorf("body exceeds max size %d", MaxAdapterBodyBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return fmt.Errorf("trailing data after JSON object")
	}
	return nil
}
