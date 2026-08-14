package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalOperatorSessionEnvironmentIsExplicitAllowlist(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"local", true},
		{"development", true},
		{"test", true},
		{"", false},
		{"staging", false},
		{"production", false},
		{"prod", false},
		{"unknown", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.value)
			if got := localOperatorSessionEnvironment(); got != tc.want {
				t.Fatalf("localOperatorSessionEnvironment(%q): want %t, got %t", tc.value, tc.want, got)
			}
		})
	}
}

func TestLocalOperatorSessionRejectsProxyAndNonLoopbackOrigins(t *testing.T) {
	request := func(remoteAddr, origin string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/auth/local-operator-session", nil)
		req.RemoteAddr = remoteAddr
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		return req
	}

	if !isDirectLoopbackRequest(request("[::1]:43123", "")) {
		t.Fatal("IPv6 loopback must be accepted")
	}
	if !isLoopbackOrigin(request("127.0.0.1:43123", "http://[::1]:13512")) {
		t.Fatal("IPv6 loopback Origin must be accepted")
	}

	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"LAN peer", request("192.168.1.7:43123", "http://localhost:13512")},
		{"missing Origin", request("127.0.0.1:43123", "")},
		{"DNS rebinding host", request("127.0.0.1:43123", "http://localhost.evil.com")},
		{"LAN Origin", request("127.0.0.1:43123", "http://192.168.1.7:13512")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !isDirectLoopbackRequest(tc.req) && tc.name == "LAN peer" {
				return
			}
			if isLoopbackOrigin(tc.req) {
				t.Fatalf("%s must not be accepted as a loopback Origin", tc.name)
			}
		})
	}

	forwarded := request("127.0.0.1:43123", "http://localhost:13512")
	forwarded.Header.Set("Forwarded", "for=198.51.100.10")
	if isDirectLoopbackRequest(forwarded) {
		t.Fatal("forwarded loopback request must fail closed")
	}
}
