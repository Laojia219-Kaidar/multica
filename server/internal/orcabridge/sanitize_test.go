package orcabridge

import (
	"strings"
	"testing"
)

func TestRedactCredentialsBearer(t *testing.T) {
	cases := []string{
		"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abc.def",
		"header authorization: bearer abc123defGHI456jkl",
		"curl -H 'Authorization: Bearer ghp_AbCdEfGhIjKlMnOpQrStUvWxYz123456'",
	}
	for _, input := range cases {
		out := RedactCredentials(input)
		if ContainsCredentials(out) {
			t.Fatalf("bearer credential survived: %q -> %q", input, out)
		}
		if !strings.Contains(out, RedactionMarker) {
			t.Fatalf("missing marker: %q -> %q", input, out)
		}
	}
}

func TestRedactCredentialsProviderKeys(t *testing.T) {
	cases := []string{
		"key sk-proj-0123456789abcdefGHIJKLMNOP",
		"key sk-ant-api03-AAABBBCCCDDDEEEFFFGGG",
		"key sk-0123456789abcdef",
		"ark key ark-1234567890abcdefWXYZ",
		"export KEY=sk-livesInEnv0123456789",
	}
	for _, input := range cases {
		out := RedactCredentials(input)
		if strings.Contains(out, "sk-proj-0123456789") || strings.Contains(out, "sk-ant-api03-AAABBBCCC") ||
			strings.Contains(out, "sk-0123456789") || strings.Contains(out, "ark-1234567890") {
			t.Fatalf("provider key survived: %q -> %q", input, out)
		}
	}
}

func TestRedactCredentialsKeyValueSecrets(t *testing.T) {
	cases := map[string]string{
		`"password": "hunter2hunter2hunter2"`: "hunter2hunter2hunter2",
		`password=hunter2hunter2hunter2`:      "hunter2hunter2hunter2",
		`api_key: 0123456789abcdef`:           "0123456789abcdef",
		`api-key=0123456789abcdef`:            "0123456789abcdef",
		`?token=abcdefgh12345678&x=1`:         "abcdefgh12345678",
		`SECRET="zzzzzzzz99999999"`:           "zzzzzzzz99999999",
		`access_key: AKIAIOSFODNN7EXAMPLE`:    "AKIAIOSFODNN7EXAMPLE",
		`credential: 0123456789abcdef`:        "0123456789abcdef",
	}
	for input, secret := range cases {
		out := RedactCredentials(input)
		if strings.Contains(out, secret) {
			t.Fatalf("kv secret survived: %q -> %q", input, out)
		}
	}
}

func TestRedactCredentialsPreservesOrdinaryProse(t *testing.T) {
	cases := []string{
		"Discussed the token bucket design and the password policy.",
		"Files: bridge.go, sanitize.go; no credentials were shared.",
		"skipped the migration as instructed",
		"arkansas and skyline are not keys",
		"",
	}
	for _, input := range cases {
		if out := RedactCredentials(input); out != input {
			t.Fatalf("ordinary prose rewritten: %q -> %q", input, out)
		}
	}
}

func TestRedactCredentialsIsDeterministic(t *testing.T) {
	input := "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.body.sig password=hunter2hunter2"
	first := RedactCredentials(input)
	for i := 0; i < 10; i++ {
		if again := RedactCredentials(input); again != first {
			t.Fatalf("redaction not deterministic: %q vs %q", first, again)
		}
	}
}

func TestRedactStringMapAndSlice(t *testing.T) {
	payload := map[string]any{
		"body":   "token=abcdefgh12345678",
		"nested": map[string]any{"secret": "0123456789abcdef"},
		"count":  7,
	}
	redacted := RedactStringMap(payload)
	if strings.Contains(redacted["body"].(string), "abcdefgh12345678") {
		t.Fatal("map redaction missed body")
	}
	nested := redacted["nested"].(map[string]any)
	if strings.Contains(nested["secret"].(string), "0123456789abcdef") {
		t.Fatal("map redaction missed nested secret")
	}
	if redacted["count"].(int) != 7 {
		t.Fatal("non-string value changed")
	}
	// Input must not be mutated.
	if payload["body"].(string) != "token=abcdefgh12345678" {
		t.Fatal("input payload was mutated")
	}
	slice := RedactStringSlice([]string{"password=hunter2hunter2", "clean"})
	if strings.Contains(slice[0], "hunter2hunter2") || slice[1] != "clean" {
		t.Fatalf("slice redaction wrong: %v", slice)
	}
	if RedactStringSlice(nil) != nil {
		t.Fatal("nil slice must stay nil")
	}
}

// R3 finding: dotted provider-key forms (e.g. sk-sp-H.ABCDEFGHIJKLMNOP)
// must be redacted. Synthetic values only.
func TestRedactCredentialsDottedProviderKeys(t *testing.T) {
	cases := []string{
		"sk-sp-H.ABCDEFGHIJKLMNOP",
		"call used sk-sp-H.ABCDEFGHIJKLMNOP to authenticate",
		"key=sk-live-AbCdEf.GhIjKlMnOp",
		"sk-proj-Q1w2e3.r4t5y6u7i8o9p0",
		"sk-ant-ZX.cvbnmasdfghjkl",
		"ark-cn-beijing.ABCDEFGHIJK",
		"export ORCA_KEY=sk-sp-H.ABCDEFGHIJKLMNOP",
	}
	for _, input := range cases {
		out := RedactCredentials(input)
		if ContainsCredentials(out) {
			t.Fatalf("dotted provider key survived: %q -> %q", input, out)
		}
		if !strings.Contains(out, RedactionMarker) {
			t.Fatalf("missing marker: %q -> %q", input, out)
		}
	}
}

func TestRedactCredentialsDottedFormNegatives(t *testing.T) {
	// Prose containing hyphenated words but no sk-/ark- token of credential
	// shape must stay untouched.
	precise := []string{
		"skipped the migration as instructed",
		"task-sp-hello world",
		"arkansas and skyline are not keys",
		"the sk- prefix family is documented",
	}
	for _, input := range precise {
		if out := RedactCredentials(input); out != input {
			t.Fatalf("prose rewritten: %q -> %q", input, out)
		}
	}
}

// R3 finding: recursively nested arrays/slices of maps/strings are redacted.
func TestRedactValueRecursiveNesting(t *testing.T) {
	nested := map[string]any{
		"summary": "worker used sk-sp-H.ABCDEFGHIJKLMNOP",
		"steps": []any{
			map[string]any{"detail": "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc.def"},
			"plain step text",
			[]any{map[string]any{"api_key": "0123456789abcdef"}},
			[]string{"token=abcdefgh12345678", "clean entry"},
		},
		"attachments": []string{"sk-sp-H.ABCDEFGHIJKLMNOP"},
		"count":       3,
	}
	redacted := RedactStringMap(nested)

	if summary, _ := redacted["summary"].(string); ContainsCredentials(summary) {
		t.Fatalf("top-level string leaked: %q", summary)
	}
	steps, _ := redacted["steps"].([]any)
	if len(steps) != 4 {
		t.Fatalf("steps array reshaped: %+v", steps)
	}
	stepMap, _ := steps[0].(map[string]any)
	if detail, _ := stepMap["detail"].(string); ContainsCredentials(detail) {
		t.Fatalf("map inside array leaked: %q", detail)
	}
	if plain, _ := steps[1].(string); plain != "plain step text" {
		t.Fatalf("plain array string rewritten: %q", plain)
	}
	inner, _ := steps[2].([]any)
	innerMap, _ := inner[0].(map[string]any)
	if apiKey, _ := innerMap["api_key"].(string); apiKey != RedactionMarker {
		t.Fatalf("credential-keyed map inside nested array not marked: %q", apiKey)
	}
	strSlice, _ := steps[3].([]string)
	if ContainsCredentials(strSlice[0]) || strSlice[1] != "clean entry" {
		t.Fatalf("string slice inside array wrong: %+v", strSlice)
	}
	if att, _ := redacted["attachments"].([]string); ContainsCredentials(att[0]) {
		t.Fatalf("attachment leaked: %+v", att)
	}
	if count, _ := redacted["count"].(int); count != 3 {
		t.Fatalf("scalar rewritten: %v", redacted["count"])
	}
	// Inputs must be unmutated.
	if nested["summary"].(string) != "worker used sk-sp-H.ABCDEFGHIJKLMNOP" {
		t.Fatal("input map mutated")
	}
	originalSteps := nested["steps"].([]any)
	originalInner, _ := originalSteps[2].([]any)
	originalInnerMap, _ := originalInner[0].(map[string]any)
	if originalInnerMap["api_key"].(string) != "0123456789abcdef" {
		t.Fatal("nested input mutated")
	}
}

// RedactValue handles bare values without a wrapping map.
func TestRedactValueBare(t *testing.T) {
	if out := RedactValue("sk-sp-H.ABCDEFGHIJKLMNOP"); ContainsCredentials(out.(string)) {
		t.Fatal("bare string leaked")
	}
	list := RedactValue([]any{"password=hunter2hunter2hunter2", 42})
	if ContainsCredentials(list.([]any)[0].(string)) || list.([]any)[1].(int) != 42 {
		t.Fatalf("bare slice wrong: %+v", list)
	}
	if out := RedactValue(3.14); out.(float64) != 3.14 {
		t.Fatal("scalar changed")
	}
}
