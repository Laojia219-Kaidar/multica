package orcabridge

import (
	"regexp"
	"strings"
)

// Redaction marker replaces credential-like content before any WorkEntry
// evidence append or Daemon settlement writeback. The marker is a fixed
// literal so redaction stays deterministic and replay digests stay stable.
const RedactionMarker = "[REDACTED:credential]"

// credentialPatterns are ordered regexes for credential-like content in
// worker results and mapping evidence:
//
//  1. HTTP authorization headers (`Authorization: Bearer <jwt>`),
//  2. provider API keys (`sk-`, `sk-proj-`, `sk-ant-`, `ark-`),
//  3. key/value secrets in assignments, query strings, or JSON/YAML
//     (`api_key=...`, `"password": "..."`, `token=...`, `secret: ...`).
//
// Word-ish values must be at least 8 characters so ordinary prose (e.g.
// "token of gratitude", "password policy") is never rewritten.
var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\bsk-(?:proj-|ant-)?[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`\bark-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?key|secret|password|passwd|token|credential)\b["']?(\s*[:=]\s*)["']?([^\s"']{8,})["']?`),
}

// authorizationHeaderPattern additionally detects a bare `Authorization:
// <scheme> <value>` header even when the scheme is not Bearer.
var authorizationHeaderPattern = regexp.MustCompile(`(?i)\bauthorization\b(\s*:\s*)[A-Za-z]+(\s+)[A-Za-z0-9._~+/=-]{8,}`)

// RedactCredentials returns value with every credential-like substring
// replaced by RedactionMarker. It is pure and deterministic: the same input
// always yields the same output, so evidence digests computed over redacted
// content replay identically.
func RedactCredentials(value string) string {
	if value == "" {
		return value
	}
	out := authorizationHeaderPattern.ReplaceAllString(value, "authorization${1}"+RedactionMarker)
	out = credentialPatterns[0].ReplaceAllString(out, "Bearer "+RedactionMarker)
	out = credentialPatterns[1].ReplaceAllString(out, RedactionMarker)
	out = credentialPatterns[2].ReplaceAllString(out, RedactionMarker)
	out = credentialPatterns[3].ReplaceAllString(out, "${1}${2}"+RedactionMarker)
	return out
}

// ContainsCredentials reports whether value still carries credential-like
// content after redaction would have run. Used by tests and fail-closed
// guards.
func ContainsCredentials(value string) bool {
	return RedactCredentials(value) != value
}

// credentialKeyPattern matches map keys whose value is a secret by naming
// convention (api_key, access_token, password, ...). A value stored under
// such a key is redacted wholesale: the pairing with the keyword exists in
// the key, so the value alone carries no safe context.
var credentialKeyPattern = regexp.MustCompile(`(?i)^(?:api[_-]?key|access[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|secret|password|passwd|token|credential)s?$`)

// RedactStringMap returns a copy of payload with credential-keyed values
// replaced by the redaction marker and every other string value redacted.
// Non-string values under non-credential keys are preserved as-is; nested
// maps are walked. The input is never mutated.
func RedactStringMap(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		if credentialKeyPattern.MatchString(key) {
			switch value.(type) {
			case string, map[string]any, []string:
				out[key] = RedactionMarker
				continue
			}
		}
		switch typed := value.(type) {
		case string:
			out[key] = RedactCredentials(typed)
		case map[string]any:
			out[key] = RedactStringMap(typed)
		default:
			out[key] = value
		}
	}
	return out
}

// RedactStringSlice returns a copy of values with each entry redacted.
func RedactStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = RedactCredentials(value)
	}
	return out
}

// hasRedactionMarker reports whether value carries the redaction marker,
// used by guards that must not persist already-marked-but-unredacted mixes.
func hasRedactionMarker(value string) bool {
	return strings.Contains(value, RedactionMarker)
}
