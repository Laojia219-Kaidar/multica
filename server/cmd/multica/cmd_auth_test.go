package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestMain(m *testing.M) {
	for _, key := range []string{
		"MULTICA_AGENT_ID",
		"MULTICA_TASK_ID",
		"MULTICA_TOKEN",
		"MULTICA_DAEMON_PORT",
		"MULTICA_WORKSPACE_ID",
		"MULTICA_SERVER_URL",
		taskTokenCapabilityFileEnv,
	} {
		os.Unsetenv(key)
	}
	os.Exit(m.Run())
}

// testCmd returns a minimal cobra.Command with the --profile persistent flag
// registered, matching the rootCmd setup used in production.
func testCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.PersistentFlags().String("profile", "", "")
	return cmd
}

func TestResolveAppURL(t *testing.T) {
	cmd := testCmd()

	t.Run("prefers MULTICA_APP_URL", func(t *testing.T) {
		t.Setenv("MULTICA_APP_URL", "http://localhost:14000")
		t.Setenv("FRONTEND_ORIGIN", "http://localhost:13000")

		if got := resolveAppURL(cmd); got != "http://localhost:14000" {
			t.Fatalf("resolveAppURL() = %q, want %q", got, "http://localhost:14000")
		}
	})

	t.Run("falls back to FRONTEND_ORIGIN", func(t *testing.T) {
		t.Setenv("MULTICA_APP_URL", "")
		t.Setenv("FRONTEND_ORIGIN", "http://localhost:13026")

		if got := resolveAppURL(cmd); got != "http://localhost:13026" {
			t.Fatalf("resolveAppURL() = %q, want %q", got, "http://localhost:13026")
		}
	})
}

func TestResolveCallbackBinding(t *testing.T) {
	// Fake outbound detector: pretends the CLI has a fixed LAN IP regardless
	// of which server it dials.
	fixed := func(ip string) func(string) net.IP {
		return func(string) net.IP { return net.ParseIP(ip).To4() }
	}
	failing := func(string) net.IP { return nil }

	cases := []struct {
		name         string
		flagHost     string
		serverURL    string
		appURL       string
		detect       func(string) net.IP
		wantCallback string
		wantBind     string
	}{
		{
			name:         "public app URL stays on loopback",
			appURL:       "https://multica.ai",
			serverURL:    "https://api.multica.ai",
			detect:       failing,
			wantCallback: "localhost",
			wantBind:     "127.0.0.1",
		},
		{
			name:         "localhost app URL stays on loopback",
			appURL:       "http://localhost:3000",
			serverURL:    "http://localhost:8080",
			detect:       failing,
			wantCallback: "localhost",
			wantBind:     "127.0.0.1",
		},
		{
			name:         "same-machine self-host uses loopback (CLI IP matches app IP)",
			appURL:       "http://192.168.0.28:3000",
			serverURL:    "http://192.168.0.28:8080",
			detect:       fixed("192.168.0.28"),
			wantCallback: "localhost",
			wantBind:     "127.0.0.1",
		},
		{
			name:         "cross-machine self-host points callback at CLI's LAN IP",
			appURL:       "http://192.168.0.28:3000",
			serverURL:    "http://192.168.0.28:8080",
			detect:       fixed("192.168.0.47"),
			wantCallback: "192.168.0.47",
			wantBind:     "0.0.0.0",
		},
		{
			name:         "outbound detection failure falls back to app IP",
			appURL:       "http://192.168.0.28:3000",
			serverURL:    "http://192.168.0.28:8080",
			detect:       failing,
			wantCallback: "192.168.0.28",
			wantBind:     "0.0.0.0",
		},
		{
			name:         "--callback-host flag overrides everything",
			flagHost:     "cli.internal.example",
			appURL:       "https://multica.ai",
			serverURL:    "https://api.multica.ai",
			detect:       fixed("10.0.0.5"),
			wantCallback: "cli.internal.example",
			wantBind:     "0.0.0.0",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotCallback, gotBind := resolveCallbackBinding(tc.flagHost, tc.serverURL, tc.appURL, tc.detect)
			if gotCallback != tc.wantCallback {
				t.Errorf("callback host = %q, want %q", gotCallback, tc.wantCallback)
			}
			if gotBind != tc.wantBind {
				t.Errorf("bind addr = %q, want %q", gotBind, tc.wantBind)
			}
		})
	}
}

func TestBrowserLoginInstructionsSSHRemoteHint(t *testing.T) {
	const loginURL = "https://multica.ai/login?cli_callback=http%3A%2F%2Flocalhost%3A43689%2Fcallback"

	got := browserLoginInstructions(loginURL, "localhost", 43689, true)
	if !strings.Contains(got, "ssh -L 43689:127.0.0.1:43689 <user>@<remote-host>") {
		t.Fatalf("remote SSH instructions missing tunnel command:\n%s", got)
	}
	if !strings.Contains(got, loginURL) {
		t.Fatalf("instructions missing login URL:\n%s", got)
	}

	got = browserLoginInstructions(loginURL, "localhost", 43689, false)
	if strings.Contains(got, "ssh -L") {
		t.Fatalf("local instructions should not include SSH tunnel command:\n%s", got)
	}

	got = browserLoginInstructions(loginURL, "192.168.1.25", 43689, true)
	if strings.Contains(got, "ssh -L") {
		t.Fatalf("non-loopback callback should not include SSH tunnel command:\n%s", got)
	}
}

func TestCallbackHostFlagValueReadsParentSetupFlag(t *testing.T) {
	var got string
	setup := &cobra.Command{Use: "setup"}
	setup.Flags().String(callbackHostFlag, "", "")
	cloud := &cobra.Command{
		Use: "cloud",
		Run: func(cmd *cobra.Command, args []string) {
			got = callbackHostFlagValue(cmd)
		},
	}
	cloud.Flags().String(callbackHostFlag, "", "")
	setup.AddCommand(cloud)
	setup.SetArgs([]string{"--callback-host", "10.0.0.5", "cloud"})

	if err := setup.Execute(); err != nil {
		t.Fatalf("execute setup cloud: %v", err)
	}
	if got != "10.0.0.5" {
		t.Fatalf("callback host = %q, want parent flag value", got)
	}
}

// TestLoginTokenFlagWiring asserts the production loginCmd flag is registered
// the way #1994 needs it to be: a String flag (not Bool) with a NoOptDefVal
// so `--token` (no value) keeps its legacy prompt-mode behavior. This is the
// load-bearing regression guard — without these asserts a future change that
// reverts the flag to Bool could pass while a synthetic stand-in test happily
// keeps testing string-flag parsing.
func TestLoginTokenFlagWiring(t *testing.T) {
	tokenFlag := loginCmd.Flags().Lookup("token")
	if tokenFlag == nil {
		t.Fatal("loginCmd is missing the --token flag")
	}
	if got := tokenFlag.Value.Type(); got != "string" {
		t.Fatalf("loginCmd --token type = %q, want %q (regressed to bool?)", got, "string")
	}
	if tokenFlag.NoOptDefVal != tokenPromptSentinel {
		t.Fatalf("loginCmd --token NoOptDefVal = %q, want %q (legacy `multica login --token` prompt mode would break)", tokenFlag.NoOptDefVal, tokenPromptSentinel)
	}
}

// TestLoginTokenHelpOutputRendersCleanly renders loginCmd's flag help through
// the same pflag path `multica login -h` uses (FlagUsagesWrapped) and locks the
// user-visible help contract that regressed. The original bug had two causes,
// both invisible to the flag-wiring/parsing tests above:
//   - The NoOptDefVal sentinel was "\x00prompt". pflag renders NoOptDefVal
//     verbatim into the flag column AND uses "\x00" as its own column-alignment
//     marker, so the split-at-first-NUL logic mispadded the line and printed a
//     raw NUL to the terminal.
//   - The usage string wrapped the PAT example in backticks, so pflag's
//     UnquoteUsage hijacked it as the flag's value placeholder and stripped a
//     backtick pair from the description.
//
// A comment can't prevent either from recurring; only rendering the real output
// and asserting on it can. This is the regression guard for that output.
func TestLoginTokenHelpOutputRendersCleanly(t *testing.T) {
	help := loginCmd.Flags().FlagUsages()

	// No control byte may reach the terminal. pflag emits only spaces and
	// newlines for layout, so any other sub-0x20 rune (notably the NUL the old
	// "\x00prompt" sentinel leaked) means the help rendering is corrupted.
	for _, r := range help {
		if r < 0x20 && r != '\n' && r != '\t' {
			t.Fatalf("login help contains control byte %#x; rendered flag usage:\n%q", r, help)
		}
	}

	// The --token line must show the standard optional-value form. This single
	// assertion pins both root causes: the placeholder is `string` (backticks
	// removed, so UnquoteUsage no longer hijacks the PAT example) and the
	// optional value is the printable `prompt` (no NUL-prefixed sentinel).
	if want := `--token string[="prompt"]`; !strings.Contains(help, want) {
		t.Fatalf("login help missing %q; rendered flag usage:\n%q", want, help)
	}

	// The description must survive intact — a swallowed backtick pair used to
	// truncate it, so assert the tail of the sentence is still present.
	if want := "to be prompted interactively."; !strings.Contains(help, want) {
		t.Fatalf("login help missing description tail %q; rendered flag usage:\n%q", want, help)
	}
}

// TestLoginTokenFlagParsing exercises every documented invocation form
// against a cobra command wired up exactly the same way as the production
// loginCmd, then runs runAuthLogin's flag-resolution logic to confirm the
// right downstream branch is taken: `--token mul_xxx` and `--token=mul_xxx`
// both consume the value (the bug from #1994), `--token` alone falls
// through to the prompt sentinel (preserves the legacy headless form), and
// no flag at all leaves the browser flow untouched.
func TestLoginTokenFlagParsing(t *testing.T) {
	type want struct {
		changed         bool
		resolvedToken   string // empty == "fall through to prompt"
		expectsPrompted bool
	}

	cases := []struct {
		name string
		argv []string
		want want
	}{
		{
			name: "space-separated value (the form from #1994)",
			argv: []string{"--token", "mul_xxx"},
			want: want{changed: true, resolvedToken: "mul_xxx"},
		},
		{
			name: "equals-separated value",
			argv: []string{"--token=mul_yyy"},
			want: want{changed: true, resolvedToken: "mul_yyy"},
		},
		{
			name: "no value falls through to prompt (legacy CLI_INSTALL.md form)",
			argv: []string{"--token"},
			want: want{changed: true, expectsPrompted: true},
		},
		{
			name: "explicit empty value also falls through to prompt",
			argv: []string{"--token="},
			want: want{changed: true, expectsPrompted: true},
		},
		{
			name: "no flag at all → browser flow",
			argv: []string{},
			want: want{changed: false},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "login"}
			// Mirror loginCmd's exact flag wiring. If init() in cmd_login.go
			// regresses, TestLoginTokenFlagWiring catches that; here we test
			// the parsing behavior given the documented wiring.
			cmd.Flags().String("token", "", "")
			cmd.Flags().Lookup("token").NoOptDefVal = tokenPromptSentinel

			if err := cmd.ParseFlags(tc.argv); err != nil {
				t.Fatalf("ParseFlags(%v) error: %v", tc.argv, err)
			}
			if cmd.Flags().Changed("token") != tc.want.changed {
				t.Fatalf("Changed(token) = %v, want %v for argv=%v", cmd.Flags().Changed("token"), tc.want.changed, tc.argv)
			}
			if !tc.want.changed {
				return
			}

			// Replay runAuthLogin's resolution logic so the test fails if
			// either the flag wiring OR the space-form recovery breaks.
			tokenFlag, _ := cmd.Flags().GetString("token")
			positional := cmd.Flags().Args()
			if tokenFlag == tokenPromptSentinel && len(positional) == 1 {
				tokenFlag = positional[0]
			}

			if tc.want.expectsPrompted {
				if tokenFlag != tokenPromptSentinel && tokenFlag != "" {
					t.Fatalf("expected prompt fall-through, got resolved token %q", tokenFlag)
				}
			} else {
				if tokenFlag != tc.want.resolvedToken {
					t.Fatalf("resolved token = %q, want %q", tokenFlag, tc.want.resolvedToken)
				}
			}
		})
	}
}

func TestNormalizeAPIBaseURL(t *testing.T) {
	t.Run("converts websocket base URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("ws://localhost:18106/ws"); got != "http://localhost:18106" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "http://localhost:18106")
		}
	})

	t.Run("keeps http base URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("http://localhost:8080"); got != "http://localhost:8080" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "http://localhost:8080")
		}
	})

	t.Run("falls back to raw value for invalid URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("://bad-url"); got != "://bad-url" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "://bad-url")
		}
	})
}

// TestValidateLoginTokenPrefix pins the accepted PAT prefix set for
// `multica login --token`. The original implementation hardcoded `mul_`
// only, which rejected legitimate Multica Cloud Node PATs (`mcn_`) at
// the CLI even though the server's middleware would have accepted them.
// If a future change drops `mcn_` from the list (or accidentally
// broadens the set to anything-goes), this test fails.
func TestValidateLoginTokenPrefix(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{name: "mul_ PAT", token: "mul_abc123", wantErr: false},
		{name: "mcn_ Cloud Node PAT", token: "mcn_abc123", wantErr: false},
		{name: "empty token", token: "", wantErr: true},
		{name: "no prefix", token: "abc123", wantErr: true},
		{name: "wrong prefix mdt_", token: "mdt_abc123", wantErr: true},
		{name: "wrong prefix mat_", token: "mat_abc123", wantErr: true},
		{name: "case-sensitive: MUL_ rejected", token: "MUL_abc123", wantErr: true},
		{name: "leading whitespace not allowed (callers TrimSpace first)", token: " mul_abc", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validateLoginTokenPrefix(tc.token)
			if tc.wantErr && err == nil {
				t.Fatalf("validateLoginTokenPrefix(%q) = nil, want error", tc.token)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateLoginTokenPrefix(%q) = %v, want nil", tc.token, err)
			}
		})
	}

	// The error string is user-facing; make sure it lists every accepted
	// prefix so users hitting it can self-serve. Hardcoding the exact
	// prefixes here is deliberate — if someone adds a new prefix to
	// loginTokenPrefixes they should also update the docs / this test.
	err := validateLoginTokenPrefix("nope_xxx")
	if err == nil {
		t.Fatal("expected error for unknown prefix")
	}
	for _, p := range []string{"mul_", "mcn_"} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not mention prefix %q", err.Error(), p)
		}
	}
}

// ---------------------------------------------------------------------------
// HIV-806: capability-gated recovery of the task-scoped token over the
// daemon's loopback health listener. All tokens and capabilities below are
// dummy test values only.
// ---------------------------------------------------------------------------

const dummyTaskTokenCapability = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func writeTaskTokenCapabilityFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task-token-capability")
	if err := os.WriteFile(path, []byte(contents), 0o400); err != nil {
		t.Fatalf("write capability file: %v", err)
	}
	return path
}

// taskTokenRecoveryTestServer fakes POST /task-token. Only requests carrying
// the dummy capability and the exact task id succeed; everything else gets
// the same refusal the real daemon emits.
func taskTokenRecoveryTestServer(t *testing.T, status int, token string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost || r.URL.Path != taskTokenRecoveryPath || r.URL.RawQuery != "" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get(taskTokenCapabilityHeader); got != dummyTaskTokenCapability {
			http.Error(w, "task token unavailable", http.StatusUnauthorized)
			return
		}
		var req struct {
			TaskID string `json:"task_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TaskID != "task-recover-1" {
			http.Error(w, "task token unavailable", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func loopbackPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host:port of %q: %v", u.Host, err)
	}
	return port
}

func closedLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return strconv.Itoa(port)
}

func setTaskTokenRecoveryEnv(t *testing.T, port, capabilityFile string) {
	t.Helper()
	t.Setenv("MULTICA_TOKEN", "")
	t.Setenv("MULTICA_AGENT_ID", "agent-recover")
	t.Setenv("MULTICA_TASK_ID", "task-recover-1")
	t.Setenv("MULTICA_DAEMON_PORT", port)
	if capabilityFile == "" {
		t.Setenv(taskTokenCapabilityFileEnv, "")
	} else {
		t.Setenv(taskTokenCapabilityFileEnv, capabilityFile)
	}
}

func TestResolveTokenRecoversTaskTokenFromDaemon(t *testing.T) {
	srv, calls := taskTokenRecoveryTestServer(t, http.StatusOK, "mat_recovered_task_token")
	capFile := writeTaskTokenCapabilityFile(t, dummyTaskTokenCapability)
	setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), capFile)

	if got := resolveToken(testCmd()); got != "mat_recovered_task_token" {
		t.Fatalf("resolveToken() = %q, want recovered task-scoped token", got)
	}
	if atomic.LoadInt32(calls) == 0 {
		t.Fatal("recovery never reached the daemon endpoint")
	}
}

func TestResolveTokenEnvTokenStillWinsOverRecovery(t *testing.T) {
	srv, calls := taskTokenRecoveryTestServer(t, http.StatusOK, "mat_recovered_task_token")
	capFile := writeTaskTokenCapabilityFile(t, dummyTaskTokenCapability)
	setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), capFile)
	t.Setenv("MULTICA_TOKEN", "mat_from_env")

	if got := resolveToken(testCmd()); got != "mat_from_env" {
		t.Fatalf("resolveToken() = %q, want MULTICA_TOKEN to win", got)
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatal("env token present but recovery was still attempted")
	}
}

func TestResolveTokenRecoveryFailuresNeverFallBack(t *testing.T) {
	validCap := writeTaskTokenCapabilityFile(t, dummyTaskTokenCapability)

	t.Run("daemon down", func(t *testing.T) {
		setTaskTokenRecoveryEnv(t, closedLoopbackPort(t), validCap)
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty when daemon is unreachable", got)
		}
	})

	t.Run("invalid port value", func(t *testing.T) {
		setTaskTokenRecoveryEnv(t, "not-a-port", validCap)
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty for non-numeric port", got)
		}
	})

	t.Run("capability env missing means no recovery attempt", func(t *testing.T) {
		srv, calls := taskTokenRecoveryTestServer(t, http.StatusOK, "mat_recovered_task_token")
		setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), "")
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty without capability pointer", got)
		}
		if atomic.LoadInt32(calls) != 0 {
			t.Fatal("recovery attempted without capability env")
		}
	})

	t.Run("capability file missing", func(t *testing.T) {
		srv, _ := taskTokenRecoveryTestServer(t, http.StatusOK, "mat_recovered_task_token")
		setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), filepath.Join(t.TempDir(), "absent"))
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty when capability file is missing", got)
		}
	})

	t.Run("empty capability file", func(t *testing.T) {
		srv, _ := taskTokenRecoveryTestServer(t, http.StatusOK, "mat_recovered_task_token")
		setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), writeTaskTokenCapabilityFile(t, "   \n"))
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty for empty capability", got)
		}
	})

	t.Run("capability file larger than the cap", func(t *testing.T) {
		srv, _ := taskTokenRecoveryTestServer(t, http.StatusOK, "mat_recovered_task_token")
		setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), writeTaskTokenCapabilityFile(t, strings.Repeat("x", maxTaskTokenCapabilityBytes+1)))
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty for oversized capability file", got)
		}
	})

	t.Run("wrong capability rejected by daemon", func(t *testing.T) {
		srv, _ := taskTokenRecoveryTestServer(t, http.StatusOK, "mat_recovered_task_token")
		setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), writeTaskTokenCapabilityFile(t, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"))
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty when daemon refuses the capability", got)
		}
	})

	t.Run("non-200 daemon response", func(t *testing.T) {
		srv, _ := taskTokenRecoveryTestServer(t, http.StatusInternalServerError, "")
		setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), validCap)
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty on 500", got)
		}
	})

	t.Run("non mat_ token in response is refused", func(t *testing.T) {
		srv, _ := taskTokenRecoveryTestServer(t, http.StatusOK, "mul_member_pat")
		setTaskTokenRecoveryEnv(t, loopbackPort(t, srv.URL), validCap)
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty: only mat_ tokens are acceptable", got)
		}
	})

	// The recovery failure must not fall back to the user-global config
	// token — that is the wrong-actor path resolveToken's daemon gate exists
	// to prevent. Seed a config with a member PAT and keep recovery failing.
	t.Run("no member PAT fallback through failed recovery", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if err := cli.SaveCLIConfig(cli.CLIConfig{Token: "mul_profile_token"}); err != nil {
			t.Fatalf("seed config: %v", err)
		}
		setTaskTokenRecoveryEnv(t, closedLoopbackPort(t), validCap)
		if got := resolveToken(testCmd()); got != "" {
			t.Fatalf("resolveToken() = %q, want empty: recovery failure must not reach the profile PAT", got)
		}
	})
}

// TestRecoverTaskTokenFromDaemonRequiresDaemonIdentity pins that recovery
// only runs with the full triple (task id, daemon port, capability pointer):
// a workdir marker alone must never trigger a loopback attempt.
func TestRecoverTaskTokenFromDaemonRequiresDaemonIdentity(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) string // returns port
	}{
		{name: "no task id", setup: func(t *testing.T) string {
			t.Setenv("MULTICA_TASK_ID", "")
			t.Setenv("MULTICA_DAEMON_PORT", "29501")
			t.Setenv(taskTokenCapabilityFileEnv, writeTaskTokenCapabilityFile(t, dummyTaskTokenCapability))
			return "29501"
		}},
		{name: "no daemon port", setup: func(t *testing.T) string {
			t.Setenv("MULTICA_TASK_ID", "task-recover-1")
			t.Setenv("MULTICA_DAEMON_PORT", "")
			t.Setenv(taskTokenCapabilityFileEnv, writeTaskTokenCapabilityFile(t, dummyTaskTokenCapability))
			return "29501"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MULTICA_AGENT_ID", "agent-recover")
			t.Setenv("MULTICA_TOKEN", "")
			tc.setup(t)
			if got := recoverTaskTokenFromDaemon(); got != "" {
				t.Fatalf("recoverTaskTokenFromDaemon() = %q, want empty", got)
			}
		})
	}
}
