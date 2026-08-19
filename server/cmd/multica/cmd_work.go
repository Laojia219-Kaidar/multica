package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/workentry"
)

// ---------------------------------------------------------------------------
// multica work — Universal Work Registration Kernel
//
// Commands use the authenticated HiveCrew API by default. Passing --state
// selects the explicit offline candidate ledger, preserving the deterministic
// resolve → register → status flow used by canaries and disconnected carriers.
// ---------------------------------------------------------------------------

var workCmd = &cobra.Command{
	Use:   "work",
	Short: "Register and track work entries (resolve/start/status/heartbeat/event/handoff/finish/sync/doctor)",
}

var workResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Resolve ownership and dedupe disposition (read-only)",
	RunE:  runWorkResolve,
}

var workRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register work and return a work_ref (idempotent)",
	RunE:  runWorkRegister,
}

var workStartCmd = &cobra.Command{
	Use:   "start <work_ref>",
	Short: "Mark execution start for a work_ref (appends a started event)",
	Args:  exactArgs(1),
	RunE:  runWorkStart,
}

var workStatusCmd = &cobra.Command{
	Use:   "status <work_ref>",
	Short: "Read the current status for a work_ref",
	Args:  exactArgs(1),
	RunE:  runWorkStatus,
}

var workHeartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Report terminal/presence heartbeat",
	RunE:  runWorkHeartbeat,
}

var workEventCmd = &cobra.Command{
	Use:   "event",
	Short: "Append a structured work event",
	RunE:  runWorkEvent,
}

var workHandoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Submit a candidate handoff package",
	RunE:  runWorkHandoff,
}

var workFinishCmd = &cobra.Command{
	Use:   "finish",
	Short: "Submit a completion candidate for independent review",
	RunE:  runWorkFinish,
}

var workSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Replay an ordered offline spool (idempotent)",
	RunE:  runWorkSync,
}

var workDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose the unclaimed inbox (list/attach/ignore)",
	RunE:  runWorkDoctor,
}

var workDoctorAttachCmd = &cobra.Command{
	Use:   "attach",
	Short: "Attach an unclaimed inbox entry to a project/issue",
	RunE:  runWorkDoctorAttach,
}

var workDoctorIgnoreCmd = &cobra.Command{
	Use:   "ignore",
	Short: "Ignore an unclaimed inbox entry",
	RunE:  runWorkDoctorIgnore,
}

var workReplayCmd = &cobra.Command{
	Use:   "replay",
	Short: "Replay the original receipt or event for an idempotency key",
	RunE:  runWorkReplay,
}

func init() {
	workCmd.PersistentFlags().String("state", "", "Use an offline candidate ledger snapshot at this path; omit to call the authenticated HiveCrew API")
	workCmd.PersistentFlags().String("output", "json", "Output format: json (default) or table")

	workCmd.AddCommand(
		workResolveCmd, workRegisterCmd, workStartCmd, workStatusCmd,
		workHeartbeatCmd, workEventCmd, workHandoffCmd, workFinishCmd,
		workSyncCmd, workDoctorCmd, workReplayCmd,
	)

	addRequestFlags(workResolveCmd)
	addRequestFlags(workRegisterCmd)
	workRegisterCmd.Flags().Bool("confirm-create", false, "Authorize step-7 creation when ownership is not confirmed")

	workStartCmd.Flags().String("session-id", "", "Executing session id")
	workStartCmd.Flags().String("run-id", "", "Executing run id (task id)")
	workStartCmd.Flags().String("actor-id", "", "Actor id")

	workHeartbeatCmd.Flags().String("actor-id", "", "Actor id (required)")
	workHeartbeatCmd.Flags().String("session-id", "", "Session id (required)")
	workHeartbeatCmd.Flags().String("host", "", "Physical host / machine title")
	workHeartbeatCmd.Flags().String("session-name", "", "Terminal session name")
	workHeartbeatCmd.Flags().Int("window-index", 0, "Terminal window index")
	workHeartbeatCmd.Flags().Int("pane-index", 0, "Terminal pane index")
	workHeartbeatCmd.Flags().String("current-command", "", "Current command")
	workHeartbeatCmd.Flags().String("agent-hint", "", "Agent hint")

	addRequestFlags(workEventCmd)
	addRequestFlags(workHandoffCmd)
	addRequestFlags(workFinishCmd)
	addRequestFlags(workSyncCmd)

	workDoctorCmd.AddCommand(workDoctorAttachCmd, workDoctorIgnoreCmd)
	workDoctorAttachCmd.Flags().String("inbox-id", "", "Inbox entry id (required)")
	workDoctorAttachCmd.Flags().String("project-id", "", "Target project id")
	workDoctorAttachCmd.Flags().String("issue-id", "", "Target issue id")
	workDoctorIgnoreCmd.Flags().String("inbox-id", "", "Inbox entry id (required)")
	workDoctorIgnoreCmd.Flags().String("reason", "", "Ignore reason")

	workReplayCmd.Flags().String("idempotency-key", "", "Idempotency key (dedupe_key or event idempotency_key; required)")
	workReplayCmd.Flags().String("kind", "receipt", "Replay kind: receipt or event")
	workReplayCmd.Flags().String("work-ref", "", "work_ref (required for event replay)")
}

// addRequestFlags registers the shared JSON request body flags.
func addRequestFlags(cmd *cobra.Command) {
	cmd.Flags().String("request", "", "Request body as JSON")
	cmd.Flags().Bool("request-stdin", false, "Read request body from stdin")
	cmd.Flags().String("request-file", "", "Read request body from a JSON file")
	cmd.Flags().Bool("allow-external-file", false, "Allow --request-file outside the current working directory")
}

// readRequestJSON resolves the --request/--request-stdin/--request-file body
// and unmarshals it into dst.
func readRequestJSON(cmd *cobra.Command, dst any) error {
	body, ok, err := resolveTextFlag(cmd, "request")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("provide --request '<json>', --request-stdin, or --request-file <path>")
	}
	if err := workentry.RejectForbiddenProofFields([]byte(body)); err != nil {
		return fmt.Errorf("reject forbidden proof field: %w", err)
	}
	if err := json.Unmarshal([]byte(body), dst); err != nil {
		return fmt.Errorf("parse request JSON: %w", err)
	}
	return nil
}

func workOutput(cmd *cobra.Command) string {
	out, _ := cmd.Flags().GetString("output")
	if out == "" {
		return "json"
	}
	return out
}

func workWorkspace(cmd *cobra.Command) string {
	return cli.FlagOrEnv(cmd, "workspace-id", "MULTICA_WORKSPACE_ID", "")
}

func workUsesLiveAPI(cmd *cobra.Command) bool {
	statePath, _ := cmd.Flags().GetString("state")
	return strings.TrimSpace(statePath) == ""
}

func workAPIClient(cmd *cobra.Command) (*cli.APIClient, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(client.WorkspaceID) == "" {
		return nil, fmt.Errorf("--workspace-id (or MULTICA_WORKSPACE_ID/profile workspace_id) is required")
	}
	return client, nil
}

func workPostJSON(cmd *cobra.Command, path string, body, out any) error {
	client, err := workAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	return client.PostJSON(ctx, path, body, out)
}

func workGetJSON(cmd *cobra.Command, path string, out any) error {
	client, err := workAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	return client.GetJSON(ctx, path, out)
}

func workLoadStore(cmd *cobra.Command) (*workentry.MemoryStore, error) {
	store := workentry.NewMemoryStore()
	statePath, _ := cmd.Flags().GetString("state")
	if statePath == "" {
		return store, nil
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, fmt.Errorf("read --state file: %w", err)
	}
	var snap workentry.MemorySnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parse --state file %q: %w", statePath, err)
	}
	store.Restore(snap)
	return store, nil
}

func workSaveStore(cmd *cobra.Command, store *workentry.MemoryStore) error {
	statePath, _ := cmd.Flags().GetString("state")
	if statePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(store.Snapshot(), "", "  ")
	if err != nil {
		return fmt.Errorf("encode --state snapshot: %w", err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		return fmt.Errorf("write --state file: %w", err)
	}
	return nil
}

func workPrintJSON(v any) error { return cli.PrintJSON(os.Stdout, v) }

func runWorkResolve(cmd *cobra.Command, _ []string) error {
	var req workentry.ResolveRequest
	if err := readRequestJSON(cmd, &req); err != nil {
		return err
	}
	if workUsesLiveAPI(cmd) {
		client, err := workAPIClient(cmd)
		if err != nil {
			return err
		}
		if strings.TrimSpace(req.Actor.WorkspaceID) == "" {
			req.Actor.WorkspaceID = client.WorkspaceID
		}
		var res workentry.ResolveResult
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.PostJSON(ctx, "/api/work/resolve", req, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	svc := workentry.NewService(store)
	if req.Actor.WorkspaceID == "" {
		req.Actor.WorkspaceID = workWorkspace(cmd)
	}

	res, err := svc.ResolvePreview(context.Background(), req)
	if err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkRegister(cmd *cobra.Command, _ []string) error {
	var req workentry.RegisterRequest
	if err := readRequestJSON(cmd, &req); err != nil {
		return err
	}
	if req.Actor.WorkspaceID == "" {
		req.Actor.WorkspaceID = workWorkspace(cmd)
	}
	if confirm, _ := cmd.Flags().GetBool("confirm-create"); confirm {
		req.ConfirmCreate = true
	}
	if workUsesLiveAPI(cmd) {
		client, err := workAPIClient(cmd)
		if err != nil {
			return err
		}
		if strings.TrimSpace(req.Actor.WorkspaceID) == "" {
			req.Actor.WorkspaceID = client.WorkspaceID
		}
		var receipt workentry.WorkRegistrationReceiptV1
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.PostJSON(ctx, "/api/work/register", req, &receipt); err != nil {
			return err
		}
		return workPrintJSON(receipt)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	svc := workentry.NewService(store)

	receipt, err := svc.Register(context.Background(), req)
	if err != nil {
		return err
	}
	if err := workSaveStore(cmd, store); err != nil {
		return err
	}
	return workPrintJSON(receipt)
}

func runWorkStart(cmd *cobra.Command, args []string) error {
	sessionID, _ := cmd.Flags().GetString("session-id")
	runID, _ := cmd.Flags().GetString("run-id")
	actorID, _ := cmd.Flags().GetString("actor-id")
	req := workentry.StartRequest{
		WorkRef:     args[0],
		SessionID:   sessionID,
		RunID:       runID,
		ActorID:     actorID,
		WorkspaceID: workWorkspace(cmd),
	}
	if workUsesLiveAPI(cmd) {
		if strings.TrimSpace(runID) != "" {
			return fmt.Errorf("--run-id cannot be supplied in live API mode; HiveCrew owns run lineage")
		}
		client, err := workAPIClient(cmd)
		if err != nil {
			return err
		}
		req.WorkspaceID = client.WorkspaceID
		// run_id is a server-owned proof field. Do not serialize even an empty
		// value because live admission rejects caller-supplied proof keys.
		body := struct {
			WorkRef     string `json:"work_ref"`
			SessionID   string `json:"session_id"`
			ActorID     string `json:"actor_id"`
			WorkspaceID string `json:"workspace_id"`
		}{
			WorkRef: req.WorkRef, SessionID: req.SessionID,
			ActorID: req.ActorID, WorkspaceID: req.WorkspaceID,
		}
		var res workentry.EventResult
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.PostJSON(ctx, "/api/work/start", body, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	svc := workentry.NewService(store)
	res, err := svc.Start(context.Background(), req)
	if err != nil {
		return err
	}
	if err := workSaveStore(cmd, store); err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkStatus(cmd *cobra.Command, args []string) error {
	var res workentry.StatusResult
	if workUsesLiveAPI(cmd) {
		if err := workGetJSON(cmd, "/api/work/status?work_ref="+url.QueryEscape(args[0]), &res); err != nil {
			return err
		}
	} else {
		store, err := workLoadStore(cmd)
		if err != nil {
			return err
		}
		ws := workWorkspace(cmd)
		if ws == "" {
			return fmt.Errorf("--workspace-id (or MULTICA_WORKSPACE_ID) is required")
		}
		res, err = workentry.NewService(store).Status(context.Background(), workentry.StatusRequest{WorkRef: args[0], WorkspaceID: ws})
		if err != nil {
			return err
		}
	}
	if workOutput(cmd) == "table" {
		headers := []string{"WORK_REF", "FOUND", "DECISION", "PROJECT_ID", "ISSUE_ID", "TASK_ID"}
		rows := [][]string{{
			res.WorkRef, fmt.Sprintf("%v", res.Found), string(res.Decision),
			res.ProjectID, res.IssueID, res.TaskID,
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}
	return workPrintJSON(res)
}

func runWorkHeartbeat(cmd *cobra.Command, _ []string) error {
	ws := workWorkspace(cmd)
	if ws == "" && !workUsesLiveAPI(cmd) {
		return fmt.Errorf("--workspace-id (or MULTICA_WORKSPACE_ID) is required")
	}
	actorID, _ := cmd.Flags().GetString("actor-id")
	if actorID == "" {
		return fmt.Errorf("--actor-id is required")
	}
	sessionID, _ := cmd.Flags().GetString("session-id")
	if sessionID == "" {
		return fmt.Errorf("--session-id is required")
	}
	window, _ := cmd.Flags().GetInt("window-index")
	pane, _ := cmd.Flags().GetInt("pane-index")
	host, _ := cmd.Flags().GetString("host")
	sessionName, _ := cmd.Flags().GetString("session-name")
	currentCommand, _ := cmd.Flags().GetString("current-command")
	agentHint, _ := cmd.Flags().GetString("agent-hint")

	record := workentry.HeartbeatRecord{
		WorkspaceID:    ws,
		ActorID:        actorID,
		SessionID:      sessionID,
		Host:           host,
		SessionName:    sessionName,
		WindowIndex:    window,
		PaneIndex:      pane,
		CurrentCommand: currentCommand,
		AgentHint:      agentHint,
	}
	if workUsesLiveAPI(cmd) {
		client, err := workAPIClient(cmd)
		if err != nil {
			return err
		}
		record.WorkspaceID = client.WorkspaceID
		var res workentry.HeartbeatResult
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.PostJSON(ctx, "/api/work/heartbeat", record, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	res, err := workentry.NewService(store).Heartbeat(context.Background(), record)
	if err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkEvent(cmd *cobra.Command, _ []string) error {
	var event workentry.WorkEventV1
	if err := readRequestJSON(cmd, &event); err != nil {
		return err
	}
	if workUsesLiveAPI(cmd) {
		var res workentry.EventResult
		if err := workPostJSON(cmd, "/api/work/event", event, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	svc := workentry.NewService(store)
	res, err := svc.Event(context.Background(), event)
	if err != nil {
		return err
	}
	if err := workSaveStore(cmd, store); err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkHandoff(cmd *cobra.Command, _ []string) error {
	var pkg workentry.WorkHandoffV1
	if err := readRequestJSON(cmd, &pkg); err != nil {
		return err
	}
	if workUsesLiveAPI(cmd) {
		var res workentry.HandoffResult
		if err := workPostJSON(cmd, "/api/work/handoff", pkg, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	svc := workentry.NewService(store)
	res, err := svc.Handoff(context.Background(), pkg)
	if err != nil {
		return err
	}
	if err := workSaveStore(cmd, store); err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkFinish(cmd *cobra.Command, _ []string) error {
	var completion workentry.WorkCompletionV1
	if err := readRequestJSON(cmd, &completion); err != nil {
		return err
	}
	if workUsesLiveAPI(cmd) {
		var res workentry.CompletionResult
		if err := workPostJSON(cmd, "/api/work/finish", completion, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	svc := workentry.NewService(store)
	res, err := svc.Finish(context.Background(), completion)
	if err != nil {
		return err
	}
	if err := workSaveStore(cmd, store); err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkSync(cmd *cobra.Command, _ []string) error {
	var entries []workentry.SyncEntry
	if err := readRequestJSON(cmd, &entries); err != nil {
		return err
	}
	if workUsesLiveAPI(cmd) {
		var res workentry.SyncResult
		if err := workPostJSON(cmd, "/api/work/sync", map[string]any{"entries": entries}, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	svc := workentry.NewService(store)
	res, err := svc.Sync(context.Background(), entries)
	if err != nil {
		return err
	}
	if err := workSaveStore(cmd, store); err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkDoctor(cmd *cobra.Command, _ []string) error {
	var items []workentry.InboxItem
	if workUsesLiveAPI(cmd) {
		if err := workGetJSON(cmd, "/api/work/reconcile", &items); err != nil {
			return err
		}
	} else {
		store, err := workLoadStore(cmd)
		if err != nil {
			return err
		}
		ws := workWorkspace(cmd)
		if ws == "" {
			return fmt.Errorf("--workspace-id (or MULTICA_WORKSPACE_ID) is required")
		}
		items, err = workentry.NewService(store).Reconcile(context.Background(), ws)
		if err != nil {
			return err
		}
	}
	if items == nil {
		items = []workentry.InboxItem{}
	}
	if workOutput(cmd) == "table" {
		headers := []string{"INBOX_ID", "WORK_REF"}
		rows := make([][]string, 0, len(items))
		for _, it := range items {
			rows = append(rows, []string{it.ID, it.WorkRef})
		}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}
	return workPrintJSON(items)
}

func runWorkDoctorAttach(cmd *cobra.Command, _ []string) error {
	ws := workWorkspace(cmd)
	inboxID, _ := cmd.Flags().GetString("inbox-id")
	projectID, _ := cmd.Flags().GetString("project-id")
	issueID, _ := cmd.Flags().GetString("issue-id")
	req := workentry.AttachRequest{
		WorkspaceID: ws, InboxID: inboxID, ProjectID: projectID, IssueID: issueID,
	}
	if workUsesLiveAPI(cmd) {
		client, err := workAPIClient(cmd)
		if err != nil {
			return err
		}
		req.WorkspaceID = client.WorkspaceID
		var res workentry.AttachResult
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.PostJSON(ctx, "/api/work/attach", req, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	res, err := workentry.NewService(store).Attach(context.Background(), req)
	if err != nil {
		return err
	}
	if err := workSaveStore(cmd, store); err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkDoctorIgnore(cmd *cobra.Command, _ []string) error {
	ws := workWorkspace(cmd)
	inboxID, _ := cmd.Flags().GetString("inbox-id")
	reason, _ := cmd.Flags().GetString("reason")
	req := workentry.IgnoreRequest{
		WorkspaceID: ws, InboxID: inboxID, Reason: reason,
	}
	if workUsesLiveAPI(cmd) {
		client, err := workAPIClient(cmd)
		if err != nil {
			return err
		}
		req.WorkspaceID = client.WorkspaceID
		var res workentry.IgnoreResult
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.PostJSON(ctx, "/api/work/ignore", req, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	res, err := workentry.NewService(store).Ignore(context.Background(), req)
	if err != nil {
		return err
	}
	if err := workSaveStore(cmd, store); err != nil {
		return err
	}
	return workPrintJSON(res)
}

func runWorkReplay(cmd *cobra.Command, _ []string) error {
	ws := workWorkspace(cmd)
	key, _ := cmd.Flags().GetString("idempotency-key")
	if key == "" {
		return fmt.Errorf("--idempotency-key is required")
	}
	kind, _ := cmd.Flags().GetString("kind")
	workRef, _ := cmd.Flags().GetString("work-ref")

	if workUsesLiveAPI(cmd) {
		path := "/api/work/replay?key=" + url.QueryEscape(key) + "&kind=" + url.QueryEscape(kind) + "&work_ref=" + url.QueryEscape(workRef)
		var res workentry.ReplayResult
		if err := workGetJSON(cmd, path, &res); err != nil {
			return err
		}
		return workPrintJSON(res)
	}

	store, err := workLoadStore(cmd)
	if err != nil {
		return err
	}
	res, err := workentry.NewService(store).Replay(context.Background(), workentry.ReplayRequest{
		WorkspaceID: ws, IdempotencyKey: key, Kind: kind, WorkRef: workRef,
	})
	if err != nil {
		return err
	}
	return workPrintJSON(res)
}
