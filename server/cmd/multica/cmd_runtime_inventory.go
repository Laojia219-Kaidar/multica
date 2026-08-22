package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// runtime inventory — the read-only, secret-safe Employee -> Agent ->
// Runtime -> RuntimeProfile -> Provider/Model -> online/registration_error
// chain for the workspace's digital employees (HIV-792).
//
// The command prints exactly what the server's allowlisted inventory
// endpoint returns. It never reads runtime_config, custom_env, tokens or
// local paths client-side, and it performs no create/repair calls.

var runtimeInventoryCmd = &cobra.Command{
	Use:   "inventory [employee]",
	Short: "Read-only Employee→Runtime registration inventory (secret-safe)",
	Long: "Print the secret-safe registration inventory for every digital employee in the workspace:\n\n" +
		"Employee → Agent → Runtime → RuntimeProfile → Provider/Model → online/registration_error.\n\n" +
		"Missing links are reported as missing_agent / missing_runtime / missing_profile and are never auto-repaired. " +
		"An offline runtime means the runtime daemon is not connected — it is not a statement about Provider availability.\n\n" +
		"Optionally pass an employee reference (agent UUID or exact agent name) to print a single chain. " +
		"A reference that matches no employee is an error (non-zero exit).",
	Args: cobra.MaximumNArgs(1),
	RunE: runRuntimeInventory,
}

func init() {
	runtimeCmd.AddCommand(runtimeInventoryCmd)
	runtimeInventoryCmd.Flags().String("output", "table", "Output format: table or json")
}

// runtimeInventoryOut is the print destination. os.Stdout in production;
// swapped by tests.
var runtimeInventoryOut io.Writer = os.Stdout

func runRuntimeInventory(cmd *cobra.Command, args []string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "table" && output != "json" {
		return fmt.Errorf("--output must be table or json")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	employee := ""
	if len(args) == 1 {
		employee = strings.TrimSpace(args[0])
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	path := "/api/runtimes/inventory"
	if employee != "" {
		path += "?employee=" + url.QueryEscape(employee)
	}

	var inventory struct {
		Count     int              `json:"count"`
		Employees []map[string]any `json:"employees"`
	}
	if err := client.GetJSON(ctx, path, &inventory); err != nil {
		if employee != "" {
			return fmt.Errorf("runtime inventory: employee %q not found or unavailable: %w", employee, err)
		}
		return fmt.Errorf("runtime inventory: %w", err)
	}

	if output == "json" {
		return cli.PrintJSON(runtimeInventoryOut, inventory.Employees)
	}
	printRuntimeInventoryTable(runtimeInventoryOut, inventory.Employees)
	return nil
}

func printRuntimeInventoryTable(w io.Writer, employees []map[string]any) {
	headers := []string{"EMPLOYEE", "AGENT", "RUNTIME", "PROFILE", "PROVIDER", "MODEL", "REGISTRATION"}
	rows := make([][]string, 0, len(employees))
	for _, employee := range employees {
		rows = append(rows, []string{
			runtimeInventoryEmployeeCell(employee),
			runtimeInventoryTableState(employee, "agent"),
			runtimeInventoryTableCell(employee, "runtime", "name", "state"),
			runtimeInventoryTableCell(employee, "profile", "display_name", "state"),
			strVal(employee, "provider"),
			strVal(employee, "model"),
			runtimeInventoryRegistrationCell(employee),
		})
	}
	cli.PrintTable(w, headers, rows)
}

// runtimeInventoryEmployeeCell renders the EMPLOYEE column: display name,
// falling back to the employee id.
func runtimeInventoryEmployeeCell(entry map[string]any) string {
	employee, _ := entry["employee"].(map[string]any)
	if employee == nil {
		return "-"
	}
	if name, _ := employee["name"].(string); name != "" {
		return name
	}
	if id, _ := employee["employee_id"].(string); id != "" {
		return id
	}
	return "-"
}

// runtimeInventoryTableCell renders a chain-link column: the display field
// when the link is healthy (state == "ok"), otherwise the state itself
// (missing_*, builtin, unknown, ...).
func runtimeInventoryTableCell(entry map[string]any, link, displayField, stateField string) string {
	linkObj, _ := entry[link].(map[string]any)
	if linkObj == nil {
		return "-"
	}
	if state, _ := linkObj[stateField].(string); state != "" && state != "ok" {
		return state
	}
	if value, _ := linkObj[displayField].(string); value != "" {
		return value
	}
	if id, _ := linkObj["id"].(string); id != "" {
		return id
	}
	return strVal(linkObj, "state")
}

func runtimeInventoryTableState(entry map[string]any, link string) string {
	linkObj, _ := entry[link].(map[string]any)
	if linkObj == nil {
		return "-"
	}
	return strVal(linkObj, "state")
}

// runtimeInventoryRegistrationCell renders the registration column. A
// A registration_error row appends the fixed server-owned reason code. The
// server never returns daemon free text because it may contain secrets or
// local paths.
func runtimeInventoryRegistrationCell(entry map[string]any) string {
	registration, _ := entry["registration"].(map[string]any)
	if registration == nil {
		return "-"
	}
	state := strVal(registration, "state")
	if state != "registration_error" {
		return state
	}
	reason := strVal(registration, "reason")
	if reason == "" {
		return state
	}
	if runes := []rune(reason); len(runes) > 60 {
		reason = string(runes[:60]) + "…"
	}
	return state + ": " + reason
}
