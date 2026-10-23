package ocs

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const herdrHookMarker = " # agb-herdr-restore"

func herdrSettingsPath(account Account) string {
	name := "settings.json"
	if account.Tool == SourceCodex {
		name = "hooks.json"
	}
	return filepath.Join(herdrAccountHome(account), name)
}

// Remove only handlers bearing our exact marker, preserving unrelated hook
// groups and settings. Validate shapes instead of silently replacing them.
func editHerdrHooks(raw []byte, command string) ([]byte, error) {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, fmt.Errorf("hook settings must be a JSON object")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("hook settings contain trailing JSON")
	}
	before, _ := json.Marshal(root)
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		if _, exists := root["hooks"]; exists {
			return nil, fmt.Errorf("hooks must be an object")
		}
		if command == "" {
			return raw, nil
		}
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	for event, value := range hooks {
		groups, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("hooks.%s must be an array", event)
		}
		keptGroups := []any{}
		for _, value := range groups {
			group, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("hooks.%s contains a non-object group", event)
			}
			handlers, ok := group["hooks"].([]any)
			if !ok {
				return nil, fmt.Errorf("hooks.%s group needs a hooks array", event)
			}
			kept := []any{}
			removed := false
			for _, value := range handlers {
				handler, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("hooks.%s contains a non-object handler", event)
				}
				cmd, _ := handler["command"].(string)
				if strings.HasSuffix(cmd, herdrHookMarker) {
					removed = true
					continue
				}
				kept = append(kept, value)
			}
			if !removed || len(kept) > 0 {
				group["hooks"] = kept
				keptGroups = append(keptGroups, group)
			}
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	if command != "" {
		for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
			groups, _ := hooks[event].([]any)
			hooks[event] = append(groups, map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": command, "timeout": 5,
			}}})
		}
	}
	after, _ := json.Marshal(root)
	if bytes.Equal(before, after) {
		return raw, nil
	}
	data, err := json.MarshalIndent(root, "", "  ")
	return append(data, '\n'), err
}

func readHerdrSettings(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []byte("{}\n"), nil
	}
	return raw, err
}

func herdrHookCommand(binary string, account Account) string {
	command := shellQuote(binary) + " herdr hook --tool " + string(account.Tool)
	if account.Home != "" {
		command += " --home " + shellQuote(account.Home)
	}
	return command + herdrHookMarker
}

// Back up every file the native installer or our hook edit can touch. The
// private backup directory also keeps absent-file markers for manual recovery.
func backupHerdrSettings(account Account) (string, error) {
	root := herdrAccountHome(account)
	backup, err := os.MkdirTemp(root, ".agb-herdr-backup-*")
	if err != nil {
		return "", err
	}
	paths := []string{herdrSettingsPath(account), herdrNativeHook(account)}
	if account.Tool == SourceCodex {
		paths = append(paths, filepath.Join(root, "config.toml"))
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		name := filepath.Base(path)
		if os.IsNotExist(err) {
			name += ".absent"
			raw = nil
		} else if err != nil {
			return backup, err
		}
		if err := os.WriteFile(filepath.Join(backup, name), raw, 0o600); err != nil {
			return backup, err
		}
	}
	return backup, nil
}

func configureHerdrAccounts(accounts []Account, binary string, install, dryRun bool, out io.Writer) error {
	// Preflight all selected accounts before installing anything. An unknown
	// format in one account must not leave the earlier accounts half configured.
	for _, account := range accounts {
		if info, err := os.Stat(herdrAccountHome(account)); err != nil || !info.IsDir() {
			return fmt.Errorf("account %s: config directory missing: %s", account.Name, herdrAccountHome(account))
		}
		raw, err := readHerdrSettings(herdrSettingsPath(account))
		if err != nil {
			return err
		}
		if _, err := editHerdrHooks(raw, ""); err != nil {
			return fmt.Errorf("%s: %w", herdrSettingsPath(account), err)
		}
	}
	for _, account := range accounts {
		command := ""
		if install {
			command = herdrHookCommand(binary, account)
		}
		path := herdrSettingsPath(account)
		fmt.Fprintf(out, "%s: %s\n", account.Name, path)
		if dryRun {
			if install {
				fmt.Fprintf(out, "  install official %s integration and account-aware resume hooks\n", account.Tool)
			} else {
				fmt.Fprintln(out, "  remove agb resume hooks; keep official integration")
			}
			continue
		}
		backup, err := backupHerdrSettings(account)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  backup: %s\n", backup)
		if install {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			cmd := exec.CommandContext(ctx, herdrBinary(), "integration", "install", string(account.Tool))
			if account.Tool == SourceClaude {
				cmd.Env = childEnvironment(claudeEnvironment(&account))
			} else {
				cmd.Env = childEnvironment(codexEnvironment(&account))
			}
			output, runErr := cmd.CombinedOutput()
			cancel()
			if runErr != nil {
				return fmt.Errorf("%s: Herdr integration install failed: %w: %s (backup: %s)", account.Name, runErr, strings.TrimSpace(string(output)), backup)
			}
		}
		raw, err := readHerdrSettings(path)
		if err != nil {
			return err
		}
		data, err := editHerdrHooks(raw, command)
		if err != nil {
			return fmt.Errorf("%s: %w (backup: %s)", path, err, backup)
		}
		if !bytes.Equal(raw, data) {
			destination, err := realSetupDestination(path)
			if err != nil {
				return err
			}
			if err := writeSetupFile(destination, data, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// RunHerdrCommand exposes an opt-in installer and the private hook entrypoint.
func RunHerdrCommand(argv []string, in io.Reader, out io.Writer) (int, error) {
	usage := "usage: agb herdr install|uninstall [--target NAME] [--dry-run] [--config PATH]"
	if len(argv) == 0 || argv[0] == "--help" || argv[0] == "-h" {
		fmt.Fprintln(out, usage)
		return 0, nil
	}
	action := argv[0]
	if action != "install" && action != "uninstall" && action != "hook" {
		return 2, fmt.Errorf("%s", usage)
	}
	flags := flag.NewFlagSet("agb herdr "+action, flag.ContinueOnError)
	flags.SetOutput(out)
	if action == "hook" {
		tool := flags.String("tool", "", "agent tool")
		home := flags.String("home", "", "isolated account home; omitted for default account")
		// Invalid hooks must also fail open, without stdout becoming context.
		flags.SetOutput(io.Discard)
		if flags.Parse(argv[1:]) == nil && flags.NArg() == 0 && (*tool == "claude" || *tool == "codex") {
			runHerdrHook(Account{Tool: Source(*tool), Home: *home}, in)
		}
		return 0, nil
	}
	target := flags.String("target", "", "one configured Claude or Codex account; omitted for all")
	configPath := flags.String("config", ConfigPath(), "agb configuration file")
	dryRun := flags.Bool("dry-run", false, "preview without writing or invoking Herdr")
	if err := flags.Parse(argv[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, err
	}
	if flags.NArg() != 0 {
		return 2, fmt.Errorf("%s", usage)
	}
	raw, err := os.ReadFile(*configPath)
	if os.IsNotExist(err) {
		raw, err = []byte("{}"), nil
	}
	if err != nil {
		return 2, err
	}
	config, err := ParseConfig(raw)
	if err != nil {
		return 2, err
	}
	var accounts []Account
	seen := map[string]bool{}
	for _, account := range config.Targets() {
		if account.Tool != SourceClaude && account.Tool != SourceCodex || *target != "" && account.Name != *target {
			continue
		}
		if account.Home != "" {
			account.Home, err = filepath.Abs(account.Home)
			if err != nil {
				return 2, err
			}
		}
		key := string(account.Tool) + ":" + herdrAccountHome(account)
		if !seen[key] {
			seen[key] = true
			accounts = append(accounts, account)
		}
	}
	if len(accounts) == 0 {
		return 2, fmt.Errorf("no configured Claude or Codex account matches %q", *target)
	}
	binary, err := os.Executable()
	if err != nil {
		return 1, err
	}
	if err := configureHerdrAccounts(accounts, binary, action == "install", *dryRun || dryRunEnabled(), out); err != nil {
		return 1, err
	}
	if !*dryRun && !dryRunEnabled() {
		fmt.Fprintln(out, "Restart agents or resume them through agb to load the updated hooks. Keep the Herdr server running.")
	}
	return 0, nil
}
