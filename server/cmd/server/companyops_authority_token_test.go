package main

import (
	"context"
	"errors"
	"testing"
)

func TestCompanyOpsAuthorityBearerTokenFromEnvRejectsAmbiguousOrIncompleteConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want error
	}{
		{name: "missing", env: map[string]string{}, want: errCompanyOpsAuthorityTokenUnavailable},
		{name: "dual raw and keychain", env: map[string]string{companyOpsAuthorityBearerEnv: "direct-token", companyOpsAuthorityKeychainServiceEnv: "service", companyOpsAuthorityKeychainAccountEnv: "account", companyOpsAuthorityKeychainScriptEnv: "/tmp/helper.swift"}, want: errCompanyOpsAuthorityTokenInvalid},
		{name: "partial keychain", env: map[string]string{companyOpsAuthorityKeychainServiceEnv: "service"}, want: errCompanyOpsAuthorityTokenInvalid},
		{name: "unsafe keychain account", env: map[string]string{companyOpsAuthorityKeychainServiceEnv: "service", companyOpsAuthorityKeychainAccountEnv: "account with spaces", companyOpsAuthorityKeychainScriptEnv: "/tmp/helper.swift"}, want: errCompanyOpsAuthorityTokenInvalid},
		{name: "relative helper", env: map[string]string{companyOpsAuthorityKeychainServiceEnv: "service", companyOpsAuthorityKeychainAccountEnv: "account", companyOpsAuthorityKeychainScriptEnv: "helper.swift"}, want: errCompanyOpsAuthorityTokenInvalid},
		{name: "raw newline", env: map[string]string{companyOpsAuthorityBearerEnv: "direct\ntoken"}, want: errCompanyOpsAuthorityTokenInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setCompanyOpsAuthorityTokenEnv(t, test.env)
			got, err := companyOpsAuthorityBearerTokenFromEnv(context.Background(), func(context.Context, companyOpsKeychainTokenReference) (string, error) {
				return "unused", nil
			})
			if got != "" || !errors.Is(err, test.want) {
				t.Fatalf("token/error = %q/%v, want empty/%v", got, err, test.want)
			}
		})
	}
}

func TestCompanyOpsAuthorityBearerTokenFromEnvUsesExactlyOneApprovedSource(t *testing.T) {
	t.Run("legacy direct", func(t *testing.T) {
		setCompanyOpsAuthorityTokenEnv(t, map[string]string{companyOpsAuthorityBearerEnv: "direct-token"})
		got, err := companyOpsAuthorityBearerTokenFromEnv(context.Background(), nil)
		if err != nil || got != "direct-token" {
			t.Fatalf("token/error = %q/%v", got, err)
		}
	})
	t.Run("keychain reference", func(t *testing.T) {
		setCompanyOpsAuthorityTokenEnv(t, map[string]string{
			companyOpsAuthorityKeychainServiceEnv: "com.hivecosm.authority",
			companyOpsAuthorityKeychainAccountEnv: "hivecrew-local",
			companyOpsAuthorityKeychainScriptEnv:  "/opt/hivecosm/read-keychain-token.swift",
		})
		called := false
		got, err := companyOpsAuthorityBearerTokenFromEnv(context.Background(), func(_ context.Context, ref companyOpsKeychainTokenReference) (string, error) {
			called = ref.service == "com.hivecosm.authority" && ref.account == "hivecrew-local" && ref.script == "/opt/hivecosm/read-keychain-token.swift"
			return "keychain-token", nil
		})
		if err != nil || !called || got != "keychain-token" {
			t.Fatalf("token/called/error = %q/%v/%v", got, called, err)
		}
	})
	t.Run("keychain reader failure stays closed", func(t *testing.T) {
		setCompanyOpsAuthorityTokenEnv(t, map[string]string{
			companyOpsAuthorityKeychainServiceEnv: "com.hivecosm.authority",
			companyOpsAuthorityKeychainAccountEnv: "hivecrew-local",
			companyOpsAuthorityKeychainScriptEnv:  "/opt/hivecosm/read-keychain-token.swift",
		})
		got, err := companyOpsAuthorityBearerTokenFromEnv(context.Background(), func(context.Context, companyOpsKeychainTokenReference) (string, error) {
			return "", errors.New("keychain unavailable")
		})
		if got != "" || !errors.Is(err, errCompanyOpsAuthorityTokenUnavailable) {
			t.Fatalf("token/error = %q/%v", got, err)
		}
	})
}

func setCompanyOpsAuthorityTokenEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for _, key := range []string{
		companyOpsAuthorityBearerEnv,
		companyOpsAuthorityKeychainServiceEnv,
		companyOpsAuthorityKeychainAccountEnv,
		companyOpsAuthorityKeychainScriptEnv,
	} {
		t.Setenv(key, values[key])
	}
}
