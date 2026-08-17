package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/mutationbroker"
	"github.com/spf13/cobra"
)

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Work with repositories",
}

var repoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace repositories",
	Long:  "Lists the repository registry for the current workspace. These are workspace-level repos, separate from project resources.",
	Args:  cobra.NoArgs,
	RunE:  runRepoList,
}

var repoAddCmd = &cobra.Command{
	Use:   "add [url]...",
	Short: "Add repositories to the workspace registry",
	Long: "Adds one or more repository URLs to the current workspace repository registry. " +
		"Existing URLs are not duplicated. Use project resources when you need project-specific context instead.",
	Args: cobra.ArbitraryArgs,
	RunE: runRepoAdd,
}

var repoRemoveCmd = &cobra.Command{
	Use:     "remove [url]...",
	Aliases: []string{"rm"},
	Short:   "Remove repositories from the workspace registry",
	Long:    "Removes one or more repository URLs from the current workspace repository registry.",
	Args:    cobra.ArbitraryArgs,
	RunE:    runRepoRemove,
}

var repoCheckoutCmd = &cobra.Command{
	Use:   "checkout <url>",
	Short: "Check out a repository into the working directory",
	Long:  "Creates a git worktree from the daemon's bare clone cache. Used by agents to check out repos on demand.",
	Args:  exactArgs(1),
	RunE:  runRepoCheckout,
}

var repoCheckoutRef string

func init() {
	repoListCmd.Flags().String("output", "table", "Output format: table or json")

	repoAddCmd.Flags().StringArray("url", nil, "Repository URL to add (may be repeated)")
	repoAddCmd.Flags().String("description", "", "Optional description; only valid when adding one URL")
	repoAddCmd.Flags().String("output", "json", "Output format: table or json")

	repoRemoveCmd.Flags().StringArray("url", nil, "Repository URL to remove (may be repeated)")
	repoRemoveCmd.Flags().String("output", "json", "Output format: table or json")

	repoCheckoutCmd.Flags().StringVar(&repoCheckoutRef, "ref", "", "branch, tag, or commit to check out instead of the remote default branch")

	repoCmd.AddCommand(repoListCmd)
	repoCmd.AddCommand(repoAddCmd)
	repoCmd.AddCommand(repoRemoveCmd)
	repoCmd.AddCommand(repoCheckoutCmd)
}

type workspaceRepo struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

type repoWorkspaceResponse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Slug  string          `json:"slug"`
	Repos []workspaceRepo `json:"repos"`
}

type repoMutationResult struct {
	WorkspaceID string          `json:"workspace_id"`
	Added       []workspaceRepo `json:"added,omitempty"`
	Updated     []workspaceRepo `json:"updated,omitempty"`
	Removed     []workspaceRepo `json:"removed,omitempty"`
	Repos       []workspaceRepo `json:"repos"`
}

func repoURLsFromArgsAndFlags(cmd *cobra.Command, args []string) ([]string, error) {
	flagURLs, _ := cmd.Flags().GetStringArray("url")
	raw := append([]string{}, flagURLs...)
	raw = append(raw, args...)
	if len(raw) == 0 {
		return nil, fmt.Errorf("at least one repository URL is required")
	}

	urls := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, u := range raw {
		u = strings.TrimSpace(u)
		if u == "" {
			return nil, fmt.Errorf("repository URL cannot be empty")
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	return urls, nil
}

func fetchRepoWorkspace(ctx context.Context, client *cli.APIClient, workspaceID string) (repoWorkspaceResponse, error) {
	var ws repoWorkspaceResponse
	if err := client.GetJSON(ctx, "/api/workspaces/"+workspaceID, &ws); err != nil {
		return repoWorkspaceResponse{}, fmt.Errorf("get workspace: %w", err)
	}
	if ws.Repos == nil {
		ws.Repos = []workspaceRepo{}
	}
	return ws, nil
}

func patchWorkspaceRepos(ctx context.Context, client *cli.APIClient, workspaceID string, repos []workspaceRepo) (repoWorkspaceResponse, error) {
	var ws repoWorkspaceResponse
	if err := client.PatchJSON(ctx, "/api/workspaces/"+workspaceID, map[string]any{"repos": repos}, &ws); err != nil {
		return repoWorkspaceResponse{}, fmt.Errorf("update workspace repos: %w", err)
	}
	if ws.Repos == nil {
		ws.Repos = []workspaceRepo{}
	}
	return ws, nil
}

func repoCommandClient(cmd *cobra.Command) (*cli.APIClient, string, error) {
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return nil, "", err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	return client, workspaceID, nil
}

func runRepoList(cmd *cobra.Command, _ []string) error {
	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	ws, err := fetchRepoWorkspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, ws.Repos)
	}
	if len(ws.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "No repositories found.")
		return nil
	}
	rows := make([][]string, 0, len(ws.Repos))
	for _, repo := range ws.Repos {
		rows = append(rows, []string{repo.URL, repo.Description})
	}
	cli.PrintTable(os.Stdout, []string{"URL", "DESCRIPTION"}, rows)
	return nil
}

func runRepoAdd(cmd *cobra.Command, args []string) error {
	urls, err := repoURLsFromArgsAndFlags(cmd, args)
	if err != nil {
		return err
	}
	description, _ := cmd.Flags().GetString("description")
	descriptionChanged := cmd.Flags().Changed("description")
	if descriptionChanged && len(urls) > 1 {
		return fmt.Errorf("--description can only be used when adding one repository URL")
	}

	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	ws, err := fetchRepoWorkspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}

	indexByURL := make(map[string]int, len(ws.Repos))
	for i, repo := range ws.Repos {
		indexByURL[repo.URL] = i
	}

	added := []workspaceRepo{}
	updated := []workspaceRepo{}
	repos := append([]workspaceRepo{}, ws.Repos...)
	for _, u := range urls {
		if idx, ok := indexByURL[u]; ok {
			if descriptionChanged && repos[idx].Description != description {
				repos[idx].Description = description
				updated = append(updated, repos[idx])
			}
			continue
		}
		repo := workspaceRepo{URL: u}
		if descriptionChanged {
			repo.Description = description
		}
		indexByURL[u] = len(repos)
		repos = append(repos, repo)
		added = append(added, repo)
	}

	if len(added) > 0 || len(updated) > 0 {
		ws, err = patchWorkspaceRepos(ctx, client, workspaceID, repos)
		if err != nil {
			return err
		}
	} else {
		ws.Repos = repos
	}

	result := repoMutationResult{
		WorkspaceID: ws.ID,
		Added:       added,
		Updated:     updated,
		Repos:       ws.Repos,
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	if len(added) == 0 && len(updated) == 0 {
		fmt.Fprintln(os.Stdout, "No repository changes.")
		return nil
	}
	rows := make([][]string, 0, len(added)+len(updated))
	for _, repo := range added {
		rows = append(rows, []string{"added", repo.URL, repo.Description})
	}
	for _, repo := range updated {
		rows = append(rows, []string{"updated", repo.URL, repo.Description})
	}
	cli.PrintTable(os.Stdout, []string{"ACTION", "URL", "DESCRIPTION"}, rows)
	return nil
}

func runRepoRemove(cmd *cobra.Command, args []string) error {
	urls, err := repoURLsFromArgsAndFlags(cmd, args)
	if err != nil {
		return err
	}

	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	ws, err := fetchRepoWorkspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}

	removeSet := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		removeSet[u] = struct{}{}
	}
	removedSet := make(map[string]struct{}, len(urls))
	removed := []workspaceRepo{}
	repos := make([]workspaceRepo, 0, len(ws.Repos))
	for _, repo := range ws.Repos {
		if _, ok := removeSet[repo.URL]; ok {
			removed = append(removed, repo)
			removedSet[repo.URL] = struct{}{}
			continue
		}
		repos = append(repos, repo)
	}
	missing := []string{}
	for _, u := range urls {
		if _, ok := removedSet[u]; !ok {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("repository not found in workspace registry: %s", strings.Join(missing, ", "))
	}

	ws, err = patchWorkspaceRepos(ctx, client, workspaceID, repos)
	if err != nil {
		return err
	}

	result := repoMutationResult{
		WorkspaceID: ws.ID,
		Removed:     removed,
		Repos:       ws.Repos,
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	rows := make([][]string, 0, len(removed))
	for _, repo := range removed {
		rows = append(rows, []string{repo.URL, repo.Description})
	}
	cli.PrintTable(os.Stdout, []string{"REMOVED URL", "DESCRIPTION"}, rows)
	return nil
}

func runRepoCheckout(cmd *cobra.Command, args []string) error {
	repoURL := args[0]
	transport := strings.TrimSpace(os.Getenv("MULTICA_REPO_CHECKOUT_TRANSPORT"))
	selected, err := selectRepoCheckoutTransport(transport, strings.TrimSpace(os.Getenv("MULTICA_MEDIATED_VCS_POLICY")))
	if err != nil {
		return err
	}
	if selected == "unix-seqpacket-v1" {
		if runtime.GOOS != "linux" {
			return fmt.Errorf("unix-seqpacket-v1 checkout transport is Linux-only")
		}
		return runUnixRepoCheckout(repoURL)
	}

	daemonPort := os.Getenv("MULTICA_DAEMON_PORT")
	if daemonPort == "" {
		return fmt.Errorf("MULTICA_DAEMON_PORT not set (this command is intended to be run by an agent inside a daemon task)")
	}

	workspaceID := os.Getenv("MULTICA_WORKSPACE_ID")
	agentName := os.Getenv("MULTICA_AGENT_NAME")
	taskID := os.Getenv("MULTICA_TASK_ID")
	runtimeID := os.Getenv("MULTICA_RUNTIME_ID")
	capabilityFile := os.Getenv("MULTICA_MUTATION_CAPABILITY_FILE")
	if capabilityFile == "" {
		return fmt.Errorf("MULTICA_MUTATION_CAPABILITY_FILE not set")
	}
	capabilityBytes, err := os.ReadFile(capabilityFile)
	if err != nil {
		return fmt.Errorf("read mutation capability: %w", err)
	}
	capability := strings.TrimSpace(string(capabilityBytes))
	if capability == "" {
		return fmt.Errorf("mutation capability file is empty")
	}

	// Use current working directory as the checkout target.
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	reqBody := map[string]string{
		"url":           repoURL,
		"workspace_id":  workspaceID,
		"workdir":       workDir,
		"ref":           repoCheckoutRef,
		"agent_name":    agentName,
		"task_id":       taskID,
		"runtime_id":    runtimeID,
		"checkout_mode": strings.TrimSpace(os.Getenv("MULTICA_REPO_CHECKOUT_MODE")),
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%s/repo/checkout", daemonPort), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build checkout request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Multica-Mutation-Capability", capability)
	req.Header.Set("X-Multica-Mutation-Request-ID", uuid.NewString())
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checkout failed: %s", string(body))
	}

	var result struct {
		Path       string `json:"path"`
		BranchName string `json:"branch_name"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Fprintf(os.Stdout, "%s\n", result.Path)
	fmt.Fprintf(os.Stderr, "Checked out %s → %s (branch: %s)\n", repoURL, result.Path, result.BranchName)

	return nil
}

func selectRepoCheckoutTransport(transport, policy string) (string, error) {
	switch transport {
	case "":
		if policy == "1" {
			return "", fmt.Errorf("mediated checkout transport missing or unsupported")
		}
		return "legacy-http", nil
	case "unix-seqpacket-v1":
		return transport, nil
	default:
		return "", fmt.Errorf("unsupported mediated checkout transport %q", transport)
	}
}

func runUnixRepoCheckout(repoURL string) error {
	locator := strings.TrimSpace(os.Getenv("MULTICA_REPO_CHECKOUT_LOCATOR"))
	if locator == "" {
		return fmt.Errorf("MULTICA_REPO_CHECKOUT_LOCATOR not set")
	}
	workspaceID := os.Getenv("MULTICA_WORKSPACE_ID")
	agentName := os.Getenv("MULTICA_AGENT_NAME")
	taskID := os.Getenv("MULTICA_TASK_ID")
	runtimeID := os.Getenv("MULTICA_RUNTIME_ID")
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	requestID := uuid.NewString()
	payload, err := json.Marshal(mutationbroker.CheckoutRequest{
		WorkspaceID:  workspaceID,
		AgentName:    agentName,
		TaskID:       taskID,
		RuntimeID:    runtimeID,
		WorkDir:      workDir,
		URL:          repoURL,
		Ref:          strings.TrimSpace(repoCheckoutRef),
		CheckoutMode: strings.TrimSpace(os.Getenv("MULTICA_REPO_CHECKOUT_MODE")),
	})
	if err != nil {
		return fmt.Errorf("encode mediated checkout request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, err := mutationbroker.Dial(ctx, locator)
	if err != nil {
		return fmt.Errorf("connect mediated checkout transport: %w", err)
	}
	defer conn.Close()
	uconn, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("mediated checkout transport is not Unix seqpacket")
	}
	cred, err := repoCheckoutCredentials()
	if err != nil {
		return err
	}
	write := func(req mutationbroker.Request) error {
		if deadline, ok := ctx.Deadline(); ok {
			if err := uconn.SetWriteDeadline(deadline); err != nil {
				return err
			}
		} else if err := uconn.SetWriteDeadline(time.Time{}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if len(data) > mutationbroker.MaxFrameBytes {
			return mutationbroker.ErrFrameTooLarge
		}
		n, oobn, err := uconn.WriteMsgUnix(data, cred, nil)
		if err != nil {
			return err
		}
		if n != len(data) || oobn != len(cred) {
			return io.ErrShortWrite
		}
		return nil
	}
	read := func() (mutationbroker.Response, error) {
		if deadline, ok := ctx.Deadline(); ok {
			if err := uconn.SetReadDeadline(deadline); err != nil {
				return mutationbroker.Response{}, err
			}
		} else if err := uconn.SetReadDeadline(time.Time{}); err != nil {
			return mutationbroker.Response{}, err
		}
		if err := ctx.Err(); err != nil {
			return mutationbroker.Response{}, err
		}
		data := make([]byte, mutationbroker.MaxFrameBytes+1)
		n, _, flags, _, err := uconn.ReadMsgUnix(data, nil)
		if err != nil {
			return mutationbroker.Response{}, err
		}
		if flags != 0 || n > mutationbroker.MaxFrameBytes {
			return mutationbroker.Response{}, mutationbroker.ErrProtocol
		}
		var response mutationbroker.Response
		decoder := json.NewDecoder(bytes.NewReader(data[:n]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&response); err != nil {
			return mutationbroker.Response{}, err
		}
		var trailing any
		if err := decoder.Decode(&trailing); err == nil || err != io.EOF {
			return mutationbroker.Response{}, mutationbroker.ErrProtocol
		}
		if response.Version != mutationbroker.ProtocolVersion {
			return mutationbroker.Response{}, mutationbroker.ErrProtocol
		}
		return response, nil
	}
	if err := write(mutationbroker.Request{Version: mutationbroker.ProtocolVersion, Kind: mutationbroker.HelloKind, RequestID: requestID + "-hello"}); err != nil {
		return fmt.Errorf("send mediated checkout hello: %w", err)
	}
	if response, err := read(); err != nil || !response.OK || response.RequestID != requestID+"-hello" {
		if err != nil {
			return fmt.Errorf("mediated checkout hello failed: %w", err)
		}
		return fmt.Errorf("mediated checkout hello rejected")
	}
	if err := write(mutationbroker.Request{Version: mutationbroker.ProtocolVersion, Kind: mutationbroker.RequestKind, RequestID: requestID, Operation: mutationbroker.OperationRepoCheckout, Payload: payload}); err != nil {
		return fmt.Errorf("send mediated checkout request: %w", err)
	}
	response, err := read()
	if err != nil {
		return fmt.Errorf("read mediated checkout response: %w", err)
	}
	if response.RequestID != requestID || !response.OK {
		return fmt.Errorf("checkout failed: mediated operation rejected")
	}
	var result struct {
		Path       string `json:"path"`
		BranchName string `json:"branch_name"`
	}
	if err := json.Unmarshal(response.Payload, &result); err != nil {
		return fmt.Errorf("parse mediated checkout response: %w", err)
	}
	if strings.TrimSpace(result.Path) == "" {
		return fmt.Errorf("mediated checkout response has no path")
	}
	fmt.Fprintf(os.Stdout, "%s\n", result.Path)
	fmt.Fprintf(os.Stderr, "Checked out %s → %s (branch: %s)\n", repoURL, result.Path, result.BranchName)
	return nil
}
