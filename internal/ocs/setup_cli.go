package ocs

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

// RunSetupCommand keeps setup independent of session scanning and the TUI.
// JSON plans expose metadata/status only, never token or environment values.
func RunSetupCommand(command string, argv []string, out io.Writer) (int, error) {
	flags := flag.NewFlagSet("agb "+command, flag.ContinueOnError)
	flags.SetOutput(out)
	profile := flags.String("profile", "", "setup profile (optional when only one exists)")
	target := flags.String("target", "", "apply only one target in the profile")
	configPath := flags.String("config", ConfigPath(), "AgentBridge configuration file")
	asJSON := flags.Bool("json", false, "machine-readable plan")
	check := flags.Bool("check", false, "report drift without writing (exit 1 if changes exist)")
	dryRun := flags.Bool("dry-run", false, "show the plan without writing")
	example := flags.Bool("example", false, "print a starter setup configuration")
	if err := flags.Parse(argv); err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, err
	}
	if flags.NArg() > 0 {
		return 2, fmt.Errorf("unexpected setup argument %q", flags.Arg(0))
	}
	if command == "setup" {
		if !*example {
			return 2, fmt.Errorf("use agb setup --example to print an editable starter configuration")
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		config, err := ParseConfig([]byte("{}"))
		if raw, readErr := os.ReadFile(*configPath); readErr == nil {
			config, err = ParseConfig(raw)
		} else if !os.IsNotExist(readErr) {
			return 2, readErr
		}
		if err != nil {
			return 2, err
		}
		var targets []string
		for _, account := range config.Targets() {
			targets = append(targets, account.Name)
		}
		return 0, encoder.Encode(map[string]any{"setup": SetupConfig{Version: 1, Profiles: map[string]SetupProfile{"development": {Targets: targets, MCPs: map[string]SetupMCP{"cloudflare-docs": {Transport: "http", URL: "https://docs.mcp.cloudflare.com/mcp"}}}}}})
	}
	if *example {
		return 2, fmt.Errorf("--example is only valid with agb setup")
	}
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		return 2, fmt.Errorf("cannot read setup configuration: %w", err)
	}
	config, err := ParseConfig(raw)
	if err != nil {
		return 2, err
	}
	plan, err := BuildSetupPlan(config, *configPath, *profile, *target)
	if err != nil {
		return 2, err
	}
	if *asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(plan); err != nil {
			return 2, err
		}
	} else {
		fmt.Fprintf(out, "Setup profile: %s\n", plan.Profile)
		for _, step := range plan.Steps {
			fmt.Fprintf(out, "%-8s %-8s %s %s\n  %s\n", step.Action, step.Target, step.Kind, step.Name, step.Path)
			if step.Message != "" {
				fmt.Fprintf(out, "  %s\n", step.Message)
			}
		}
		for _, warning := range plan.Warnings {
			fmt.Fprintf(out, "Note: %s\n", warning)
		}
	}
	if plan.HasConflicts() {
		return 2, fmt.Errorf("setup has conflicts; no changes applied")
	}
	if *check {
		if plan.HasChanges() {
			return 1, nil
		}
		return 0, nil
	}
	if command == "sync" && !*dryRun && !dryRunEnabled() {
		if err := ApplySetupPlan(plan); err != nil {
			return 2, err
		}
		if !*asJSON {
			fmt.Fprintln(out, "Setup synchronized.")
		}
	}
	return 0, nil
}
