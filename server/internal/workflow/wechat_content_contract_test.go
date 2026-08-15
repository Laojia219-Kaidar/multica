package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// WeChat content production node contract — persistent Go contract tests
// (HIVECREW-WECHAT-REAL-OPERATIONS-V1 / WO-10R).
// ---------------------------------------------------------------------------

const (
	wechatTestAgentID   = "11111111-1111-4111-8111-111111111111"
	wechatTestSessionID = "22222222-2222-4222-8222-222222222222"
	wechatTestWorkOrder = "hive://hivecosm/delivery/project/PRJ-WECHAT-OPS/work-order/WO-10"
)

var wechatTestSHA = "sha256:" + strings.Repeat("a", 64)

func validWechatContentRequest() WechatContentProductionRequest {
	return WechatContentProductionRequest{
		SchemaVersion: WechatContentProductionRequestSchemaVersion,
		Channel:       WechatContentChannel,
		ProjectID:     "PRJ-WECHAT-OPS",
		Authority: WechatContentAuthorityContext{
			WorkOrderSourceRef: wechatTestWorkOrder,
			EmployeeID:         "EMP-001",
			IdentityBindingID:  "IB-001",
			AgentID:            wechatTestAgentID,
			SessionID:          wechatTestSessionID,
		},
		Definition: WechatContentDefinitionBinding{
			DefinitionID: "content.wechat-production-package",
			Version:      1,
			Digest:       wechatTestSHA,
		},
		Brief: WechatContentBrief{
			Subject:        "新品发布稿",
			Objective:      "向受众说明产品价值",
			Audience:       "公众号订阅用户",
			SourceRefs:     []string{"ref://material/1"},
			Tone:           "专业",
			Deadline:       "2026-08-20T12:00:00Z",
			ApprovalPolicy: "owner_approval",
			HandoffNote:    "请根据资料包撰写一篇面向订阅用户的新品发布公众号文章。",
		},
		IdempotencyKey: "req-1",
	}
}

// validWechatContentRequestJSON returns the canonical wire form of a valid
// request, so decode-path tests mutate JSON the same way a caller would.
func validWechatContentRequestJSON(t *testing.T) map[string]any {
	t.Helper()
	data, err := json.Marshal(validWechatContentRequest())
	if err != nil {
		t.Fatalf("marshal valid request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal valid request: %v", err)
	}
	return decoded
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestWechatContentNodeContractsFrozen(t *testing.T) {
	nodes := WechatContentNodeContracts()
	if len(nodes) != 4 {
		t.Fatalf("expected four frozen nodes, got %d", len(nodes))
	}
	wantKeys := []WechatContentNodeKey{
		WechatContentNodeResearchMaterialPackage,
		WechatContentNodeArticleDraft,
		WechatContentNodeEditorialReviewReport,
		WechatContentNodeWechatPublicationPackage,
	}
	wantUpstream := []*WechatContentNodeKey{
		nil,
		wechatContentNodeKeyPtr(WechatContentNodeResearchMaterialPackage),
		wechatContentNodeKeyPtr(WechatContentNodeArticleDraft),
		wechatContentNodeKeyPtr(WechatContentNodeEditorialReviewReport),
	}
	wantKinds := []string{
		"wechat.research-material-package.v1",
		"wechat.article-draft.v1",
		"wechat.editorial-review-report.v1",
		"wechat.wechat-publication-package.v1",
	}
	wantRules := []string{"auto_accept", "editorial_review", "approval_gate", "owner_approval"}
	for i, node := range nodes {
		if node.Key != wantKeys[i] {
			t.Errorf("node %d key = %q, want %q", i, node.Key, wantKeys[i])
		}
		if node.Order != i+1 {
			t.Errorf("node %d order = %d, want %d", i, node.Order, i+1)
		}
		if !wechatContentNodeKeyEqual(node.RequiredUpstream, wantUpstream[i]) {
			t.Errorf("node %d required_upstream = %s, want %s", i,
				formatWechatNodeKeyPtr(node.RequiredUpstream), formatWechatNodeKeyPtr(wantUpstream[i]))
		}
		if node.ArtifactKind != wantKinds[i] {
			t.Errorf("node %d artifact_kind = %q, want %q", i, node.ArtifactKind, wantKinds[i])
		}
		if node.ReviewRule != wantRules[i] {
			t.Errorf("node %d review_rule = %q, want %q", i, node.ReviewRule, wantRules[i])
		}
	}
	// Mutating the returned slice must not corrupt subsequent reads.
	nodes[0].ArtifactKind = "wechat.mutated.v1"
	fresh := WechatContentNodeContracts()
	if fresh[0].ArtifactKind != "wechat.research-material-package.v1" {
		t.Fatalf("frozen node contract mutated by caller")
	}
}

func TestWechatContentFirstNodeUpstreamIsJSONNull(t *testing.T) {
	nodes := WechatContentNodeContracts()
	data := mustMarshal(t, nodes[0])
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal node: %v", err)
	}
	value, present := wire["required_upstream"]
	if !present {
		t.Fatalf("required_upstream missing from wire form")
	}
	if value != nil {
		t.Fatalf("first node required_upstream = %v, want JSON null", value)
	}
	// Wire parity in the other direction: JSON null decodes to a nil pointer.
	var decoded WechatContentNodeContract
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if decoded.RequiredUpstream != nil {
		t.Fatalf("JSON null required_upstream decoded to %v, want nil", *decoded.RequiredUpstream)
	}
}

func TestWechatContentLineageShapeFrozen(t *testing.T) {
	lineage := frozenWechatContentNodeLineage()
	members := []WechatContentLineageMember{
		lineage.Issue, lineage.Assignment, lineage.Task,
		lineage.Run, lineage.Candidate, lineage.Outcome,
	}
	authorities := []string{
		WechatContentLineageAuthorityIssue,
		WechatContentLineageAuthorityAssignment,
		WechatContentLineageAuthorityTask,
		WechatContentLineageAuthorityRun,
		WechatContentLineageAuthorityCandidate,
		WechatContentLineageAuthorityOutcome,
	}
	if len(members) != 6 {
		t.Fatalf("expected six lineage members, got %d", len(members))
	}
	for i, member := range members {
		if !member.Required {
			t.Errorf("lineage member %d required = false, want true", i)
		}
		if member.Authority != authorities[i] {
			t.Errorf("lineage member %d authority = %q, want frozen constant %q", i, member.Authority, authorities[i])
		}
	}
	for _, node := range WechatContentNodeContracts() {
		if node.Lineage == nil {
			t.Fatalf("frozen node %q carries no lineage", node.Key)
		}
		if *node.Lineage != lineage {
			t.Errorf("frozen node %q lineage differs from the frozen constants", node.Key)
		}
	}
}

func TestValidateWechatContentProductionRequestAcceptsValid(t *testing.T) {
	if err := ValidateWechatContentProductionRequest(validWechatContentRequest()); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

func TestValidateWechatContentProductionRequestCrossProject(t *testing.T) {
	req := validWechatContentRequest()
	req.ProjectID = "PRJ-OTHER"
	err := ValidateWechatContentProductionRequest(req)
	if err == nil || !strings.Contains(err.Error(), "cross-project") {
		t.Fatalf("cross-project request must fail closed, got %v", err)
	}
}

func TestValidateWechatContentProductionRequestHandoff(t *testing.T) {
	missing := validWechatContentRequest()
	missing.Brief.HandoffNote = ""
	if err := ValidateWechatContentProductionRequest(missing); err == nil ||
		!strings.Contains(err.Error(), "handoff_note") {
		t.Fatalf("missing handoff_note must fail closed, got %v", err)
	}

	blank := validWechatContentRequest()
	blank.Brief.HandoffNote = "   \n\t  "
	if err := ValidateWechatContentProductionRequest(blank); err == nil ||
		!strings.Contains(err.Error(), "handoff_note") {
		t.Fatalf("blank handoff_note must fail closed, got %v", err)
	}

	oversize := validWechatContentRequest()
	oversize.Brief.HandoffNote = strings.Repeat("x", WechatContentHandoffNoteMaxBytes+1)
	if err := ValidateWechatContentProductionRequest(oversize); err == nil ||
		!strings.Contains(err.Error(), "handoff_note") {
		t.Fatalf("oversize handoff_note must fail closed, got %v", err)
	}

	// A 32 KiB handoff note stays legal: the cap matches the existing
	// CompanyOps assignment handler.
	exact := validWechatContentRequest()
	exact.Brief.HandoffNote = strings.Repeat("x", WechatContentHandoffNoteMaxBytes)
	if err := ValidateWechatContentProductionRequest(exact); err != nil {
		t.Fatalf("exactly-32KiB handoff_note must stay legal, got %v", err)
	}
}

func TestValidateWechatContentProductionRequestEmptySourceRefs(t *testing.T) {
	empty := validWechatContentRequest()
	empty.Brief.SourceRefs = nil
	if err := ValidateWechatContentProductionRequest(empty); err == nil ||
		!strings.Contains(err.Error(), "source_refs") {
		t.Fatalf("empty source_refs must fail closed, got %v", err)
	}

	blank := validWechatContentRequest()
	blank.Brief.SourceRefs = []string{"   "}
	if err := ValidateWechatContentProductionRequest(blank); err == nil ||
		!strings.Contains(err.Error(), "source_refs") {
		t.Fatalf("blank source_refs entry must fail closed, got %v", err)
	}
}

func TestValidateWechatContentDeadlineOffsets(t *testing.T) {
	cases := []struct {
		name     string
		deadline string
		wantOK   bool
	}{
		{"utc zulu", "2026-08-20T12:00:00Z", true},
		{"numeric offset", "2026-08-20T20:00:00+08:00", true},
		{"negative offset", "2026-08-20T04:00:00-08:00", true},
		{"fractional seconds", "2026-08-20T12:00:00.123456789Z", true},
		{"invalid calendar", "2026-13-40T99:99:99Z", false},
		{"nonexistent day", "2026-02-30T12:00:00Z", false},
		{"missing timezone", "2026-08-20T12:00:00", false},
		{"offset hour overflow", "2026-08-20T12:00:00+24:00", false},
		{"offset minute overflow", "2026-08-20T12:00:00+08:60", false},
		{"offset max legal", "2026-08-20T12:00:00+23:59", true},
		{"not a datetime", "tomorrow", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validWechatContentRequest()
			req.Brief.Deadline = tc.deadline
			err := ValidateWechatContentProductionRequest(req)
			if tc.wantOK && err != nil {
				t.Fatalf("deadline %q must be accepted, got %v", tc.deadline, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("deadline %q must fail closed", tc.deadline)
			}
		})
	}
}

func TestValidateWechatContentNodePlan(t *testing.T) {
	canonical := WechatContentNodeContracts()

	stripLineage := func(nodes []WechatContentNodeContract) []WechatContentNodeContract {
		out := make([]WechatContentNodeContract, len(nodes))
		for i, n := range nodes {
			n.Lineage = nil
			out[i] = n
		}
		return out
	}

	t.Run("accepts the canonical plan with and without lineage", func(t *testing.T) {
		if err := ValidateWechatContentNodePlan(canonical); err != nil {
			t.Fatalf("canonical plan rejected: %v", err)
		}
		if err := ValidateWechatContentNodePlan(stripLineage(canonical)); err != nil {
			t.Fatalf("canonical plan without optional lineage rejected: %v", err)
		}
	})

	t.Run("rejects duplicate node", func(t *testing.T) {
		dup := []WechatContentNodeContract{canonical[0], canonical[0], canonical[1], canonical[2]}
		if err := ValidateWechatContentNodePlan(dup); err == nil ||
			!strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("duplicate node must fail closed, got %v", err)
		}
	})

	t.Run("rejects missing node", func(t *testing.T) {
		if err := ValidateWechatContentNodePlan(canonical[:3]); err == nil ||
			!strings.Contains(err.Error(), "missing") {
			t.Fatalf("missing node must fail closed, got %v", err)
		}
	})

	t.Run("rejects reordered nodes", func(t *testing.T) {
		reordered := []WechatContentNodeContract{canonical[1], canonical[0], canonical[2], canonical[3]}
		if err := ValidateWechatContentNodePlan(reordered); err == nil ||
			!strings.Contains(err.Error(), "precedes its upstream") {
			t.Fatalf("reordered plan must fail closed, got %v", err)
		}
	})

	t.Run("rejects altered node fields", func(t *testing.T) {
		altered := append([]WechatContentNodeContract(nil), canonical...)
		altered[0].ArtifactKind = "wechat.changed.v1"
		if err := ValidateWechatContentNodePlan(altered); err == nil ||
			!strings.Contains(err.Error(), "artifact_kind altered") {
			t.Fatalf("altered artifact_kind must fail closed, got %v", err)
		}

		alteredUpstream := append([]WechatContentNodeContract(nil), canonical...)
		alteredUpstream[1].RequiredUpstream = nil
		if err := ValidateWechatContentNodePlan(alteredUpstream); err == nil ||
			!strings.Contains(err.Error(), "required_upstream altered") {
			t.Fatalf("altered required_upstream must fail closed, got %v", err)
		}
	})

	t.Run("rejects altered lineage", func(t *testing.T) {
		altered := append([]WechatContentNodeContract(nil), canonical...)
		lineage := frozenWechatContentNodeLineage()
		lineage.Task.Authority = "caller-chosen-string"
		altered[0].Lineage = &lineage
		if err := ValidateWechatContentNodePlan(altered); err == nil ||
			!strings.Contains(err.Error(), "lineage altered") {
			t.Fatalf("caller-chosen lineage authority must fail closed, got %v", err)
		}
	})

	t.Run("rejects unknown node key", func(t *testing.T) {
		unknown := append(append([]WechatContentNodeContract(nil), canonical...),
			WechatContentNodeContract{Key: "not-a-node", Order: 5})
		if err := ValidateWechatContentNodePlan(unknown); err == nil ||
			!strings.Contains(err.Error(), "unknown node key") {
			t.Fatalf("unknown node key must fail closed, got %v", err)
		}
	})
}

func TestDecodeWechatContentProductionRequestJSON(t *testing.T) {
	t.Run("accepts the canonical valid request", func(t *testing.T) {
		req, err := DecodeWechatContentProductionRequestJSON(
			mustMarshal(t, validWechatContentRequestJSON(t)))
		if err != nil {
			t.Fatalf("valid wire request rejected: %v", err)
		}
		if req.Brief.HandoffNote == "" {
			t.Fatalf("handoff_note lost in decode")
		}
	})

	t.Run("rejects top-level caller-supplied input_digest", func(t *testing.T) {
		raw := validWechatContentRequestJSON(t)
		raw["input_digest"] = wechatTestSHA
		if _, err := DecodeWechatContentProductionRequestJSON(mustMarshal(t, raw)); err == nil ||
			!strings.Contains(err.Error(), "input_digest") {
			t.Fatalf("client-supplied input_digest must fail closed, got %v", err)
		}
	})

	t.Run("rejects nested forged proof at any depth", func(t *testing.T) {
		raw := validWechatContentRequestJSON(t)
		brief := raw["brief"].(map[string]any)
		brief["source_refs"] = []any{"ref://material/1", map[string]any{"task_id": "forged"}}
		if _, err := DecodeWechatContentProductionRequestJSON(mustMarshal(t, raw)); err == nil ||
			!strings.Contains(err.Error(), "task_id") {
			t.Fatalf("nested forged task_id must fail closed, got %v", err)
		}

		raw = validWechatContentRequestJSON(t)
		authority := raw["authority"].(map[string]any)
		authority["execution_receipt"] = map[string]any{"state": "completed"}
		if _, err := DecodeWechatContentProductionRequestJSON(mustMarshal(t, raw)); err == nil ||
			!strings.Contains(err.Error(), "execution_receipt") {
			t.Fatalf("nested forged execution_receipt must fail closed, got %v", err)
		}
	})

	t.Run("rejects unknown fields instead of silently dropping them", func(t *testing.T) {
		raw := validWechatContentRequestJSON(t)
		raw["surprise"] = true
		if _, err := DecodeWechatContentProductionRequestJSON(mustMarshal(t, raw)); err == nil ||
			!strings.Contains(err.Error(), "strict contract decode") {
			t.Fatalf("unknown top-level field must fail closed, got %v", err)
		}

		raw = validWechatContentRequestJSON(t)
		raw["brief"].(map[string]any)["nested"] = map[string]any{"x": 1}
		if _, err := DecodeWechatContentProductionRequestJSON(mustMarshal(t, raw)); err == nil ||
			!strings.Contains(err.Error(), "strict contract decode") {
			t.Fatalf("unknown nested field must fail closed, got %v", err)
		}
	})

	t.Run("rejects invalid brief semantics after strict decode", func(t *testing.T) {
		raw := validWechatContentRequestJSON(t)
		raw["brief"].(map[string]any)["handoff_note"] = ""
		if _, err := DecodeWechatContentProductionRequestJSON(mustMarshal(t, raw)); err == nil ||
			!strings.Contains(err.Error(), "handoff_note") {
			t.Fatalf("empty handoff_note must fail closed on the wire path, got %v", err)
		}
	})
}

func TestDecodeWechatContentNodePlanJSON(t *testing.T) {
	t.Run("accepts the canonical frozen plan", func(t *testing.T) {
		nodes, err := DecodeWechatContentNodePlanJSON(mustMarshal(t, WechatContentNodeContracts()))
		if err != nil {
			t.Fatalf("canonical plan rejected: %v", err)
		}
		if len(nodes) != 4 {
			t.Fatalf("expected four nodes, got %d", len(nodes))
		}
	})

	t.Run("rejects forged proof inside a node entry", func(t *testing.T) {
		var raw []map[string]any
		if err := json.Unmarshal(mustMarshal(t, WechatContentNodeContracts()), &raw); err != nil {
			t.Fatalf("unmarshal plan: %v", err)
		}
		raw[0]["run_id"] = "forged"
		if _, err := DecodeWechatContentNodePlanJSON(mustMarshal(t, raw)); err == nil ||
			!strings.Contains(err.Error(), "run_id") {
			t.Fatalf("forged run_id inside a node entry must fail closed, got %v", err)
		}
	})

	t.Run("rejects unknown fields inside a node entry", func(t *testing.T) {
		var raw []map[string]any
		if err := json.Unmarshal(mustMarshal(t, WechatContentNodeContracts()), &raw); err != nil {
			t.Fatalf("unmarshal plan: %v", err)
		}
		raw[0]["surprise"] = true
		if _, err := DecodeWechatContentNodePlanJSON(mustMarshal(t, raw)); err == nil ||
			!strings.Contains(err.Error(), "strict contract decode") {
			t.Fatalf("unknown node entry field must fail closed, got %v", err)
		}
	})
}
