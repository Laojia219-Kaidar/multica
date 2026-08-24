package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ─── ACP terminal bridge (HIV-862) ───
//
// Kimi Code CLI 0.37.2 executes its native Bash tool through the ACP
// terminal methods instead of an in-process shell: the agent asks the
// *client* (this daemon) to spawn the process (terminal/create), polls
// the combined output roughly every 250ms (terminal/output), awaits the
// exit status (terminal/wait_for_exit), and terminates / frees the
// terminal (terminal/kill, terminal/release). kimi-cli refuses every one
// of those calls with "ACP terminal capability is unavailable" unless the
// client advertised clientCapabilities.terminal=true in initialize —
// exactly why HIV-861's otherwise-correct launch never got a working
// terminal tool.
//
// The wire shapes below are pinned to the zod schemas bundled inside
// @moonshot-ai/kimi-code 0.37.2 (ACP SDK 0.23.0), so the daemon replies
// with what that CLI actually parses:
//
//	terminal/create        {sessionId, command, args[], env[{name,value}], cwd, outputByteLimit} → {terminalId}
//	terminal/output        {sessionId, terminalId} → {output, truncated, exitStatus?{exitCode?,signal?}}
//	terminal/wait_for_exit {sessionId, terminalId} → {exitCode?, signal?}   (blocks until the process exits)
//	terminal/kill          {sessionId, terminalId} → {}                     (terminal stays valid)
//	terminal/release       {sessionId, terminalId} → {}                     (the id becomes invalid)
//
// One acpTerminalManager belongs to exactly one hermesClient, i.e. one
// Task run. Terminals are therefore Task-scoped: cancelling the run
// context kills every spawned process group, and client shutdown
// (closeAll) kills whatever is left. A hermesClient whose backend did not
// advertise the terminal capability keeps terminals == nil, and
// terminal/* requests then keep receiving the pre-existing fail-closed
// JSON-RPC "method not found" reply.

const (
	// acpTerminalMaxOutputBytes is the daemon-side ceiling on captured
	// combined output per terminal. Kimi asks for 4 MiB
	// (OUTPUT_BYTE_LIMIT in kimi-cli's acpTerminalRunner); a smaller
	// positive outputByteLimit from the request is honoured, anything
	// at or above this ceiling is clamped to it.
	acpTerminalMaxOutputBytes = 1 << 20 // 1 MiB

	// acpTerminalWaitGrace bounds how long a blocked
	// terminal/wait_for_exit keeps waiting for the reaper once the
	// owning run context was already cancelled, so a wedged child can
	// never hang the JSON-RPC reply (and the Run) indefinitely.
	acpTerminalWaitGrace = time.Second

	// acpTerminalReleaseWait bounds the synchronous reaping window
	// inside terminal/release before the (already killed) terminal id
	// is invalidated.
	acpTerminalReleaseWait = 2 * time.Second

	// acpTerminalPipeDrainDelay is exec.Cmd.WaitDelay for spawned
	// terminal processes: after the process exits, grandchildren that
	// inherited the output pipes get this long to finish before the
	// pipes are force-closed and cmd.Wait returns.
	acpTerminalPipeDrainDelay = 2 * time.Second
)

// acpTerminalHostEnvAllowlist is the complete set of GENERIC host
// environment variables a terminal child may inherit (the two
// task-scoped daemon variables below are the only additions). Terminal
// processes run model-requested commands, so they must NOT inherit the
// daemon's credentials (provider API keys injected through Config.Env,
// MULTICA_TOKEN, …) the way the agent CLI process itself does. Only
// names needed by ordinary development commands are allowed through;
// everything else the child needs must arrive as request-scoped env
// (merged and screened by acpTerminalChildEnv).
var acpTerminalHostEnvAllowlist = []string{
	// POSIX / macOS / Linux
	"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TZ",
	"SHELL", "USER", "LOGNAME",
	"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
	// Windows
	"TEMP", "TMP", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "USERNAME",
	"SYSTEMROOT", "COMSPEC", "PATHEXT", "PROGRAMDATA", "APPDATA",
	"LOCALAPPDATA", "PROGRAMFILES",
}

// acpTerminalTrustedTaskEnv is the exact, closed set of task-scoped
// daemon variables a terminal child inherits IN ADDITION to the generic
// allowlist above, and the only MULTICA_* names it may ever see. The
// HIV-877 canary showed the native ACP terminal working while its child
// environment could not recover the task-scoped Multica credential,
// because the bridge stripped MULTICA_DAEMON_PORT and
// MULTICA_LOCAL_AUTH_CAPABILITY_FILE along with every other MULTICA_*
// host variable. These two carry only the local daemon's port and a
// task-scoped capability FILE PATH — never a credential value — so the
// CLI inside the terminal can reach the daemon and recover the task
// credential itself.
//
// Since HIV-880 the source is the Task's own Config.Env: the daemon
// injects both pointers into the Task-specific agentEnv (which becomes
// the Kimi CLI process environment), NOT into the daemon's own process
// environment — which is why the HIV-878 attempt of reading
// os.Environ() still failed live (HIV-879). The kimi backend extracts
// exactly these two names from Config.Env
// (acpTerminalTrustedTaskEnvFromConfig) and hands the already-filtered
// entries to the Task-owned terminal manager; the daemon's host
// environment is never consulted for them. MULTICA_TOKEN, provider/API
// credentials and every other Config.Env or MULTICA_* value stay
// excluded, and request-scoped env still rejects the whole MULTICA_*
// namespace (isACPTerminalCredentialEnvName), so the model can neither
// inject nor override either trusted value. The capability file itself
// is never opened, parsed or logged — only its path string passes
// through. Do not add more names here without a new security-reviewed
// work order.
var acpTerminalTrustedTaskEnv = []string{
	"MULTICA_DAEMON_PORT",
	"MULTICA_LOCAL_AUTH_CAPABILITY_FILE",
}

// acpTerminalEnvNameRe is the shape a request-scoped env name must have
// to be merged into a terminal child's environment. `=` can never appear
// in a POSIX env name; anything else is malformed and rejected.
var acpTerminalEnvNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// acpTerminalCredentialEnvFragments names request-scoped env variables
// whose upper-cased name contains any of these substrings are rejected
// outright: the terminal runs model-chosen commands, so smuggling a
// provider/API credential (or the daemon's own task token) into that
// environment must fail closed. Representative blocked names:
// KIMI_API_KEY, MOONSHOT_API_KEY, OPENAI_API_KEY (API_KEY), AWS_SESSION_TOKEN
// (TOKEN), ANTHROPIC_AUTH_TOKEN (AUTH), GITHUB_TOKEN (TOKEN).
var acpTerminalCredentialEnvFragments = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "PASSPHRASE", "CREDENTIAL",
	"APIKEY", "API_KEY", "PRIVATE_KEY", "ACCESS_KEY", "SESSION_KEY",
	"AUTH", "COOKIE",
}

// acpTerminalError is a structured JSON-RPC error for a terminal method
// reply. code follows JSON-RPC / kimi-cli conventions: -32602 invalid
// params, -32603 internal, and -32002 resource-not-found (kimi-cli's
// isResourceNotFound) for unknown / released / foreign-session terminals.
type acpTerminalError struct {
	code    int
	message string
}

func (e *acpTerminalError) Error() string { return e.message }

func acpTerminalInvalidParams(format string, args ...any) *acpTerminalError {
	return &acpTerminalError{code: -32602, message: fmt.Sprintf(format, args...)}
}

func acpTerminalNotFound(message string) *acpTerminalError {
	return &acpTerminalError{code: -32002, message: message}
}

func acpTerminalInternal(format string, args ...any) *acpTerminalError {
	return &acpTerminalError{code: -32603, message: fmt.Sprintf(format, args...)}
}

// acpTerminalEnvVar is one entry of terminal/create's env array.
type acpTerminalEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// acpTerminalCreateParams mirrors zCreateTerminalRequest from the ACP
// schema bundled with kimi-cli 0.37.2. args/env may be absent; cwd and
// outputByteLimit may be null.
type acpTerminalCreateParams struct {
	SessionID       string              `json:"sessionId"`
	Command         string              `json:"command"`
	Args            []string            `json:"args"`
	Env             []acpTerminalEnvVar `json:"env"`
	Cwd             *string             `json:"cwd"`
	OutputByteLimit *int64              `json:"outputByteLimit"`
}

// acpTerminalIDParams mirrors the {sessionId, terminalId} shape shared by
// terminal/output, terminal/wait_for_exit, terminal/kill and
// terminal/release.
type acpTerminalIDParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// acpTerminalExitStatus mirrors zTerminalExitStatus / the
// terminal/wait_for_exit result: exitCode is a uint32 and omitted when
// the process died from a signal; signal carries the signal name then.
type acpTerminalExitStatus struct {
	ExitCode *uint32 `json:"exitCode,omitempty"`
	Signal   string  `json:"signal,omitempty"`
}

// acpTerminalOutputResponse mirrors zTerminalOutputResponse. output and
// truncated are required by the kimi-cli schema and must always be
// present, even when empty.
type acpTerminalOutputResponse struct {
	Output     string                 `json:"output"`
	Truncated  bool                   `json:"truncated"`
	ExitStatus *acpTerminalExitStatus `json:"exitStatus,omitempty"`
}

// acpTerminal is one spawned host process plus its bounded combined
// output buffer. The zero value is not usable; create via
// acpTerminalManager.create.
type acpTerminal struct {
	id        string
	sessionID string
	cmd       *exec.Cmd
	limit     int           // output buffer cap in bytes
	done      chan struct{} // closed exactly once when cmd.Wait returned

	mu        sync.Mutex
	buf       []byte // combined stdout+stderr, keeping the LAST limit bytes
	truncated bool   // sticky: set once any captured bytes were dropped
	exited    bool
	exit      acpTerminalExitStatus
}

// Write appends captured output bytes, dropping the oldest bytes once the
// buffer exceeds the terminal's limit. It implements io.Writer so exec's
// copy goroutines for stdout and stderr both feed the same combined,
// bounded buffer.
func (t *acpTerminal) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.truncated = true
	}
	return len(p), nil
}

// snapshot returns the accumulated output (sanitised to valid UTF-8 so
// JSON encoding is lossless), the sticky truncation flag, and the exit
// status once the process has been reaped.
func (t *acpTerminal) snapshot() (output string, truncated bool, exit acpTerminalExitStatus, exited bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// json.Marshal replaces invalid UTF-8 with U+FFFD per byte, which
	// would make the sanitised string unstable across polls; normalise
	// here so each snapshot of the same buffer is byte-identical and
	// kimi's emitted-offset bookkeeping (output.length) stays monotonic.
	output = strings.ToValidUTF8(string(t.buf), "�")
	return output, t.truncated, t.exit, t.exited
}

// markFinished records the reaped exit status and closes done exactly
// once. err is cmd.Wait's return value; a nil err means exit code 0.
func (t *acpTerminal) markFinished(err error) {
	status := acpTerminalExitStatusFromWait(err)
	t.mu.Lock()
	if !t.exited {
		t.exited = true
		t.exit = status
		close(t.done)
	}
	t.mu.Unlock()
}

// acpTerminalExitStatusFromWait converts cmd.Wait's error into the ACP
// exit status shape. Portability note: unix WaitStatus exposes Signal();
// the interface assertion simply fails on platforms whose implementation
// lacks it (Windows reports exit codes only).
func acpTerminalExitStatusFromWait(err error) acpTerminalExitStatus {
	if err == nil {
		code := uint32(0)
		return acpTerminalExitStatus{ExitCode: &code}
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// Process never started or an I/O error — no honest status.
		return acpTerminalExitStatus{}
	}
	if code := exitErr.ProcessState.ExitCode(); code >= 0 {
		c := uint32(code)
		return acpTerminalExitStatus{ExitCode: &c}
	}
	if ws, ok := exitErr.ProcessState.Sys().(interface{ Signal() syscall.Signal }); ok {
		return acpTerminalExitStatus{Signal: ws.Signal().String()}
	}
	return acpTerminalExitStatus{Signal: "killed"}
}

// kill terminates the terminal's whole process group (the spawned
// command plus any descendants it forked), falling back to the single
// process. Idempotent: signalling an already-reaped group is a no-op.
func (t *acpTerminal) kill() {
	if t.cmd != nil && t.cmd.Process != nil {
		signalProcessGroup(t.cmd.Process, syscall.SIGKILL)
	}
}

// acpTerminalManager owns every terminal created for one ACP client
// (one Task run). All methods are safe for concurrent use.
type acpTerminalManager struct {
	ctx    context.Context // the Task run context; cancellation kills every terminal
	cwd    string          // default cwd for terminals whose create params omit one
	logger *slog.Logger

	// trustedTaskEnv is the immutable, already-vetted set of trusted
	// task-scoped daemon pointers ("KEY=VALUE" entries) every terminal
	// child of THIS Task inherits in addition to the generic host
	// allowlist — at most MULTICA_DAEMON_PORT and
	// MULTICA_LOCAL_AUTH_CAPABILITY_FILE, sourced from this Task's
	// Config.Env (HIV-880). Written exactly once at construction and
	// never mutated afterwards; empty when the Task config carries
	// neither pointer.
	trustedTaskEnv []string

	mu        sync.Mutex
	closed    bool
	terminals map[string]*acpTerminal
}

// newACPTerminalManager builds the terminal bridge for one ACP client.
// ctx must be the same context that bounds the agent CLI process, so
// cancelling the Task kills the terminals with it.
//
// trustedTaskEnv optionally carries the ALREADY-FILTERED trusted task
// pointers ("KEY=VALUE" entries, at most MULTICA_DAEMON_PORT and
// MULTICA_LOCAL_AUTH_CAPABILITY_FILE) sourced from THIS Task's
// Config.Env — the Task-specific agentEnv the daemon assembles, which
// is where both pointers live in every live deployment (HIV-879/880).
// The constructor defensively vets the entries into a fresh, immutable
// slice: an entry whose name is not one of the two trusted names is
// dropped silently, so no caller can ever smuggle MULTICA_TOKEN, a
// provider/API credential or any other value into a terminal child
// through this path. The variadic form keeps the zero-trusted-env call
// used by tests valid.
func newACPTerminalManager(ctx context.Context, cwd string, logger *slog.Logger, trustedTaskEnv ...string) *acpTerminalManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &acpTerminalManager{
		ctx:            ctx,
		cwd:            cwd,
		logger:         logger,
		terminals:      make(map[string]*acpTerminal),
		trustedTaskEnv: vetACPTerminalTrustedTaskEnv(trustedTaskEnv),
	}
}

// acpTerminalTrustedTaskEnvFromConfig extracts exactly the trusted
// task-scoped daemon pointers from one Task's Config.Env: it reads only
// the two names in acpTerminalTrustedTaskEnv and passes their values
// through verbatim as "KEY=VALUE" entries. MULTICA_TOKEN, provider/API
// credentials and every other Config.Env entry never reach the terminal
// bridge, and the capability file itself is never opened or parsed —
// the value is a path string the terminal child needs as-is (HIV-880).
func acpTerminalTrustedTaskEnvFromConfig(cfgEnv map[string]string) []string {
	trusted := make([]string, 0, len(acpTerminalTrustedTaskEnv))
	for _, key := range acpTerminalTrustedTaskEnv {
		if v, ok := cfgEnv[key]; ok {
			trusted = append(trusted, key+"="+v)
		}
	}
	return trusted
}

// vetACPTerminalTrustedTaskEnv copies entries into a fresh slice keeping
// only well-formed entries whose name is one of the two trusted task
// pointers (first occurrence wins). This is the manager-boundary fence:
// even a future caller passing unfiltered Config.Env entries cannot leak
// MULTICA_TOKEN or a provider credential into a terminal child.
func vetACPTerminalTrustedTaskEnv(entries []string) []string {
	if len(entries) == 0 {
		return nil
	}
	vetted := make([]string, 0, len(acpTerminalTrustedTaskEnv))
	seen := make(map[string]bool, len(acpTerminalTrustedTaskEnv))
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || seen[key] {
			continue
		}
		trusted := false
		for _, name := range acpTerminalTrustedTaskEnv {
			if key == name {
				trusted = true
				break
			}
		}
		if !trusted {
			continue
		}
		seen[key] = true
		vetted = append(vetted, entry)
	}
	if len(vetted) == 0 {
		return nil
	}
	return vetted
}

// isACPTerminalMethod reports whether method is one of the ACP terminal
// client methods this bridge serves.
func isACPTerminalMethod(method string) bool {
	switch method {
	case "terminal/create", "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release":
		return true
	}
	return false
}

// dispatch serves one agent→client terminal request and returns the
// JSON-RPC result or a structured error. It never blocks the caller
// indefinitely: wait_for_exit is bounded by the run context plus
// acpTerminalWaitGrace.
func (m *acpTerminalManager) dispatch(method string, params json.RawMessage) (any, *acpTerminalError) {
	switch method {
	case "terminal/create":
		return m.create(params)
	case "terminal/output":
		p, err := parseACPTerminalIDParams(params)
		if err != nil {
			return nil, err
		}
		t, err := m.lookup(p)
		if err != nil {
			return nil, err
		}
		output, truncated, exit, exited := t.snapshot()
		resp := acpTerminalOutputResponse{Output: output, Truncated: truncated}
		if exited {
			e := exit
			resp.ExitStatus = &e
		}
		return resp, nil
	case "terminal/wait_for_exit":
		p, err := parseACPTerminalIDParams(params)
		if err != nil {
			return nil, err
		}
		t, err := m.lookup(p)
		if err != nil {
			return nil, err
		}
		return m.waitForExit(t)
	case "terminal/kill":
		p, err := parseACPTerminalIDParams(params)
		if err != nil {
			return nil, err
		}
		t, err := m.lookup(p)
		if err != nil {
			return nil, err
		}
		t.kill()
		m.logger.Debug("acp terminal killed", "terminal_id", t.id)
		return map[string]any{}, nil
	case "terminal/release":
		p, err := parseACPTerminalIDParams(params)
		if err != nil {
			return nil, err
		}
		return m.release(p)
	}
	return nil, acpTerminalInvalidParams("method not found: %s", method)
}

// create implements terminal/create: validate the request, spawn the
// process tied to the Task context, and register it under a fresh
// unguessable id.
func (m *acpTerminalManager) create(params json.RawMessage) (any, *acpTerminalError) {
	var p acpTerminalCreateParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, acpTerminalInvalidParams("invalid terminal/create params: %v", err)
		}
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return nil, acpTerminalInvalidParams("terminal/create requires a sessionId")
	}
	if p.Command == "" {
		return nil, acpTerminalInvalidParams("terminal/create requires a non-empty command")
	}
	childEnv, err := acpTerminalChildEnv(m.trustedTaskEnv, p.Env)
	if err != nil {
		return nil, err
	}
	limit := acpTerminalMaxOutputBytes
	if p.OutputByteLimit != nil {
		if *p.OutputByteLimit <= 0 {
			return nil, acpTerminalInvalidParams("terminal/create outputByteLimit must be positive, got %d", *p.OutputByteLimit)
		}
		if *p.OutputByteLimit < int64(acpTerminalMaxOutputBytes) {
			limit = int(*p.OutputByteLimit)
		}
	}
	dir := m.cwd
	if p.Cwd != nil && *p.Cwd != "" {
		dir = *p.Cwd
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, acpTerminalInternal("terminal service is shutting down")
	}
	m.mu.Unlock()

	// The terminal process lives and dies with the Task run: cancelling
	// m.ctx fires cmd.Cancel, which signals the whole process group so
	// descendants (the shell's children) die too, not just the leader.
	cmd := exec.CommandContext(m.ctx, p.Command, p.Args...)
	configureProcessGroup(cmd)
	hideAgentWindow(cmd)
	cmd.Dir = dir
	cmd.Env = childEnv
	cmd.Cancel = func() error {
		signalProcessGroup(cmd.Process, syscall.SIGKILL)
		return nil
	}
	cmd.WaitDelay = acpTerminalPipeDrainDelay

	t := &acpTerminal{
		sessionID: p.SessionID,
		cmd:       cmd,
		limit:     limit,
		done:      make(chan struct{}),
	}
	// Combined stdout+stderr feed the same bounded buffer via exec's
	// copy goroutines (Write is mutex-serialised).
	cmd.Stdout = t
	cmd.Stderr = t

	if err := cmd.Start(); err != nil {
		return nil, acpTerminalInternal("terminal/create failed to start command: %v", err)
	}
	go func() {
		t.markFinished(cmd.Wait())
	}()

	id, idErr := acpTerminalNewID()
	if idErr != nil {
		t.kill()
		return nil, acpTerminalInternal("terminal/create id generation failed: %v", idErr)
	}
	t.id = id

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		t.kill()
		return nil, acpTerminalInternal("terminal service is shutting down")
	}
	m.terminals[id] = t
	m.mu.Unlock()

	m.logger.Debug("acp terminal created",
		"terminal_id", id,
		"env_vars", len(p.Env),
		"output_limit", limit,
	)
	return map[string]any{"terminalId": id}, nil
}

// acpTerminalNewID mints an unguessable terminal id (192 bits of
// crypto/rand), so ids from one Task can never be guessed or collided
// against another client's.
func acpTerminalNewID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "term-" + hex.EncodeToString(b[:]), nil
}

// parseACPTerminalIDParams validates the {sessionId, terminalId} shape
// shared by every post-create terminal method.
func parseACPTerminalIDParams(params json.RawMessage) (acpTerminalIDParams, *acpTerminalError) {
	var p acpTerminalIDParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return p, acpTerminalInvalidParams("invalid terminal request params: %v", err)
		}
	}
	if p.TerminalID == "" {
		return p, acpTerminalInvalidParams("terminal request requires a terminalId")
	}
	return p, nil
}

// lookup resolves a terminal id for the requesting session. A terminal
// belongs to exactly one ACP client and one session: unknown ids, ids
// released earlier, and ids addressed from a different session all fail
// closed with the same resource-not-found error (no existence oracle).
func (m *acpTerminalManager) lookup(p acpTerminalIDParams) (*acpTerminal, *acpTerminalError) {
	m.mu.Lock()
	t, ok := m.terminals[p.TerminalID]
	m.mu.Unlock()
	if !ok || t.sessionID != p.SessionID {
		return nil, acpTerminalNotFound("terminal not found: " + p.TerminalID)
	}
	return t, nil
}

// waitForExit blocks until the process is reaped and returns its ACP exit
// status. Bounded: once the run context is cancelled the wait resolves
// within acpTerminalWaitGrace either way, so a reply always goes out.
func (m *acpTerminalManager) waitForExit(t *acpTerminal) (any, *acpTerminalError) {
	select {
	case <-t.done:
	case <-m.ctx.Done():
		select {
		case <-t.done:
		case <-time.After(acpTerminalWaitGrace):
			return nil, acpTerminalInternal("terminal wait aborted: run context cancelled")
		}
	}
	_, _, exit, _ := t.snapshot()
	return exit, nil
}

// release implements terminal/release: validate ownership under the same
// lock that deletes the entry, THEN kill the process group if it is
// still running, wait briefly for the reaper, and invalidate the id so
// every later terminal/* request for it fails closed. Deleting only
// after the sessionID check is load-bearing (review P1): with the
// delete-first order, a foreign session's rejected release still
// removed the owner's terminal from the registry — the owner then got
// not-found on terminal/output while its process kept running. The
// ownership check and the delete must be one atomic step so a foreign
// release can never invalidate a terminal it does not own.
func (m *acpTerminalManager) release(p acpTerminalIDParams) (any, *acpTerminalError) {
	m.mu.Lock()
	t, ok := m.terminals[p.TerminalID]
	if !ok || t.sessionID != p.SessionID {
		m.mu.Unlock()
		return nil, acpTerminalNotFound("terminal not found: " + p.TerminalID)
	}
	delete(m.terminals, p.TerminalID)
	m.mu.Unlock()
	t.kill()
	select {
	case <-t.done:
	case <-time.After(acpTerminalReleaseWait):
	}
	m.logger.Debug("acp terminal released", "terminal_id", t.id)
	return map[string]any{}, nil
}

// closeAll kills every live terminal and clears the registry. Called on
// client shutdown (the agent CLI process exiting or the Task finishing);
// non-blocking so teardown is never delayed by a stuck child.
func (m *acpTerminalManager) closeAll() {
	m.mu.Lock()
	m.closed = true
	terminals := make([]*acpTerminal, 0, len(m.terminals))
	for id, t := range m.terminals {
		terminals = append(terminals, t)
		delete(m.terminals, id)
	}
	m.mu.Unlock()
	for _, t := range terminals {
		t.kill()
	}
	if len(terminals) > 0 {
		m.logger.Debug("acp terminal bridge closed", "terminals", len(terminals))
	}
}

// isACPTerminalCredentialEnvName reports whether an env name is
// credential-shaped (or in the daemon's own namespace) and must never be
// merged into a terminal child's environment.
func isACPTerminalCredentialEnvName(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "MULTICA_") {
		return true
	}
	for _, frag := range acpTerminalCredentialEnvFragments {
		if strings.Contains(upper, frag) {
			return true
		}
	}
	return false
}

// acpTerminalChildEnv builds the terminal child's environment: ONLY the
// allowlisted generic host variables, the immutable trusted task-scoped
// daemon pointers already extracted from THIS Task's Config.Env
// (trustedTaskEnv, HIV-880), plus the request-scoped entries. The
// daemon's own process environment is consulted for the generic
// allowlist only; the rest of the agent CLI's Config.Env (provider
// credentials, MULTICA_TOKEN, …) is never consulted, and a
// credential-shaped request entry is rejected rather than silently
// dropped so the failure is visible on the wire.
func acpTerminalChildEnv(trustedTaskEnv []string, requestEnv []acpTerminalEnvVar) ([]string, *acpTerminalError) {
	env := make([]string, 0, len(acpTerminalHostEnvAllowlist)+len(trustedTaskEnv)+len(requestEnv))
	for _, key := range acpTerminalHostEnvAllowlist {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	// Trusted task identity (HIV-878/880): the already-vetted pointers
	// extracted from THIS Task's Config.Env — never os.Environ(), which
	// lacks both in every live deployment (HIV-879). The request path
	// below keeps rejecting every MULTICA_* name, so the model cannot
	// inject or override either.
	env = append(env, trustedTaskEnv...)
	for _, kv := range requestEnv {
		if !acpTerminalEnvNameRe.MatchString(kv.Name) {
			return nil, acpTerminalInvalidParams("terminal/create env has malformed name %q", kv.Name)
		}
		if isACPTerminalCredentialEnvName(kv.Name) {
			return nil, acpTerminalInvalidParams("terminal/create env rejects credential-like name %q", kv.Name)
		}
		env = append(env, kv.Name+"="+kv.Value)
	}
	return env, nil
}

// ensure io.Writer contract is satisfied at compile time.
var _ io.Writer = (*acpTerminal)(nil)
