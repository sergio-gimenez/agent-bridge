package ocs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Setup *SetupConfig
	// When true, launch the target tool with permission checks bypassed
	// (claude: --dangerously-skip-permissions, opencode: --auto,
	// codex: --dangerously-bypass-approvals-and-sandbox).
	SkipPermissions bool
	// OpenCode has no accounts, so its per-tool settings live in their own
	// block.
	Opencode             Account
	ClaudeAccounts       []Account
	DefaultClaudeAccount string
	CodexAccounts        []Account
	DefaultCodexAccount  string
}

// Each tool names its isolated home with its own environment variable, so each
// keeps its own config key rather than sharing one generic name.
var homeKey = map[Source]string{
	SourceClaude: "configDir",
	SourceCodex:  "codexHome",
}

var defaultAccounts = map[Source]Account{
	SourceClaude: {Tool: SourceClaude, Name: "cc"},
	SourceCodex:  {Tool: SourceCodex, Name: "cx"},
}

func ConfigPath() string {
	if path := os.Getenv("AGB_CONFIG_PATH"); path != "" {
		return path
	}
	if path := os.Getenv("OCS_CONFIG_PATH"); path != "" {
		return path
	}
	return preferredPath(
		filepath.Join(homeDir(), ".config", "agentbridge", "config.json"),
		filepath.Join(homeDir(), ".config", "ocs", "config.json"),
	)
}

// preferredPath lets AgentBridge adopt its new name without stranding an
// existing ocs installation. New installs use the first path; an existing
// legacy file remains authoritative until the user moves it.
func preferredPath(current, legacy string) string {
	if _, err := os.Stat(current); err == nil {
		return current
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return current
}

func dryRunEnabled() bool {
	return os.Getenv("AGB_DRY_RUN") != "" || os.Getenv("OCS_DRY_RUN") != ""
}

// Only a real boolean sets a per-account default; anything else leaves it
// unset, so the account falls back to the global setting.
func skipPermissionsOf(entry any) *bool {
	fields, ok := entry.(map[string]any)
	if !ok {
		return nil
	}
	value, ok := fields["skipPermissions"].(bool)
	if !ok {
		return nil
	}
	return &value
}

func parseAccounts(raw any, tool Source) []Account {
	entries, _ := raw.([]any)
	seen := map[string]bool{}
	var accounts []Account

	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, ok := fields["name"].(string)
		if !ok {
			continue
		}
		home, hasHome := fields[homeKey[tool]]
		homePath, isString := home.(string)
		if hasHome && !isString {
			continue
		}

		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		account := Account{Tool: tool, Name: name, SkipPermissions: skipPermissionsOf(fields)}
		if homePath != "" {
			account.Home = expandHome(homePath)
		}
		accounts = append(accounts, account)
	}

	if len(accounts) == 0 {
		return []Account{defaultAccounts[tool]}
	}
	return accounts
}

func pickDefault(accounts []Account, requested any) string {
	name, _ := requested.(string)
	name = strings.ToLower(strings.TrimSpace(name))
	for _, account := range accounts {
		if account.Name == name {
			return name
		}
	}
	return accounts[0].Name
}

func ParseConfig(raw []byte) (Config, error) {
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Config{}, err
	}
	setup, err := parseSetup(parsed["setup"])
	if err != nil {
		return Config{}, err
	}

	claude := parseAccounts(parsed["claudeAccounts"], SourceClaude)
	codex := parseAccounts(parsed["codexAccounts"], SourceCodex)
	skip, _ := parsed["skipPermissions"].(bool)

	opencode := OpencodeAccount
	opencode.SkipPermissions = skipPermissionsOf(parsed["opencode"])

	return Config{
		Setup:                setup,
		SkipPermissions:      skip,
		Opencode:             opencode,
		ClaudeAccounts:       claude,
		DefaultClaudeAccount: pickDefault(claude, parsed["defaultClaudeAccount"]),
		CodexAccounts:        codex,
		DefaultCodexAccount:  pickDefault(codex, parsed["defaultCodexAccount"]),
	}, nil
}

func LoadConfig() Config {
	raw, err := os.ReadFile(ConfigPath())
	if err == nil {
		if config, err := ParseConfig(raw); err == nil {
			return config
		}
	}
	config, _ := ParseConfig([]byte("{}"))
	return config
}

// A tool's default account leads its group, so "the first account of this
// tool" is the configured default wherever a route lands on a tool without
// naming an account.
func defaultFirst(accounts []Account, defaultName string) []Account {
	var first, rest []Account
	for _, account := range accounts {
		if account.Name == defaultName {
			first = append(first, account)
		} else {
			rest = append(rest, account)
		}
	}
	return append(first, rest...)
}

// Targets is the one ordered list of destinations the picker cycles: OpenCode,
// then every Claude account, then every Codex account.
func (config Config) Targets() []Account {
	targets := []Account{config.Opencode}
	targets = append(targets, defaultFirst(config.ClaudeAccounts, config.DefaultClaudeAccount)...)
	return append(targets, defaultFirst(config.CodexAccounts, config.DefaultCodexAccount)...)
}

// DefaultSkipPermissions: the CLI flag decides for this run; without one, the
// target's own setting beats the global default. Ctrl+Y in the picker
// overrides all of these.
func (config Config) DefaultSkipPermissions(cliFlag *bool, target Account) bool {
	if cliFlag != nil {
		return *cliFlag
	}
	for _, candidate := range config.Targets() {
		if candidate.Tool == target.Tool && candidate.Name == target.Name && candidate.SkipPermissions != nil {
			return *candidate.SkipPermissions
		}
	}
	return config.SkipPermissions
}
