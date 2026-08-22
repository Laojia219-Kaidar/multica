package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

type baseOverview struct {
	MachineTitle      string `json:"machine_title"`
	RuntimeOnline     int    `json:"runtime_online"`
	RuntimeRegistered int    `json:"runtime_registered"`
	Employees         int    `json:"employees"`
	Drained           bool   `json:"drained"`
}

type baseOperationalModeResult struct {
	MachineTitle  string `json:"machine_title"`
	Mode          string `json:"mode"`
	AgentsUpdated int    `json:"agents_updated"`
}

var baseCmd = &cobra.Command{
	Use:   "base",
	Short: "Work with observed execution bases",
}

var baseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List observed execution bases in the workspace",
	RunE:  runBaseList,
}

var baseOperationalModeCmd = &cobra.Command{
	Use:   "operational-mode",
	Short: "Drain or resume task claims on an execution base",
	RunE:  runBaseOperationalMode,
}

func init() {
	baseCmd.AddCommand(baseListCmd)
	baseCmd.AddCommand(baseOperationalModeCmd)

	baseListCmd.Flags().String("output", "table", "Output format: table or json")

	baseOperationalModeCmd.Flags().String("machine-title", "", "Exact machine title returned by 'multica base list' (required)")
	baseOperationalModeCmd.Flags().String("mode", "", "Operational mode: active or resting (required)")
	baseOperationalModeCmd.Flags().String("output", "json", "Output format: table or json")
}

func runBaseList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var bases []baseOverview
	if err := client.GetJSON(ctx, "/api/bases", &bases); err != nil {
		return fmt.Errorf("list bases: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, bases)
	}
	if output != "table" {
		return fmt.Errorf("--output must be table or json")
	}

	headers := []string{"MACHINE", "ONLINE", "REGISTERED", "EMPLOYEES", "DRAINED"}
	rows := make([][]string, 0, len(bases))
	for _, base := range bases {
		rows = append(rows, []string{
			base.MachineTitle,
			strconv.Itoa(base.RuntimeOnline),
			strconv.Itoa(base.RuntimeRegistered),
			strconv.Itoa(base.Employees),
			strconv.FormatBool(base.Drained),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runBaseOperationalMode(cmd *cobra.Command, _ []string) error {
	machineTitle, _ := cmd.Flags().GetString("machine-title")
	mode, _ := cmd.Flags().GetString("mode")
	machineTitle = strings.TrimSpace(machineTitle)
	mode = strings.TrimSpace(mode)

	if machineTitle == "" {
		return fmt.Errorf("--machine-title is required")
	}
	if mode != "active" && mode != "resting" {
		return fmt.Errorf("--mode must be active or resting")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var bases []baseOverview
	if err := client.GetJSON(ctx, "/api/bases", &bases); err != nil {
		return fmt.Errorf("validate base: %w", err)
	}
	found := false
	for _, base := range bases {
		if base.MachineTitle == machineTitle {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("execution base %q not found; use 'multica base list' for exact machine titles", machineTitle)
	}

	body := map[string]any{
		"machine_title": machineTitle,
		"mode":          mode,
	}
	var result baseOperationalModeResult
	if err := client.PostJSON(ctx, "/api/bases/operational-mode", body, &result); err != nil {
		return fmt.Errorf("set base operational mode: %w", err)
	}
	if result.AgentsUpdated == 0 {
		return fmt.Errorf("base %q matched but no agents were updated", machineTitle)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	if output != "table" {
		return fmt.Errorf("--output must be table or json")
	}
	cli.PrintTable(os.Stdout,
		[]string{"MACHINE", "MODE", "AGENTS_UPDATED"},
		[][]string{{
			result.MachineTitle,
			result.Mode,
			strconv.Itoa(result.AgentsUpdated),
		}},
	)
	return nil
}
