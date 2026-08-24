package notoolcanary

import (
	"strings"
	"testing"
)

const testRequestSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validMarker() string {
	marker, err := CanonicalMarker(testRequestSHA)
	if err != nil {
		panic(err)
	}
	return marker
}

func TestParseValidClosedContract(t *testing.T) {
	state, contract := Parse(validMarker(), Provider, TaskKind, IssueID)
	if state != Valid {
		t.Fatalf("state = %v, want Valid", state)
	}
	if contract.RequestSHA256 != testRequestSHA {
		t.Fatalf("request digest drift: %q", contract.RequestSHA256)
	}
	for _, forbidden := range []string{"multica ", "issue get", "comment add", "MCP"} {
		if strings.Contains(Prompt(contract)+RuntimeBrief(state, contract), forbidden) {
			t.Fatalf("no-tool rendering contains forbidden mandate %q", forbidden)
		}
	}
	if !strings.Contains(Prompt(contract), Instruction) || !strings.Contains(Prompt(contract), DeliveryPrefix) {
		t.Fatal("exact instruction or delivery prefix missing")
	}
}

func TestParseOrdinaryHandoffNotPresent(t *testing.T) {
	state, _ := Parse("ordinary assignment handoff", Provider, TaskKind, IssueID)
	if state != NotPresent {
		t.Fatalf("state = %v, want NotPresent", state)
	}
}

func TestParseZaraExactEmptyHandoffUsesStaticClosedContract(t *testing.T) {
	state, contract := Parse("", ZaraProvider, ZaraTaskKind, ZaraIssueID)
	if state != Valid {
		t.Fatalf("state = %v, want Valid", state)
	}
	want := zaraContract()
	if contract != want {
		t.Fatalf("contract = %#v, want %#v", contract, want)
	}
	combined := Prompt(contract) + RuntimeBrief(state, contract)
	for _, required := range []string{ZaraInstruction, ZaraDeliveryPrefix, ZaraRequestSHA256, "max_tool_calls is 0"} {
		if !strings.Contains(combined, required) {
			t.Fatalf("static Zara contract missing %q:\n%s", required, combined)
		}
	}
	for _, forbidden := range []string{"multica ", "issue get", "comment add", "Available Commands", "repository"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("static Zara contract contains forbidden workflow %q:\n%s", forbidden, combined)
		}
	}
}

func TestParseZaraIssueMismatchFailsClosed(t *testing.T) {
	cases := map[string]struct {
		note     string
		provider string
		kind     string
	}{
		"nonempty handoff": {note: "ordinary handoff", provider: ZaraProvider, kind: ZaraTaskKind},
		"wrong provider":   {provider: Provider, kind: ZaraTaskKind},
		"wrong task kind":  {provider: ZaraProvider, kind: "review"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			state, _ := Parse(tc.note, tc.provider, tc.kind, ZaraIssueID)
			if state != Invalid {
				t.Fatalf("state = %v, want Invalid", state)
			}
		})
	}
}

func TestSingleUseIssueIsExact(t *testing.T) {
	if !IsSingleUseIssue(ZaraIssueID) {
		t.Fatal("Zara Issue must be single-use")
	}
	if IsSingleUseIssue(IssueID) || IsSingleUseIssue("") || IsSingleUseIssue("3ec06127-a2e7-46b0-8ae8-115a97fe9a24") {
		t.Fatal("single-use gate matched a non-Zara Issue")
	}
}

func TestParseMarkerVariantsFailClosed(t *testing.T) {
	cases := map[string]string{
		"stale canary":        strings.Replace(validMarker(), CanaryID, "WO-C1-04-HIV719-QWEN-DGX-FRESH-CANARY-014", 1),
		"wrong issue field":   strings.Replace(validMarker(), IssueID, "00000000-0000-4000-8000-000000000000", 1),
		"wrong live issue":    validMarker(),
		"wrong task field":    strings.Replace(validMarker(), `"task_kind":"work"`, `"task_kind":"review"`, 1),
		"wrong live task":     validMarker(),
		"wrong live provider": validMarker(),
		"unknown version":     strings.Replace(validMarker(), MarkerPrefix, MarkerNamespace+"V2 ", 1),
		"unknown field":       strings.Replace(validMarker(), `,"tool_policy"`, `,"unknown":true,"tool_policy"`, 1),
		"noncanonical order":  strings.Replace(validMarker(), `"canary_id":"`+CanaryID+`","delivery_prefix":"`+DeliveryPrefix+`"`, `"delivery_prefix":"`+DeliveryPrefix+`","canary_id":"`+CanaryID+`"`, 1),
		"uppercase digest":    strings.Replace(validMarker(), testRequestSHA, strings.ToUpper(testRequestSHA), 1),
		"malformed":           MarkerPrefix + `{`,
	}
	for name, note := range cases {
		t.Run(name, func(t *testing.T) {
			actualKind := TaskKind
			actualIssue := IssueID
			actualProvider := Provider
			if name == "wrong live issue" {
				actualIssue = "00000000-0000-4000-8000-000000000000"
			}
			if name == "wrong live task" {
				actualKind = "review"
			}
			if name == "wrong live provider" {
				actualProvider = "claude"
			}
			state, _ := Parse(note, actualProvider, actualKind, actualIssue)
			if state != Invalid {
				t.Fatalf("state = %v, want Invalid", state)
			}
			if strings.Contains(InvalidPrompt()+RuntimeBrief(state, Contract{}), "multica ") {
				t.Fatal("rejection path contains CLI mandate")
			}
		})
	}
}
