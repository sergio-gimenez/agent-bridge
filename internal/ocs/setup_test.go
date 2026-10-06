package ocs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func setupFixture(t *testing.T) (Config, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source", "cloudflare")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"SKILL.md": "---\nname: cloudflare\ndescription: Cloudflare setup\n---\nRead references/details.md", "references/details.md": "Supporting context"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := Config{
		Opencode:       OpencodeAccount,
		ClaudeAccounts: []Account{{Tool: SourceClaude, Name: "cc1", Home: filepath.Join(root, "claude")}},
		CodexAccounts:  []Account{{Tool: SourceCodex, Name: "cx1", Home: filepath.Join(root, "codex")}},
		Setup: &SetupConfig{Version: 1,
			Destinations: map[string]SetupDestination{
				"oc":  {MCPConfig: filepath.Join(root, "opencode.jsonc"), SkillsDir: filepath.Join(root, "shared-skills")},
				"cc1": {MCPConfig: filepath.Join(root, "claude", ".claude.json"), SkillsDir: filepath.Join(root, "claude", "skills")},
				"cx1": {MCPConfig: filepath.Join(root, "codex", "config.toml"), SkillsDir: filepath.Join(root, "shared-skills")},
			},
			Profiles: map[string]SetupProfile{"development": {Targets: []string{"oc", "cc1", "cx1"}, Skills: map[string]SetupSkill{"cloudflare": {Path: source}}, MCPs: map[string]SetupMCP{
				"cloudflare-docs": {Transport: "http", URL: "https://docs.mcp.cloudflare.com/mcp"},
				"local-tool":      {Transport: "stdio", Command: "node", Args: []string{"/path with spaces/server.js"}, EnvVars: []string{"LOCAL_TOKEN"}},
			}}},
		},
	}
	path := filepath.Join(root, "config.json")
	// Serialize through the real parser rather than relying on Go's field names.
	raw, err := json.Marshal(map[string]any{"claudeAccounts": []obj{{"name": "cc1", "configDir": config.ClaudeAccounts[0].Home}}, "codexAccounts": []obj{{"name": "cx1", "codexHome": config.CodexAccounts[0].Home}}, "setup": config.Setup})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, path
}

func setupPlan(t *testing.T, config Config, path string) *SetupPlan {
	t.Helper()
	plan, err := BuildSetupPlan(config, path, "development", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasConflicts() {
		t.Fatalf("unexpected conflicts: %+v", plan.Steps)
	}
	return plan
}

func putSetupFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetupSyncAcrossToolsIsIdempotentAndPreservesSettings(t *testing.T) {
	config, path := setupFixture(t)
	initial := map[string]string{
		"oc":  "{\n// Keep this comment\n\"model\":\"vendor/model\",\n\"mcp\":{\"existing\":{\"type\":\"remote\",\"url\":\"https://existing.example/mcp\"},},\n}\n",
		"cc1": "{\"projects\":{\"/repo\":{\"permissions\":[\"private\"]}},\"mcpServers\":{\"existing\":{\"type\":\"http\",\"url\":\"https://existing.example/mcp\"}}}\n",
		"cx1": "# Keep this comment\nmodel = 'example'\n[mcp_servers.existing]\nurl = 'https://existing.example/mcp'\n[projects.'/repo']\ntrust_level = 'trusted'\n",
	}
	for target, text := range initial {
		putSetupFile(t, config.Setup.Destinations[target].MCPConfig, text)
	}
	plan := setupPlan(t, config, path)
	if !plan.HasChanges() {
		t.Fatal("first plan should add capabilities")
	}
	if fileExists(plan.statePath) || fileExists(config.Setup.Destinations["cc1"].SkillsDir) {
		t.Fatal("read-only plan wrote files")
	}
	for target, text := range initial {
		if readSeed(t, config.Setup.Destinations[target].MCPConfig) != text {
			t.Fatal("plan modified native configuration")
		}
	}
	if err := ApplySetupPlan(plan); err != nil {
		t.Fatal(err)
	}
	for _, account := range config.Targets() {
		dest := config.Setup.Destinations[account.Name]
		raw := []byte(readSeed(t, dest.MCPConfig))
		root, err := nativeRoot(raw, account.Tool)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := nativeRoot([]byte(initial[account.Name]), account.Tool)
		key := nativeMCPKey(account.Tool)
		old := before[key].(map[string]any)
		entries := root[key].(map[string]any)
		if !reflect.DeepEqual(entries["existing"], old["existing"]) {
			t.Fatal("unrelated MCP changed")
		}
		delete(root, key)
		delete(before, key)
		if !reflect.DeepEqual(root, before) {
			t.Fatal("unrelated settings changed")
		}
		if account.Tool != SourceClaude && !bytes.Contains(raw, []byte("Keep this comment")) {
			t.Fatal("native comment lost")
		}
		if got := readSeed(t, filepath.Join(dest.SkillsDir, "cloudflare", "references", "details.md")); got != "Supporting context" {
			t.Fatal("skill resources were not linked")
		}
	}
	again := setupPlan(t, config, path)
	if again.HasChanges() {
		t.Fatalf("second sync is not a no-op: %+v", again.Steps)
	}
	stateBefore := readSeed(t, plan.statePath)
	if err := ApplySetupPlan(again); err != nil {
		t.Fatal(err)
	}
	if stateBefore != readSeed(t, plan.statePath) {
		t.Fatal("no-op sync rewrote ownership state")
	}
	backups, err := os.ReadDir(filepath.Join(filepath.Dir(path), "sync-backups"))
	if err != nil || len(backups) != 3 {
		t.Fatalf("backups = %v, %v", backups, err)
	}
	for _, backup := range backups {
		info, _ := backup.Info()
		if info.Mode().Perm() != 0o600 {
			t.Fatal("backup is not private")
		}
	}
}

func TestLegacyOCSManagedBlockMigratesToAgentBridgeMarkers(t *testing.T) {
	raw := []byte("# ocs MCP begin: docs\n[mcp_servers.docs]\nurl = 'https://old.example/mcp'\n# ocs MCP end: docs\n")
	updated, err := editNativeMCP(raw, SourceCodex, "docs", map[string]any{"url": "https://new.example/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(updated, []byte("# AgentBridge MCP begin: docs")) || bytes.Contains(updated, []byte("# ocs MCP")) {
		t.Fatalf("legacy markers were not migrated:\n%s", updated)
	}
}

func TestSetupUpdatesAndExplicitDisableOnlyManagedEntries(t *testing.T) {
	config, path := setupFixture(t)
	if err := ApplySetupPlan(setupPlan(t, config, path)); err != nil {
		t.Fatal(err)
	}
	profile := config.Setup.Profiles["development"]
	mcp := profile.MCPs["cloudflare-docs"]
	mcp.URL = "https://other.example/mcp"
	profile.MCPs["cloudflare-docs"] = mcp
	off := false
	profile.Overrides = map[string]SetupOverride{"cc1": {MCPs: map[string]SetupMCP{"local-tool": {Enabled: &off}}}}
	config.Setup.Profiles["development"] = profile
	plan := setupPlan(t, config, path)
	if err := ApplySetupPlan(plan); err != nil {
		t.Fatal(err)
	}
	for _, account := range config.Targets() {
		entries, _ := nativeEntries([]byte(readSeed(t, config.Setup.Destinations[account.Name].MCPConfig)), account.Tool)
		if entries["cloudflare-docs"].(map[string]any)["url"] != mcp.URL {
			t.Fatal("managed entry was not updated")
		}
		_, exists := entries["local-tool"]
		if exists != (account.Name != "cc1") {
			t.Fatal("target exclusion did not apply")
		}
	}
	if setupPlan(t, config, path).HasChanges() {
		t.Fatal("updated setup still has drift")
	}
}

func TestSetupConflictPreventsAllWrites(t *testing.T) {
	config, path := setupFixture(t)
	nativePath := config.Setup.Destinations["cc1"].MCPConfig
	original := "{\"mcpServers\":{\"cloudflare-docs\":{\"type\":\"http\",\"url\":\"https://private.example/mcp\"}}}"
	putSetupFile(t, nativePath, original)
	plan, err := BuildSetupPlan(config, path, "development", "")
	if err != nil || !plan.HasConflicts() {
		t.Fatalf("missing conflict: %v, %+v", err, plan)
	}
	if err := ApplySetupPlan(plan); err == nil {
		t.Fatal("conflicted plan was applied")
	}
	if readSeed(t, nativePath) != original || fileExists(config.Setup.Destinations["oc"].MCPConfig) || fileExists(plan.statePath) {
		t.Fatal("conflicted sync changed a destination")
	}
}

func TestSetupDetectsManualChangesAndStalePlans(t *testing.T) {
	config, path := setupFixture(t)
	if err := ApplySetupPlan(setupPlan(t, config, path)); err != nil {
		t.Fatal(err)
	}
	stale := setupPlan(t, config, path)
	profile := config.Setup.Profiles["development"]
	mcp := profile.MCPs["cloudflare-docs"]
	mcp.URL = "https://next.example/mcp"
	profile.MCPs["cloudflare-docs"] = mcp
	config.Setup.Profiles["development"] = profile
	stale = setupPlan(t, config, path)
	nativePath := config.Setup.Destinations["cc1"].MCPConfig
	original := readSeed(t, nativePath)
	changed := strings.Replace(original, "https://docs.mcp.cloudflare.com/mcp", "https://manually-edited.example/mcp", 1)
	putSetupFile(t, nativePath, changed)
	if err := ApplySetupPlan(stale); err == nil || !strings.Contains(err.Error(), "changed since planning") {
		t.Fatalf("stale plan was applied: %v", err)
	}
	plan, err := BuildSetupPlan(config, path, "development", "")
	if err != nil || !plan.HasConflicts() {
		t.Fatal("manual change was not detected")
	}
	if readSeed(t, nativePath) != changed {
		t.Fatal("manual edit was overwritten")
	}
}

func TestSetupJSONPlanCheckAndDryRunNeverWrite(t *testing.T) {
	config, path := setupFixture(t)
	t.Setenv("LOCAL_TOKEN", "do-not-print-this-token")
	for _, command := range []string{"plan", "sync"} {
		var out bytes.Buffer
		code, err := RunSetupCommand(command, []string{"--config", path, "--json", "--check"}, &out)
		if code != 1 || err != nil {
			t.Fatalf("check = %d, %v", code, err)
		}
		var payload map[string]any
		if json.Unmarshal(out.Bytes(), &payload) != nil || strings.Contains(out.String(), "do-not-print-this-token") || strings.Contains(out.String(), "stateBefore") {
			t.Fatal("JSON output is invalid or reveals internal state")
		}
		if fileExists(config.Setup.Destinations["oc"].MCPConfig) || fileExists(filepath.Join(filepath.Dir(path), "sync-state.json")) {
			t.Fatal("check wrote files")
		}
	}
	var out bytes.Buffer
	if code, err := RunSetupCommand("sync", []string{"--config", path, "--dry-run"}, &out); code != 0 || err != nil {
		t.Fatalf("dry run = %d, %v", code, err)
	}
	if fileExists(config.Setup.Destinations["oc"].MCPConfig) {
		t.Fatal("dry run wrote files")
	}
	out.Reset()
	if code, err := RunSetupCommand("sync", []string{"--config", path}, &out); code != 0 || err != nil {
		t.Fatalf("CLI sync = %d, %v", code, err)
	}
	out.Reset()
	if code, err := RunSetupCommand("sync", []string{"--config", path, "--check"}, &out); code != 0 || err != nil {
		t.Fatalf("no-op check = %d, %v", code, err)
	}
}

func TestSetupSharedSkillExclusionsAreReported(t *testing.T) {
	config, path := setupFixture(t)
	profile := config.Setup.Profiles["development"]
	off := false
	profile.Overrides = map[string]SetupOverride{"cx1": {Skills: map[string]SetupSkill{"cloudflare": {Enabled: &off}}}}
	config.Setup.Profiles["development"] = profile
	for _, filter := range []string{"", "oc", "cx1"} {
		plan, err := BuildSetupPlan(config, path, "development", filter)
		if err != nil || !plan.HasConflicts() {
			t.Fatalf("shared isolation conflict bypassed with target %q", filter)
		}
	}
}

func TestSetupMCPTransportAndEnvironmentTranslation(t *testing.T) {
	for _, tool := range []Source{SourceClaude, SourceCodex, SourceOpencode} {
		entry, err := (SetupMCP{Transport: "http", URL: "https://example.com/mcp", BearerTokenEnv: "CF_TOKEN"}).native(tool)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(entry)
		if !bytes.Contains(raw, []byte("CF_TOKEN")) {
			t.Fatal("environment reference missing")
		}
	}
	for _, mcp := range []SetupMCP{
		{Transport: "http", URL: "https://user:secret@example.com/mcp"}, {Transport: "http", URL: "file:///secret"},
		{Transport: "http", URL: "https://example.com", Command: "node"}, {Transport: "stdio"},
		{Transport: "stdio", Command: "node", EnvVars: []string{"BAD=VALUE"}},
	} {
		if _, err := mcp.native(SourceCodex); err == nil {
			t.Fatalf("invalid MCP accepted: %+v", mcp)
		}
	}
}

func TestSetupEditorsPreserveJSONCAndTOMLWithQuotedContent(t *testing.T) {
	for _, raw := range []string{
		"{ /* keep */ \"mcp\": { /* keep too */ }, \"x\": \"https://example.com/a,//notacomment\", }\n",
		"{\"mcp\":{\"other\":{\"type\":\"remote\",\"url\":\"https://a.example\"} /* comment after value */ ,},\"x\":2}",
	} {
		desired := map[string]any{"type": "remote", "url": "https://b.example/mcp"}
		updated, err := editNativeMCP([]byte(raw), SourceOpencode, "test", desired)
		if err != nil {
			t.Fatal(err)
		}
		removed, err := editNativeMCP(updated, SourceOpencode, "test", nil)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := nativeRoot([]byte(raw), SourceOpencode)
		after, _ := nativeRoot(removed, SourceOpencode)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("JSONC unrelated fields changed")
		}
	}
	raw := "# multiline string must remain intact\ntext = '''\n[mcp_servers.fake]\nurl = 'not-a-server'\n'''\n"
	updated, err := editNativeMCP([]byte(raw), SourceCodex, "test", map[string]any{"url": "https://example.com/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(updated, []byte(raw)) {
		t.Fatal("multiline TOML text changed")
	}
	removed, err := editNativeMCP(updated, SourceCodex, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := nativeRoot([]byte(raw), SourceCodex)
	after, _ := nativeRoot(removed, SourceCodex)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("TOML removal changed unrelated configuration")
	}
}

func TestSetupValidationAndBadDestinations(t *testing.T) {
	for _, raw := range []string{`{"setup":{"version":2}}`, `{"setup":{"version":1,"profilse":{}}}`} {
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Fatal("invalid setup configuration accepted")
		}
	}
	config, path := setupFixture(t)
	blocker := filepath.Join(filepath.Dir(path), "not-a-directory")
	dest := config.Setup.Destinations["cc1"]
	dest.SkillsDir = filepath.Join(blocker, "skills")
	config.Setup.Destinations["cc1"] = dest
	plan := setupPlan(t, config, path)
	putSetupFile(t, blocker, "blocked")
	if err := ApplySetupPlan(plan); err == nil {
		t.Fatal("invalid destination did not stop apply")
	}
	if fileExists(config.Setup.Destinations["oc"].MCPConfig) {
		t.Fatal("preflight failure left partial registrations")
	}
}

func TestSetupEquivalentManualMCPsRemainUnmanaged(t *testing.T) {
	config, path := setupFixture(t)
	profile := config.Setup.Profiles["development"]
	profile.Skills = nil
	delete(profile.MCPs, "local-tool")
	config.Setup.Profiles["development"] = profile
	for _, account := range config.Targets() {
		desired, _ := profile.MCPs["cloudflare-docs"].native(account.Tool)
		delete(desired, "enabled")
		if account.Tool == SourceClaude {
			desired["type"] = "streamable-http"
		}
		desired["headers"] = map[string]string{"Authorization": "Bearer private-native-token"}
		raw, err := editNativeMCP(nil, account.Tool, "cloudflare-docs", desired)
		if err != nil {
			t.Fatal(err)
		}
		putSetupFile(t, config.Setup.Destinations[account.Name].MCPConfig, string(raw))
	}
	plan := setupPlan(t, config, path)
	if plan.HasChanges() || len(plan.state.Managed) != 0 {
		t.Fatal("equivalent existing servers were adopted or modified")
	}
	raw, _ := json.Marshal(plan)
	if bytes.Contains(raw, []byte("private-native-token")) {
		t.Fatal("plan leaked native credentials")
	}
	if err := ApplySetupPlan(plan); err != nil {
		t.Fatal(err)
	}
	if fileExists(plan.statePath) {
		t.Fatal("no-op plan created ownership state")
	}
}

func TestSetupTargetFilterAndSymlinkedConfig(t *testing.T) {
	config, path := setupFixture(t)
	nativePath := config.Setup.Destinations["cc1"].MCPConfig
	realPath := filepath.Join(filepath.Dir(path), "dotfiles", "claude.json")
	putSetupFile(t, realPath, "{\"projects\":{\"keep\":true}}")
	if err := os.MkdirAll(filepath.Dir(nativePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, nativePath); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildSetupPlan(config, path, "development", "cc1")
	if err != nil || plan.HasConflicts() {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if err := ApplySetupPlan(plan); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(nativePath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config symlink was replaced")
	}
	if fileExists(config.Setup.Destinations["oc"].MCPConfig) || fileExists(config.Setup.Destinations["cx1"].MCPConfig) {
		t.Fatal("target filter wrote another account's config")
	}
	if !strings.Contains(readSeed(t, realPath), "cloudflare-docs") {
		t.Fatal("actual config behind symlink was not updated")
	}
}

func TestSetupDetectsSharedDirectoriesThroughAliases(t *testing.T) {
	config, path := setupFixture(t)
	shared := config.Setup.Destinations["oc"].SkillsDir
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(path), "alias")
	if err := os.Symlink(shared, alias); err != nil {
		t.Fatal(err)
	}
	dest := config.Setup.Destinations["cx1"]
	dest.SkillsDir = alias
	config.Setup.Destinations["cx1"] = dest
	off := false
	profile := config.Setup.Profiles["development"]
	profile.Overrides = map[string]SetupOverride{"cx1": {Skills: map[string]SetupSkill{"cloudflare": {Enabled: &off}}}}
	config.Setup.Profiles["development"] = profile
	plan, err := BuildSetupPlan(config, path, "development", "")
	if err != nil || !plan.HasConflicts() {
		t.Fatal("alias bypassed shared-directory exclusion conflict")
	}
}

func TestSetupExampleUsesConfiguredAccountNames(t *testing.T) {
	_, path := setupFixture(t)
	var out bytes.Buffer
	code, err := RunSetupCommand("setup", []string{"--example", "--config", path}, &out)
	if code != 0 || err != nil {
		t.Fatalf("example = %d, %v", code, err)
	}
	var example struct {
		Setup SetupConfig `json:"setup"`
	}
	if err := json.Unmarshal(out.Bytes(), &example); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(example.Setup.Profiles["development"].Targets, []string{"oc", "cc1", "cx1"}) {
		t.Fatal("example invented account names")
	}
}
