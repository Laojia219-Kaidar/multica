package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type companyOpsRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f companyOpsRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCompanyOpsBearerTransportNeverCrossesConfiguredOrigin(t *testing.T) {
	calls := 0
	base := companyOpsRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if got := request.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    request,
		}, nil
	})
	transport := companyOpsBearerTransport{
		base:            base,
		token:           "secret-token",
		authorityScheme: "https",
		authorityHost:   "authority.example:8443",
	}
	sameOrigin := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Scheme: "https", Host: "authority.example:8443", Path: "/api"},
		Header: make(http.Header),
	}
	if _, err := transport.RoundTrip(sameOrigin.WithContext(context.Background())); err != nil {
		t.Fatal(err)
	}
	if sameOrigin.Header.Get("Authorization") != "" {
		t.Fatal("transport mutated the caller request")
	}
	crossOrigin := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Scheme: "https", Host: "redirect.example", Path: "/steal"},
		Header: make(http.Header),
	}
	if _, err := transport.RoundTrip(crossOrigin.WithContext(context.Background())); err == nil {
		t.Fatal("cross-origin authority request must fail before transport")
	}
	if calls != 1 {
		t.Fatalf("base transport calls = %d, want 1", calls)
	}
}

func TestCompanyOpsDirectoryRouterAppliesSecurityHeadersAcrossEarlyFailures(t *testing.T) {
	if testServer == nil {
		t.Skip("integration server is unavailable")
	}
	tests := []struct {
		name       string
		request    func(*testing.T) *http.Request
		wantStatus int
	}{
		{
			name: "unauthenticated",
			request: func(t *testing.T) *http.Request {
				request, err := http.NewRequest(http.MethodGet, testServer.URL+"/api/company-ops/organization", nil)
				if err != nil {
					t.Fatal(err)
				}
				return request
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "nonmember",
			request: func(t *testing.T) *http.Request {
				request, err := http.NewRequest(http.MethodGet, testServer.URL+"/api/company-ops/organization", nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+testToken)
				request.Header.Set("X-Workspace-ID", "ffffffff-ffff-4fff-8fff-ffffffffffff")
				return request
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "member directory disabled",
			request: func(t *testing.T) *http.Request {
				request, err := http.NewRequest(http.MethodGet, testServer.URL+"/api/company-ops/organization", nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+testToken)
				request.Header.Set("X-Workspace-ID", testWorkspaceID)
				return request
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "method not allowed",
			request: func(t *testing.T) *http.Request {
				request, err := http.NewRequest(http.MethodDelete, testServer.URL+"/api/company-ops/organization", nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+testToken)
				request.Header.Set("X-Workspace-ID", testWorkspaceID)
				return request
			},
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name: "unknown companyops route",
			request: func(t *testing.T) *http.Request {
				request, err := http.NewRequest(http.MethodGet, testServer.URL+"/api/company-ops/not-a-route", nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+testToken)
				request.Header.Set("X-Workspace-ID", testWorkspaceID)
				return request
			},
			wantStatus: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := http.DefaultClient.Do(test.request(t))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
			if got := response.Header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if got := response.Header.Get("X-Content-Type-Options"); !strings.EqualFold(got, "nosniff") {
				t.Fatalf("X-Content-Type-Options = %q", got)
			}
		})
	}
}
