package ocs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SetupStep struct {
	Target  string `json:"target"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Action  string `json:"action"`
	Path    string `json:"path"`
	Message string `json:"message,omitempty"`
}

type setupFile struct {
	Before, After []byte
	Exists        bool
	Mode          os.FileMode
}
type setupLink struct{ Path, Before, After string }
type setupState struct {
	Version int               `json:"version"`
	Managed map[string]string `json:"managed"`
}

type SetupPlan struct {
	Profile     string      `json:"profile"`
	Steps       []SetupStep `json:"steps"`
	Warnings    []string    `json:"warnings,omitempty"`
	files       map[string]*setupFile
	links       []setupLink
	state       setupState
	statePath   string
	stateBefore []byte
}

func fingerprint(value any) string {
	raw, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func readSetupFile(path string) (*setupFile, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return &setupFile{Mode: 0o600}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("configuration is not a regular file: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &setupFile{Before: raw, After: raw, Exists: true, Mode: info.Mode().Perm()}, nil
}

func setupDestination(account Account, override SetupDestination, base string) (SetupDestination, error) {
	home := homeDir()
	dest := SetupDestination{}
	switch account.Tool {
	case SourceClaude:
		if account.Home == "" {
			dest.MCPConfig = filepath.Join(home, ".claude.json")
			dest.SkillsDir = filepath.Join(home, ".claude", "skills")
		} else {
			dest.MCPConfig = filepath.Join(account.Home, ".claude.json")
			dest.SkillsDir = filepath.Join(account.Home, "skills")
		}
	case SourceCodex:
		root := account.Home
		if root == "" {
			root = filepath.Join(home, ".codex")
		}
		dest.MCPConfig = filepath.Join(root, "config.toml")
		dest.SkillsDir = filepath.Join(home, ".agents", "skills")
	case SourceOpencode:
		root := os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			root = filepath.Join(home, ".config")
		}
		dest.MCPConfig = filepath.Join(root, "opencode", "opencode.json")
		if fileExists(filepath.Join(root, "opencode", "opencode.jsonc")) {
			dest.MCPConfig += "c"
		}
		if path := os.Getenv("OPENCODE_CONFIG"); path != "" {
			dest.MCPConfig = path
		}
		dest.SkillsDir = filepath.Join(home, ".agents", "skills")
	}
	if override.MCPConfig != "" {
		dest.MCPConfig = override.MCPConfig
	}
	if override.SkillsDir != "" {
		dest.SkillsDir = override.SkillsDir
	}
	var err error
	dest.MCPConfig, err = setupPath(dest.MCPConfig, base)
	if err != nil {
		return dest, err
	}
	// Resolve existing ancestors as well as config files, preserving dotfile
	// links and detecting two declared paths to the same physical destination.
	dest.MCPConfig, err = realSetupDestination(dest.MCPConfig)
	if err != nil {
		return dest, err
	}
	dest.SkillsDir, err = setupPath(dest.SkillsDir, base)
	if err == nil {
		dest.SkillsDir, err = realSetupDestination(dest.SkillsDir)
	}
	return dest, err
}

func realSetupDestination(path string) (string, error) {
	var missing []string
	ancestor := path
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			resolved, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		missing = append(missing, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("cannot resolve setup destination")
		}
		ancestor = parent
	}
}

func (plan *SetupPlan) HasConflicts() bool {
	for _, step := range plan.Steps {
		if step.Action == "conflict" {
			return true
		}
	}
	return false
}

func (plan *SetupPlan) HasChanges() bool {
	for _, step := range plan.Steps {
		if step.Action == "add" || step.Action == "update" || step.Action == "remove" {
			return true
		}
	}
	return false
}

// BuildSetupPlan is read-only. In particular, it never creates target homes,
// a state file, a lock, or a skill link.
func BuildSetupPlan(config Config, configPath, profileName, targetFilter string) (*SetupPlan, error) {
	if config.Setup == nil {
		return nil, fmt.Errorf("no setup profiles configured; run agb setup --example")
	}
	if profileName == "" && len(config.Setup.Profiles) == 1 {
		for name := range config.Setup.Profiles {
			profileName = name
		}
	}
	profile, exists := config.Setup.Profiles[profileName]
	if !exists {
		return nil, fmt.Errorf("select --profile from: %s", strings.Join(sortedKeys(config.Setup.Profiles), ", "))
	}
	if len(profile.Targets) == 0 {
		return nil, fmt.Errorf("profile %q has no targets", profileName)
	}
	base, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return nil, err
	}
	plan := &SetupPlan{Profile: profileName, Steps: []SetupStep{}, files: make(map[string]*setupFile), statePath: filepath.Join(base, "sync-state.json"), state: setupState{Version: 1, Managed: make(map[string]string)}}
	if raw, err := os.ReadFile(plan.statePath); err == nil {
		if json.Unmarshal(raw, &plan.state) != nil || plan.state.Version != 1 || plan.state.Managed == nil {
			return nil, fmt.Errorf("invalid sync-state.json; refusing to lose ownership records")
		}
		plan.stateBefore = raw
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	accounts := make(map[string]Account)
	for _, account := range config.Targets() {
		if _, duplicate := accounts[account.Name]; duplicate {
			return nil, fmt.Errorf("duplicate target name %q across tools", account.Name)
		}
		accounts[account.Name] = account
	}
	selected := make(map[string]bool)
	for _, target := range profile.Targets {
		if _, exists := accounts[target]; !exists {
			return nil, fmt.Errorf("unknown profile target %q", target)
		}
		if selected[target] {
			return nil, fmt.Errorf("duplicate profile target %q", target)
		}
		selected[target] = true
	}
	for target := range profile.Overrides {
		if !selected[target] {
			return nil, fmt.Errorf("override target %q is not in profile targets", target)
		}
	}
	for target := range config.Setup.Destinations {
		if _, exists := accounts[target]; !exists {
			return nil, fmt.Errorf("unknown destination target %q", target)
		}
	}
	if targetFilter != "" && !selected[targetFilter] {
		return nil, fmt.Errorf("target %q is not in this profile", targetFilter)
	}
	// Shared physical destinations must resolve consistently even when applying
	// only one target, so a filter cannot bypass an isolation conflict.
	seen := make(map[string]string)
	for _, target := range profile.Targets {
		account := accounts[target]
		dest, err := setupDestination(account, config.Setup.Destinations[target], base)
		if err != nil {
			return nil, err
		}
		skills, mcps := mergedSkills(profile, target), mergedMCPs(profile, target)
		if len(skills) > 0 && (account.Tool == SourceCodex || account.Tool == SourceOpencode) {
			plan.Warnings = append(plan.Warnings, target+": skills in shared discovery directories can also be visible to other local agents/accounts; a link does not guarantee account isolation")
		}
		for _, kind := range []string{"skill", "mcp"} {
			names := sortedKeys(skills)
			if kind == "mcp" {
				names = sortedKeys(mcps)
			}
			for _, name := range names {
				if !setupName.MatchString(name) {
					return nil, fmt.Errorf("invalid %s name %q (use letters, digits, hyphens, underscores)", kind, name)
				}
				path := dest.MCPConfig
				if kind == "skill" {
					path = filepath.Join(dest.SkillsDir, name)
				}
				key := path + "\x00" + kind + "\x00" + name
				var desired any = skills[name]
				if kind == "mcp" {
					desired = mcps[name]
				}
				want := fingerprint(desired)
				if previous, exists := seen[key]; exists && previous != want {
					plan.Steps = append(plan.Steps, SetupStep{target, kind, name, "conflict", path, "profile targets request different values at the same physical destination"})
					continue
				}
				if targetFilter != "" && target != targetFilter {
					seen[key] = want
					continue
				}
				if _, shared := seen[key]; shared && targetFilter == "" {
					plan.Steps = append(plan.Steps, SetupStep{target, kind, name, "shared", path, "same destination and definition as another selected target"})
					continue
				}
				seen[key] = want
				step := SetupStep{Target: target, Kind: kind, Name: name, Path: path}
				if kind == "skill" {
					err = plan.planSkill(&step, key, skills[name], base)
				} else {
					err = plan.planMCP(&step, key, mcps[name], account.Tool)
				}
				if err != nil {
					step.Action, step.Message = "conflict", err.Error()
				}
				plan.Steps = append(plan.Steps, step)
			}
		}
	}
	return plan, nil
}

func linkState(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "unmanaged file or directory", nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), nil
}

func (plan *SetupPlan) planSkill(step *SetupStep, key string, skill SetupSkill, base string) error {
	current, err := linkState(step.Path)
	if err != nil {
		return err
	}
	owned, managed := plan.state.Managed[key]
	if managed && current != "" && fingerprint(current) != owned {
		return fmt.Errorf("managed skill link changed outside AgentBridge; reconcile it before syncing")
	}
	if !setupEnabled(skill.Enabled) {
		step.Action = "disabled"
		if current == "" {
			delete(plan.state.Managed, key)
			return nil
		}
		if !managed {
			return fmt.Errorf("cannot disable an unmanaged skill installation")
		}
		step.Action = "remove"
		plan.links = append(plan.links, setupLink{Path: step.Path, Before: current})
		delete(plan.state.Managed, key)
		return nil
	}
	if skill.Path == "" {
		return fmt.Errorf("skill requires a local path containing SKILL.md")
	}
	source, err := setupPath(skill.Path, base)
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("skill source is missing or unreadable: %s", skill.Path)
	}
	if info, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("skill source must contain a regular SKILL.md")
	}
	if current == source || filepath.Clean(step.Path) == source {
		step.Action = "present"
		return nil
	}
	if relative, err := filepath.Rel(source, step.Path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("skill destination cannot be inside its source folder")
	}
	if current != "" && !managed {
		return fmt.Errorf("skill destination exists and is not managed by AgentBridge")
	}
	step.Action = "add"
	if current != "" {
		step.Action = "update"
	}
	plan.links = append(plan.links, setupLink{step.Path, current, source})
	plan.state.Managed[key] = fingerprint(source)
	return nil
}

func (plan *SetupPlan) planMCP(step *SetupStep, key string, mcp SetupMCP, tool Source) error {
	file := plan.files[step.Path]
	if file == nil {
		var err error
		file, err = readSetupFile(step.Path)
		if err != nil {
			return err
		}
		plan.files[step.Path] = file
	}
	entries, err := nativeEntries(file.After, tool)
	if err != nil {
		return err
	}
	current, exists := entries[step.Name]
	owned, managed := plan.state.Managed[key]
	if managed && exists && fingerprint(current) != owned {
		return fmt.Errorf("managed MCP entry changed outside AgentBridge; reconcile it before syncing")
	}
	var desired map[string]any
	if !setupEnabled(mcp.Enabled) {
		step.Action = "disabled"
		if !exists {
			delete(plan.state.Managed, key)
			return nil
		}
		if !managed {
			return fmt.Errorf("cannot disable an unmanaged MCP entry")
		}
		step.Action = "remove"
	} else {
		desired, err = mcp.native(tool)
		if err != nil {
			return err
		}
		if exists && nativeMCPMatches(current, desired, tool) {
			step.Action = "present"
			return nil
		}
		if exists && !managed {
			return fmt.Errorf("MCP name exists with another definition and is not managed by AgentBridge")
		}
		step.Action = "add"
		if exists {
			step.Action = "update"
		}
	}
	updated, err := editNativeMCP(file.After, tool, step.Name, desired)
	if err != nil {
		return err
	}
	file.After = updated
	if desired == nil {
		delete(plan.state.Managed, key)
	} else {
		plan.state.Managed[key] = fingerprint(desired)
	}
	return nil
}
