package metrics

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const vendorHTTPTimeout = 12 * time.Second

// VendorQuotaConfig holds optional env-based credentials for live vendor polling.
// Secrets must never be logged; callers mask values in diagnostics.
type VendorQuotaConfig struct {
	MiniMaxAPIKey    string
	ZhipuAPIKey      string
	VolcengineAK     string
	VolcengineSK     string
	DeepSeekAPIKey   string
	HTTPClient       *http.Client
}

func (c VendorQuotaConfig) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: vendorHTTPTimeout}
}

// VendorQuotaObservation is one normalized vendor window before plan mapping.
type VendorQuotaObservation struct {
	Vendor      string
	WindowKind  string
	Limit       *int64
	Used        int64
	Remaining   *int64
	ResetAt     *time.Time
	ObservedAt  time.Time
	SourceRef   string
}

// FetchMiniMaxRemains calls the official subscription remains endpoint.
func FetchMiniMaxRemains(ctx context.Context, cfg VendorQuotaConfig) ([]VendorQuotaObservation, error) {
	return fetchMiniMaxRemainsAt(ctx, cfg, "https://www.minimaxi.com/v1/token_plan/remains")
}

func fetchMiniMaxRemainsAt(ctx context.Context, cfg VendorQuotaConfig, endpoint string) ([]VendorQuotaObservation, error) {
	key := strings.TrimSpace(cfg.MiniMaxAPIKey)
	if key == "" {
		return nil, errors.New("minimax api key not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := cfg.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("minimax remains status %d", resp.StatusCode)
	}

	var payload struct {
		Remains []struct {
			WindowType string `json:"window_type"`
			Total      int64  `json:"total"`
			Used       int64  `json:"used"`
			Remaining  int64  `json:"remaining"`
			ResetAt    int64  `json:"reset_at"`
		} `json:"remains"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("minimax remains response is not JSON")
	}

	now := time.Now().UTC()
	out := make([]VendorQuotaObservation, 0)
	for _, row := range payload.Remains {
		kind := minimaxWindowKind(row.WindowType)
		if kind == "" {
			continue
		}
		total := row.Total
		remaining := row.Remaining
		var resetAt *time.Time
		if row.ResetAt > 0 {
			t := time.Unix(row.ResetAt, 0).UTC()
			resetAt = &t
		}
		out = append(out, VendorQuotaObservation{
			Vendor:     "MiniMax",
			WindowKind: kind,
			Limit:      &total,
			Used:       row.Used,
			Remaining:  &remaining,
			ResetAt:    resetAt,
			ObservedAt: now,
			SourceRef:  "minimax:token_plan/remains",
		})
	}
	return out, nil
}

func minimaxWindowKind(windowType string) string {
	switch strings.ToLower(strings.TrimSpace(windowType)) {
	case "5h", "5hour", "five_hour":
		return "5h"
	case "7d", "week", "weekly":
		return "7d"
	default:
		return ""
	}
}

// FetchZhipuQuota calls the unofficial-but-used monitor endpoint defensively.
func FetchZhipuQuota(ctx context.Context, cfg VendorQuotaConfig) ([]VendorQuotaObservation, error) {
	key := strings.TrimSpace(cfg.ZhipuAPIKey)
	if key == "" {
		return nil, errors.New("zhipu api key not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.bigmodel.cn/api/monitor/usage/quota/limit", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := cfg.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zhipu quota status %d", resp.StatusCode)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("zhipu quota response is not JSON")
	}

	now := time.Now().UTC()
	out := make([]VendorQuotaObservation, 0)
	appendWindow := func(kind string, limit, used, remaining int64) {
		if limit <= 0 && remaining <= 0 {
			return
		}
		lim := limit
		rem := remaining
		out = append(out, VendorQuotaObservation{
			Vendor:     "智谱 · GLM",
			WindowKind: kind,
			Limit:      &lim,
			Used:       used,
			Remaining:  &rem,
			ObservedAt: now,
			SourceRef:  "zhipu:monitor/usage/quota/limit",
		})
	}

	parseBucket := func(raw json.RawMessage, kind string) {
		if len(raw) == 0 {
			return
		}
		var bucket struct {
			Total     int64 `json:"total"`
			Used      int64 `json:"used"`
			Remaining int64 `json:"remaining"`
		}
		if err := json.Unmarshal(raw, &bucket); err != nil {
			return
		}
		appendWindow(kind, bucket.Total, bucket.Used, bucket.Remaining)
	}

	parseBucket(payload["TOKENS_LIMIT_5H"], "5h")
	parseBucket(payload["TOKENS_LIMIT_7D"], "7d")
	parseBucket(payload["TOKENS_LIMIT_MONTH"], "monthly")
	parseBucket(payload["TOKENS_LIMIT"], "monthly")
	parseBucket(payload["TIME_LIMIT_5H"], "5h")
	parseBucket(payload["TIME_LIMIT_7D"], "7d")
	parseBucket(payload["TIME_LIMIT_MONTH"], "monthly")

	if len(out) == 0 {
		return nil, errors.New("zhipu quota response had no recognizable windows")
	}
	return out, nil
}

// FetchDeepSeekBalance returns cash balance only (no period windows).
func FetchDeepSeekBalance(ctx context.Context, cfg VendorQuotaConfig) (*VendorQuotaObservation, error) {
	key := strings.TrimSpace(cfg.DeepSeekAPIKey)
	if key == "" {
		return nil, errors.New("deepseek api key not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.deepseek.com/user/balance", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := cfg.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deepseek balance status %d", resp.StatusCode)
	}

	var payload struct {
		BalanceInfos []struct {
			Currency        string  `json:"currency"`
			TotalBalance    float64 `json:"total_balance"`
			GrantedBalance  float64 `json:"granted_balance"`
			ToppedUpBalance float64 `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("deepseek balance response is not JSON")
	}
	if len(payload.BalanceInfos) == 0 {
		return nil, errors.New("deepseek balance response empty")
	}
	// Represent USD cents as pseudo-tokens for display; DeepSeek has no token windows.
	cents := int64(payload.BalanceInfos[0].TotalBalance * 100)
	now := time.Now().UTC()
	return &VendorQuotaObservation{
		Vendor:     "DeepSeek",
		WindowKind: "monthly",
		Limit:      &cents,
		Remaining:  &cents,
		ObservedAt: now,
		SourceRef:  "deepseek:user/balance",
	}, nil
}

// FetchVolcengineArkUsage calls GetAFPUsage for Ark token windows.
func FetchVolcengineArkUsage(ctx context.Context, cfg VendorQuotaConfig) ([]VendorQuotaObservation, error) {
	ak := strings.TrimSpace(cfg.VolcengineAK)
	sk := strings.TrimSpace(cfg.VolcengineSK)
	if ak == "" || sk == "" {
		return nil, errors.New("volcengine ak/sk not configured")
	}

	host := "open.volcengineapi.com"
	path := "/"
	query := "Action=GetAFPUsage&Version=2024-01-01"
	url := "https://" + host + path + "?" + query

	now := time.Now().UTC()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Date", now.Format("20060102T150405Z"))
	if err := signVolcengineRequest(req, ak, sk, now); err != nil {
		return nil, err
	}

	resp, err := cfg.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("volcengine GetAFPUsage status %d", resp.StatusCode)
	}

	var payload struct {
		Result struct {
			Usage []struct {
				WindowType string `json:"WindowType"`
				Total      int64  `json:"Total"`
				Used       int64  `json:"Used"`
				Remaining  int64  `json:"Remaining"`
				ResetTime  string `json:"ResetTime"`
			} `json:"Usage"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("volcengine usage response is not JSON")
	}

	out := make([]VendorQuotaObservation, 0)
	for _, row := range payload.Result.Usage {
		kind := volcengineWindowKind(row.WindowType)
		if kind == "" {
			continue
		}
		total := row.Total
		remaining := row.Remaining
		var resetAt *time.Time
		if row.ResetTime != "" {
			if t, err := time.Parse(time.RFC3339, row.ResetTime); err == nil {
				resetAt = &t
			}
		}
		out = append(out, VendorQuotaObservation{
			Vendor:     "火山引擎 · Doubao",
			WindowKind: kind,
			Limit:      &total,
			Used:       row.Used,
			Remaining:  &remaining,
			ResetAt:    resetAt,
			ObservedAt: now,
			SourceRef:  "volcengine:ark:GetAFPUsage",
		})
	}
	if len(out) == 0 {
		return nil, errors.New("volcengine usage response had no windows")
	}
	return out, nil
}

func volcengineWindowKind(windowType string) string {
	switch strings.ToLower(strings.TrimSpace(windowType)) {
	case "5h", "five_hour":
		return "5h"
	case "1d", "day", "daily":
		return "daily"
	case "7d", "week", "weekly":
		return "7d"
	case "month", "monthly":
		return "monthly"
	default:
		return ""
	}
}

func signVolcengineRequest(req *http.Request, ak, sk string, now time.Time) error {
	// Minimal HMAC-SHA256 signer for GetAFPUsage query-only requests.
	date := now.UTC().Format("20060102T150405Z")
	shortDate := date[:8]
	credentialScope := shortDate + "/cn-north-1/ark/request"
	canonicalQuery := req.URL.RawQuery
	canonicalHeaders := "host:" + req.Host + "\n" + "x-date:" + date + "\n"
	signedHeaders := "host;x-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		"/",
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		hex.EncodeToString(hashSHA256([]byte(""))),
	}, "\n")
	stringToSign := strings.Join([]string{
		"HMAC-SHA256",
		date,
		credentialScope,
		hex.EncodeToString(hashSHA256([]byte(canonicalRequest))),
	}, "\n")
	signingKey := deriveVolcSigningKey(sk, shortDate, "ark")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf(
		"HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		ak, credentialScope, signedHeaders, signature,
	))
	req.Header.Set("X-Date", date)
	return nil
}

func hashSHA256(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(msg))
	return m.Sum(nil)
}

func deriveVolcSigningKey(secret, date, service string) []byte {
	kDate := hmacSHA256([]byte(secret), date)
	kRegion := hmacSHA256(kDate, "cn-north-1")
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "request")
}

// MapVendorObservationToPlan maps a vendor observation to Lane D plan identity.
func MapVendorObservationToPlan(v VendorQuotaObservation, runtimeName string) (provider, plan, account string) {
	switch v.Vendor {
	case "MiniMax":
		return "MiniMax", "MiniMax API", runtimeName
	case "智谱 · GLM":
		return "智谱 · GLM", "GLM API", runtimeName
	case "火山引擎 · Doubao":
		return "火山引擎 · Doubao", "Volcengine Agent Plan", runtimeName
	case "DeepSeek":
		return "DeepSeek", "DeepSeek API", runtimeName
	default:
		return v.Vendor, v.Vendor, runtimeName
	}
}

// MaskSecret returns a redacted preview safe for logs.
func MaskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "…" + value[len(value)-4:]
}
