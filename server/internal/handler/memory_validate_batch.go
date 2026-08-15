package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/memory"
)

// Capacity routing V1 (HIVE-CAPACITY-ROUTING-V1): the memory-candidate
// batch validation workload runs on the DGX foundation base's local 27B
// (vLLM Qwen3.6-27B-NVFP4, data never leaves the machine). The backend
// reaches it through the same host-side ssh tunnel as the cockpit
// federation (127.0.0.1:9800 → DGX :8000), container-side via
// host.docker.internal. This replaces the previous no-op status flip in
// ValidateCandidate with a substantive judgment for the batch path; the
// single-candidate endpoint keeps its original semantics.

var memoryValidationClient = &http.Client{Timeout: 120 * time.Second}

func local27BBaseURL() string {
	if v := os.Getenv("HIVECREW_LOCAL27B_URL"); v != "" {
		return v
	}
	return "http://host.docker.internal:9800"
}

type local27BChatRequest struct {
	Model string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens          int                    `json:"max_tokens"`
	ChatTemplateKwargs map[string]interface{} `json:"chat_template_kwargs,omitempty"`
}

type local27BChatResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// memoryVerdict is the structured judgment the 27B must return.
type memoryVerdict struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

var memoryVerdictJSONRe = regexp.MustCompile(`\{[^{}]*"valid"[^{}]*\}`)

// judgeMemoryCandidate sends one candidate to the local 27B and parses the
// verdict. Qwen3.6 is a thinking model; thinking is disabled via
// chat_template_kwargs when honored, with a reasoning-tail fallback parse.
func judgeMemoryCandidate(ctx context.Context, c memory.MemoryCandidate) (memoryVerdict, string, error) {
	prompt := fmt.Sprintf(
		"你是数字员工组织的记忆评审员。判断下面这条员工记忆候选是否值得保留为长期记忆。"+
			"评判标准：内容具体、技术或流程上正确、对后续工作有复用价值；泛泛而谈、错误、或重复常识则不值得保留。"+
			"只输出紧凑JSON，格式 {\"valid\":true或false,\"reason\":\"一句话理由\"}，不要输出其他任何文字。\n"+
			"记忆类型：%s\n记忆内容：%s",
		c.Kind, c.Content,
	)
	reqBody := local27BChatRequest{
		Model: "qwen3.6-27b-nvfp4",
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "user", Content: prompt}},
		MaxTokens:          4096,
		ChatTemplateKwargs: map[string]interface{}{"thinking": false},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return memoryVerdict{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, local27BBaseURL()+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return memoryVerdict{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := memoryValidationClient.Do(req)
	if err != nil {
		return memoryVerdict{}, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return memoryVerdict{}, "", fmt.Errorf("local27b status %d: %s", resp.StatusCode, string(b))
	}
	var out local27BChatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return memoryVerdict{}, "", err
	}
	if len(out.Choices) == 0 {
		return memoryVerdict{}, "", fmt.Errorf("local27b returned no choices")
	}
	// Qwen3.6 thinking behavior is inconsistent with chat_template_kwargs:
	// sometimes the answer lands in content, sometimes only inside reasoning
	// (with content null) and the JSON appears at the reasoning tail. Try
	// content first, then reasoning; extract the last verdict-like object.
	candidates := []string{
		strings.TrimSpace(out.Choices[0].Message.Content),
		strings.TrimSpace(out.Choices[0].Message.Reasoning),
	}
	var verdict memoryVerdict
	for _, raw := range candidates {
		if raw == "" {
			continue
		}
		// find LAST {...} mentioning "valid" — thinking traces may quote the
		// schema earlier; the final answer is the last occurrence.
		all := memoryVerdictJSONRe.FindAllString(raw, -1)
		if len(all) == 0 {
			continue
		}
		if err := json.Unmarshal([]byte(all[len(all)-1]), &verdict); err == nil {
			return verdict, all[len(all)-1], nil
		}
	}
	joined := strings.Join(candidates, " ")
	return verdict, joined, fmt.Errorf("local27b answer not parseable as verdict JSON: %.120s", joined)
}

type memoryBatchValidationReport struct {
	CandidateID string `json:"candidate_id"`
	Status      string `json:"status"` // validated | rejected | error
	Valid       *bool  `json:"valid,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ModelAnswer string `json:"model_answer,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ValidateMemoryCandidatesBatch POST /api/memory/candidates/validate-batch
// routes every pending candidate through the local 27B for substantive
// judgment. Candidates judged valid → validated (eligible for promotion);
// judged invalid → rejected with the reason recorded in evidence; transport
// or parse failures leave the candidate pending and are reported per-item.
func (h *Handler) ValidateMemoryCandidatesBatch(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	repo := h.memoryRepo()
	pending, err := repo.ListRecent(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list candidates")
		return
	}
	store := memoryStore()
	report := make([]memoryBatchValidationReport, 0, len(pending))
	validatedN, rejectedN, errorN := 0, 0, 0

	for _, c := range pending {
		if c.Status != memory.StatusPending {
			continue
		}
		verdict, raw, jerr := judgeMemoryCandidate(r.Context(), c)
		entry := memoryBatchValidationReport{CandidateID: c.ID}
		switch {
		case jerr != nil:
			entry.Status = "error"
			entry.Error = jerr.Error()
			errorN++
		case verdict.Valid:
			// Mirror the store transition so the read model stays coherent,
			// then persist.
			store.ValidateCandidate(c.ID)
			if err := repo.UpdateStatus(r.Context(), c.ID, memory.StatusValidated); err != nil {
				entry.Status = "error"
				entry.Error = "validated in store but persist failed: " + err.Error()
				errorN++
			} else {
				entry.Status = "validated"
				entry.Valid = &verdict.Valid
				entry.Reason = verdict.Reason
				entry.ModelAnswer = raw
				validatedN++
			}
		default:
			if err := repo.UpdateStatus(r.Context(), c.ID, memory.StatusRejected); err != nil {
				entry.Status = "error"
				entry.Error = "rejected in store but persist failed: " + err.Error()
				errorN++
			} else {
				_, _ = store.RejectCandidate(c.ID)
				entry.Status = "rejected"
				entry.Valid = &verdict.Valid
				entry.Reason = verdict.Reason
				entry.ModelAnswer = raw
				rejectedN++
			}
		}
		report = append(report, entry)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"routed_to": "dgx-local-27b (qwen3.6-27b-nvfp4)",
		"pending":   len(report),
		"validated": validatedN,
		"rejected":  rejectedN,
		"errors":    errorN,
		"results":   report,
	})
}
