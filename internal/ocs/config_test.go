package ocs

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func mustConfig(t *testing.T, raw string) Config {
	t.Helper()
	config, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func targetNames(config Config) []string {
	var names []string
	for _, target := range config.Targets() {
		names = append(names, target.Name)
	}
	return names
}

func TestParseArgs(t *testing.T) {
	if ParseArgs(nil).SkipPermissions != nil {
		t.Fatal("no flag should leave the config in charge")
	}
	for _, flag := range []string{"--dangerous", "--skip-permissions", "--yolo"} {
		if got := ParseArgs([]string{flag}).SkipPermissions; got == nil || !*got {
			t.Fatalf("%s did not enable skipping", flag)
		}
	}
	if got := ParseArgs([]string{"--safe"}).SkipPermissions; got == nil || *got {
		t.Fatal("--safe did not force checks on")
	}
	if ParseArgs([]string{"--claude-account", "CC2"}).Target != "cc2" ||
		ParseArgs([]string{"--codex-account", "CX2"}).Target != "cx2" ||
		ParseArgs([]string{"--target", "OC"}).Target != "oc" {
		t.Fatal("target flags")
	}
	if got := ParseArgs([]string{"--query", "mesh vpn", "--assistant"}); got.Query != "mesh vpn" || got.Search != ScopeAll {
		t.Fatalf("got %+v", got)
	}
}

func TestParseConfig(t *testing.T) {
	if !mustConfig(t, `{"skipPermissions":true}`).SkipPermissions || mustConfig(t, `{}`).SkipPermissions {
		t.Fatal("skipPermissions")
	}

	config := mustConfig(t, `{
		"claudeAccounts": [{"name": "CC1"}, {"name": "cc2", "configDir": "/tmp/cc2"}, {"name": "bad", "configDir": null}],
		"defaultClaudeAccount": "CC2",
		"codexAccounts": [{"name": "cx1"}, {"name": "CX2", "codexHome": "/tmp/cx2"}],
		"defaultCodexAccount": "cx2"
	}`)
	wantClaude := []Account{{Tool: SourceClaude, Name: "cc1"}, {Tool: SourceClaude, Name: "cc2", Home: "/tmp/cc2"}}
	if !reflect.DeepEqual(config.ClaudeAccounts, wantClaude) || config.DefaultClaudeAccount != "cc2" {
		t.Fatalf("claude = %+v / %s", config.ClaudeAccounts, config.DefaultClaudeAccount)
	}
	wantCodex := []Account{{Tool: SourceCodex, Name: "cx1"}, {Tool: SourceCodex, Name: "cx2", Home: "/tmp/cx2"}}
	if !reflect.DeepEqual(config.CodexAccounts, wantCodex) || config.DefaultCodexAccount != "cx2" {
		t.Fatalf("codex = %+v", config.CodexAccounts)
	}
	// Each tool's group is led by its default account, which is where Tab lands.
	if got := targetNames(config); !reflect.DeepEqual(got, []string{"oc", "cc2", "cc1", "cx2", "cx1"}) {
		t.Fatalf("targets = %v", got)
	}

	empty := mustConfig(t, `{}`)
	if got := targetNames(empty); !reflect.DeepEqual(got, []string{"oc", "cc", "cx"}) {
		t.Fatalf("default targets = %v", got)
	}

	hosts := mustConfig(t, `{"moveHosts": ["desk", " ", 3], "arrive": {"desk": " herdr x {resume} ", "blank": "  ", "bad": 1}}`)
	if !reflect.DeepEqual(hosts.MoveHosts, []string{"desk"}) || !reflect.DeepEqual(hosts.Arrive, map[string]string{"desk": "herdr x {resume}"}) {
		t.Fatalf("moveHosts %v, arrive %v", hosts.MoveHosts, hosts.Arrive)
	}
}

func TestAgentBridgePathsPreferNewAndFallbackToOCS(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("AGB_CONFIG_PATH", "")
	t.Setenv("OCS_CONFIG_PATH", "")
	t.Setenv("AGB_CACHE_PATH", "")
	t.Setenv("OCS_CACHE_PATH", "")

	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	newConfig := filepath.Join(root, ".config", "agentbridge", "config.json")
	oldConfig := filepath.Join(root, ".config", "ocs", "config.json")
	newCache := filepath.Join(cacheRoot, "agentbridge", "index.gob")
	oldCache := filepath.Join(cacheRoot, "ocs", "index.gob")
	if ConfigPath() != newConfig || CachePath() != newCache {
		t.Fatal("fresh install should use AgentBridge paths")
	}
	putSetupFile(t, oldConfig, "{}")
	putSetupFile(t, oldCache, "legacy")
	if ConfigPath() != oldConfig || CachePath() != oldCache {
		t.Fatal("existing ocs paths should remain authoritative")
	}
	putSetupFile(t, newConfig, "{}")
	putSetupFile(t, newCache, "current")
	if ConfigPath() != newConfig || CachePath() != newCache {
		t.Fatal("AgentBridge paths should win when both exist")
	}

	t.Setenv("OCS_CONFIG_PATH", filepath.Join(root, "legacy-config.json"))
	t.Setenv("OCS_CACHE_PATH", filepath.Join(root, "legacy-cache.gob"))
	if !strings.Contains(ConfigPath(), "legacy-config") || !strings.Contains(CachePath(), "legacy-cache") {
		t.Fatal("legacy environment overrides should work")
	}
	t.Setenv("AGB_CONFIG_PATH", filepath.Join(root, "agentbridge-config.json"))
	t.Setenv("AGB_CACHE_PATH", filepath.Join(root, "agentbridge-cache.gob"))
	if !strings.Contains(ConfigPath(), "agentbridge-config") || !strings.Contains(CachePath(), "agentbridge-cache") {
		t.Fatal("AgentBridge environment overrides should take precedence")
	}

	t.Setenv("AGB_DRY_RUN", "")
	t.Setenv("OCS_DRY_RUN", "1")
	if !dryRunEnabled() {
		t.Fatal("legacy dry-run override should work")
	}
}

func TestSkipPermissionsPrecedence(t *testing.T) {
	config := mustConfig(t, `{
		"skipPermissions": true,
		"opencode": {"skipPermissions": false},
		"claudeAccounts": [{"name": "cc1"}, {"name": "cc2", "skipPermissions": false}],
		"codexAccounts": [{"name": "cx1", "skipPermissions": "yes"}]
	}`)
	targets := config.Targets()
	oc, cc1, cc2, cx1 := targets[0], targets[1], targets[2], targets[3]

	if config.CodexAccounts[0].SkipPermissions != nil {
		t.Fatal("a non-boolean was read as a setting")
	}
	on, off := true, false
	cases := []struct {
		flag   *bool
		target Account
		want   bool
	}{
		{nil, oc, false},
		{nil, cc1, true},
		{nil, cc2, false},
		{nil, cx1, true},
		{&on, cc2, true},
		{&off, cc1, false},
	}
	for _, c := range cases {
		if got := config.DefaultSkipPermissions(c.flag, c.target); got != c.want {
			t.Errorf("%s with flag %v = %v", c.target.Name, c.flag, got)
		}
	}
}

func TestContinuationPrompt(t *testing.T) {
	session := claudeTestSession()
	prompt := BuildContinuationPrompt(session, []Turn{
		{RoleUser, "How do I make the picker responsive?"},
		{RoleAssistant, "Use terminal width."},
		{RoleUser, "Add badges too."},
	})
	for _, want := range []string{
		"prior Claude Code conversation",
		"Original session: sid",
		"Latest user message: [turn 3]\nAdd badges too.",
		"do not repeat work",
		"USER: How do I make the picker responsive?",
		"ASSISTANT: Use terminal width.",
		"=== TRANSCRIPT ===",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}

	var turns []Turn
	for i := 0; i < 40; i++ {
		role := RoleUser
		if i%2 == 1 {
			role = RoleAssistant
		}
		turns = append(turns, Turn{role, "TURN" + strconv.Itoa(i) + "::" + strings.Repeat("x", 5000)})
	}
	long := BuildContinuationPrompt(session, turns)
	if !strings.Contains(long, "earlier turns omitted from this excerpt") ||
		!strings.Contains(long, "TURN39::") || !strings.Contains(long, "TURN0::") || strings.Contains(long, "TURN2::") {
		t.Fatal("the opening request and newest turns should be kept, with a notice for omitted history")
	}
}

func dryRun(t *testing.T, open func() (int, error)) string {
	t.Helper()
	t.Setenv("AGB_DRY_RUN", "1")
	var out bytes.Buffer
	dryRunOut = &out
	defer func() { dryRunOut = os.Stdout }()
	if _, err := open(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestLaunchCommands(t *testing.T) {
	dir := t.TempDir()
	cx2Account := Account{Tool: SourceCodex, Name: "cx2", Home: "/tmp/.codex-cx2"}

	cases := []struct {
		open    func() (int, error)
		want    string
		without string
	}{
		{func() (int, error) { return OpenClaudeSession("sid", dir, OpenOptions{Fork: true}) }, "claude --resume sid --fork-session", ""},
		{func() (int, error) { return OpenClaudeSession("sid", dir, OpenOptions{}) }, "claude --resume sid", "--fork-session"},
		{func() (int, error) { return OpenOpencodeSession("sid", dir, OpenOptions{Fork: true}) }, "opencode --session sid --fork", ""},
		{func() (int, error) { return OpenOpencodeSession("sid", dir, OpenOptions{}) }, "opencode --session sid", "--fork"},
		{func() (int, error) { return OpenCodexSession("sid", dir, OpenOptions{Fork: true}) }, "codex fork sid", ""},
		{func() (int, error) { return OpenCodexSession("sid", dir, OpenOptions{}) }, "codex resume sid", ""},
		{func() (int, error) { return OpenCodexSession("sid", dir, OpenOptions{Account: &cx2Account}) },
			"CODEX_HOME=/tmp/.codex-cx2 codex resume sid", ""},
		{func() (int, error) { return OpenCodexFresh(dir, "seed", OpenOptions{SkipPermissions: true}) },
			"codex --dangerously-bypass-approvals-and-sandbox seed", ""},
		{func() (int, error) { return OpenClaudeFresh(dir, "seed", OpenOptions{SkipPermissions: true}) },
			"claude --dangerously-skip-permissions seed", ""},
		{func() (int, error) { return OpenOpencodeSession("sid", dir, OpenOptions{SkipPermissions: true}) },
			"opencode --session sid --auto", ""},
	}
	for _, c := range cases {
		output := dryRun(t, c.open)
		if !strings.Contains(output, c.want) || (c.without != "" && strings.Contains(output, c.without)) {
			t.Errorf("output %q: want %q without %q", output, c.want, c.without)
		}
	}

	// The surrounding shell's CLAUDE_CONFIG_DIR is inherited, but it is not
	// part of the route and must not be printed as though AgentBridge had chosen it.
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/.claude-cc2")
	if output := dryRun(t, func() (int, error) { return OpenCodexSession("sid", dir, OpenOptions{}) }); strings.Contains(output, "CLAUDE_CONFIG_DIR") {
		t.Fatalf("inherited env rendered: %q", output)
	}
}

func TestAssertDirectory(t *testing.T) {
	if AssertDirectory(t.TempDir()) != nil {
		t.Fatal("an existing directory was rejected")
	}
	err := AssertDirectory("/tmp/ocs-does-not-exist-9f3a")
	if err == nil || !strings.Contains(err.Error(), "Session directory no longer exists: /tmp/ocs-does-not-exist-9f3a") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderCommand(t *testing.T) {
	if got := RenderCommand("claude", []string{"--resume", "sid"}, "/tmp/project", nil); got != "claude --resume sid\n  in /tmp/project" {
		t.Fatalf("got %q", got)
	}
	env := []envOverride{{"CLAUDE_CONFIG_DIR", "/tmp/.claude-cc2"}}
	if got := RenderCommand("claude", []string{"--resume", "sid"}, "/tmp/project", env); got != "CLAUDE_CONFIG_DIR=/tmp/.claude-cc2 claude --resume sid\n  in /tmp/project" {
		t.Fatalf("got %q", got)
	}

	t.Setenv("HOME", "/home/dev")
	env = []envOverride{{"CLAUDE_CONFIG_DIR", "/home/dev/.claude-cc2"}}
	if got := RenderCommand("claude", []string{"--resume", "sid"}, "/home/dev/code/app", env); got != "CLAUDE_CONFIG_DIR=~/.claude-cc2 claude --resume sid\n  in ~/code/app" {
		t.Fatalf("got %q", got)
	}

	seed := "Continue a prior conversation.\n" + strings.Repeat("x", 500)
	if got := RenderCommand("claude", []string{seed}, "/tmp/project", nil); got != "claude <transcript seed, 531 chars>\n  in /tmp/project" {
		t.Fatalf("got %q", got)
	}
	if got := RenderCommand("opencode", []string{"run", "--dir", "/tmp/a b"}, "/tmp/a b", nil); got != "opencode run --dir \"/tmp/a b\"\n  in /tmp/a b" {
		t.Fatalf("got %q", got)
	}
}
