package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	companyOpsAuthorityBearerEnv          = "HIVECOSM_AUTHORITY_BEARER_TOKEN"
	companyOpsAuthorityKeychainServiceEnv = "HIVECOSM_AUTHORITY_TOKEN_KEYCHAIN_SERVICE"
	companyOpsAuthorityKeychainAccountEnv = "HIVECOSM_AUTHORITY_TOKEN_KEYCHAIN_ACCOUNT"
	companyOpsAuthorityKeychainScriptEnv  = "HIVECOSM_AUTHORITY_TOKEN_KEYCHAIN_HELPER"
)

var (
	errCompanyOpsAuthorityTokenUnavailable = errors.New("authority token reference is unavailable")
	errCompanyOpsAuthorityTokenInvalid     = errors.New("authority token reference is invalid")
)

type companyOpsKeychainTokenReference struct {
	service string
	account string
	script  string
}

type companyOpsKeychainTokenReader func(context.Context, companyOpsKeychainTokenReference) (string, error)

// companyOpsAuthorityBearerTokenFromEnv supports the existing direct runtime
// injection for backwards compatibility, or a complete macOS Keychain
// reference. They are intentionally mutually exclusive so an accidental
// environment token cannot silently override the audited reference.
func companyOpsAuthorityBearerTokenFromEnv(ctx context.Context, keychainRead companyOpsKeychainTokenReader) (string, error) {
	raw := os.Getenv(companyOpsAuthorityBearerEnv)
	ref := companyOpsKeychainTokenReference{
		service: os.Getenv(companyOpsAuthorityKeychainServiceEnv),
		account: os.Getenv(companyOpsAuthorityKeychainAccountEnv),
		script:  os.Getenv(companyOpsAuthorityKeychainScriptEnv),
	}
	keychainConfigured := ref.service != "" || ref.account != "" || ref.script != ""
	if raw != "" && keychainConfigured {
		return "", errCompanyOpsAuthorityTokenInvalid
	}
	if raw != "" {
		if !strictCompanyOpsBearerToken(raw) {
			return "", errCompanyOpsAuthorityTokenInvalid
		}
		return raw, nil
	}
	if !keychainConfigured {
		return "", errCompanyOpsAuthorityTokenUnavailable
	}
	if !validCompanyOpsKeychainReference(ref) {
		return "", errCompanyOpsAuthorityTokenInvalid
	}
	if keychainRead == nil {
		keychainRead = readCompanyOpsAuthorityKeychainToken
	}
	// The Swift helper pays driver plus Keychain latency per invocation
	// (8-14s observed on the formal Mac runtime). Resolution happens once at
	// startup and the bearer stays in-process, so the bound is startup
	// patience, not request latency.
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := keychainRead(readCtx, ref)
	if err != nil || !strictCompanyOpsBearerToken(token) {
		return "", errCompanyOpsAuthorityTokenUnavailable
	}
	return token, nil
}

func validCompanyOpsKeychainReference(ref companyOpsKeychainTokenReference) bool {
	if !strictCompanyOpsConfigText(ref.service) || !strictCompanyOpsConfigText(ref.account) {
		return false
	}
	if !strictCompanyOpsConfigText(ref.script) || !filepath.IsAbs(ref.script) {
		return false
	}
	for _, value := range []string{ref.service, ref.account} {
		for _, runeValue := range value {
			if !(runeValue >= 'a' && runeValue <= 'z' || runeValue >= 'A' && runeValue <= 'Z' || runeValue >= '0' && runeValue <= '9' || strings.ContainsRune("@._:-", runeValue)) {
				return false
			}
		}
	}
	return true
}

// readCompanyOpsAuthorityKeychainToken invokes the checked-in Swift helper as
// an argv-only process. Its stdout remains inside this process as the HTTP
// bearer value; it is never logged, persisted, or exposed via an environment
// variable. A Docker/Linux runtime lacks macOS Keychain and therefore fails
// closed rather than attempting a weaker fallback.
func readCompanyOpsAuthorityKeychainToken(ctx context.Context, ref companyOpsKeychainTokenReference) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errCompanyOpsAuthorityTokenUnavailable
	}
	if _, err := os.Stat(ref.script); err != nil {
		return "", errCompanyOpsAuthorityTokenUnavailable
	}
	command := exec.CommandContext(ctx, "/usr/bin/swift", ref.script, ref.service, ref.account)
	output, err := command.Output()
	if err != nil || ctx.Err() != nil {
		return "", errCompanyOpsAuthorityTokenUnavailable
	}
	return string(output), nil
}

func companyOpsAuthorityTokenSourceError(err error) string {
	if errors.Is(err, errCompanyOpsAuthorityTokenInvalid) {
		return "authority token reference is invalid"
	}
	return fmt.Sprintf("%s is unavailable", "authority token reference")
}
