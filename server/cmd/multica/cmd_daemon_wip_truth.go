package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var daemonWIPTruthCmd = &cobra.Command{
	Use:   "wip-truth",
	Short: "Read-only WIP truth probe: daemon active vs server pending task histogram",
	Long: `Queries the local daemon /health endpoint and the server pending-tasks
endpoint for each scoped runtime, then produces a fail-closed JSON V1 report
comparing daemon-active count against server-side dispatched+running tasks.

No mutations. Uses existing local CLI credentials. Missing, invalid, or
unscoped data produces UNKNOWN rather than a misleading zero.`,
	RunE: runDaemonWIPTruth,
}

func init() {
	daemonWIPTruthCmd.Flags().String("output", "json", "Output format: json (only json supported)")
}

func runDaemonWIPTruth(cmd *cobra.Command, _ []string) error {
	profile := resolveProfile(cmd)
	healthPort := healthPortForProfile(profile)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Read daemon /health.
	snap := readDaemonHealth(ctx, healthPort)

	// 2. Collect runtime IDs from health response.
	var runtimeIDs []string
	for _, ws := range snap.Workspaces {
		runtimeIDs = append(runtimeIDs, ws.Runtimes...)
	}

	// 3. Query server pending tasks per runtime (if we have credentials).
	var allTasks []cli.ServerPendingTask
	if len(runtimeIDs) > 0 {
		client, err := newAPIClient(cmd)
		if err == nil {
			for _, rtID := range runtimeIDs {
				var tasks []cli.ServerPendingTask
				path := fmt.Sprintf("/api/daemon/runtimes/%s/tasks/pending", rtID)
				if err := client.GetJSON(ctx, path, &tasks); err != nil {
					continue
				}
				allTasks = append(allTasks, tasks...)
			}
		}
	}

	// 4. Compute report.
	report := cli.ComputeWIPTruth(snap, allTasks, time.Now())

	// 5. Output.
	return cli.PrintJSON(cmd.OutOrStdout(), report)
}

// readDaemonHealth reads the daemon /health endpoint and returns a minimal
// snapshot. On any error it returns a snapshot with empty status so
// ComputeWIPTruth produces a fail-closed UNKNOWN report.
func readDaemonHealth(ctx context.Context, port int) cli.DaemonHealthSnapshot {
	addr := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return cli.DaemonHealthSnapshot{}
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return cli.DaemonHealthSnapshot{}
	}
	defer resp.Body.Close()

	var snap cli.DaemonHealthSnapshot
	if err := jsonDecode(resp.Body, &snap); err != nil {
		return cli.DaemonHealthSnapshot{}
	}
	return snap
}

func jsonDecode(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}
