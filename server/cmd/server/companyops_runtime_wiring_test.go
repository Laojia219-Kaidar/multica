package main

import (
	"net/http"
	"net/url"
	"testing"
)

func TestSafeCompanyOpsAuthorityURLRequiresTLSOutsideLoopback(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "remote https", raw: "https://authority.example", want: true},
		{name: "loopback http", raw: "http://127.0.0.1:3104", want: true},
		{name: "remote http", raw: "http://authority.example", want: false},
		{name: "missing hostname", raw: "https://:443", want: false},
		{name: "trailing colon", raw: "https://authority.example:", want: false},
		{name: "zero port", raw: "https://authority.example:0", want: false},
		{name: "out of range port", raw: "https://authority.example:65536", want: false},
		{name: "non numeric port", raw: "https://authority.example:notaport", want: false},
		{name: "ipv6 with port", raw: "https://[::1]:443", want: true},
		{name: "query", raw: "https://authority.example?token=secret", want: false},
		{name: "userinfo", raw: "https://user:pass@authority.example", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			u, err := url.Parse(test.raw)
			if err != nil {
				// Go 1.26 rejects non-numeric ports at parse time; that
				// rejection is itself the fail-closed outcome the case
				// asserts, so only an unexpected rejection of a wanted URL
				// is a failure.
				if test.want {
					t.Fatal(err)
				}
				return
			}
			if got := isSafeCompanyOpsAuthorityURL(u); got != test.want {
				t.Fatalf("isSafeCompanyOpsAuthorityURL(%q) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
}

func TestCompanyOpsRuntimeClientsFailClosedWithoutCompleteInjectedConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		token  string
		tenant string
	}{
		{name: "missing base", token: "injected-token", tenant: "tenant-1"},
		{name: "missing token", base: "https://authority.example", tenant: "tenant-1"},
		{name: "missing tenant", base: "https://authority.example", token: "injected-token"},
		{name: "token newline", base: "https://authority.example", token: "injected\ntoken", tenant: "tenant-1"},
		{name: "token leading ascii space", base: "https://authority.example", token: " injected-token", tenant: "tenant-1"},
		{name: "token trailing ascii space", base: "https://authority.example", token: "injected-token ", tenant: "tenant-1"},
		{name: "token leading unicode space", base: "https://authority.example", token: "\u00a0injected-token", tenant: "tenant-1"},
		{name: "token trailing unicode space", base: "https://authority.example", token: "injected-token\u00a0", tenant: "tenant-1"},
		{name: "token carriage return", base: "https://authority.example", token: "injected\rtoken", tenant: "tenant-1"},
		{name: "remote http", base: "http://authority.example", token: "injected-token", tenant: "tenant-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clients, err := newCompanyOpsRuntimeClients(test.base, test.token, test.tenant, http.DefaultTransport)
			if err == nil {
				t.Fatal("newCompanyOpsRuntimeClients succeeded for incomplete or unsafe configuration")
			}
			if clients.dispatch != nil || clients.quota != nil || clients.source != nil {
				t.Fatal("runtime source was partially constructed after configuration failure")
			}
		})
	}
}

func TestCompanyOpsRuntimeClientsConstructOnlyWithCompleteConfiguration(t *testing.T) {
	clients, err := newCompanyOpsRuntimeClients("https://authority.example", "injected-token", "tenant-1", http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if clients.dispatch == nil || clients.quota == nil || clients.source == nil {
		t.Fatal("complete configuration did not construct all read-only runtime sources")
	}
}
