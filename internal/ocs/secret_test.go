package ocs

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var secretNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// secretHome is an OpenCode setup like a real one: a local provider, a
// provider on someone else's server, plugins and MCPs, OpenCode Go signed in,
// and a catalog with a model the privacy table has never heard of.
func secretHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("OPENCODE_API_KEY", "")
	writeTestFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{
  // comments, as OpenCode allows
  "$schema": "https://opencode.ai/config.json",
  "permission": {"bash": "ask"},
  "plugin": ["opencode-wakatime", "opencode-claude-memory@2"],
  "mcp": {"cloudflare": {"type": "remote", "url": "https://example.com/mcp"}},
  "share": "auto",
  "provider": {
    "ollama": {"options": {"baseURL": "http://127.0.0.1:11434/v1"}, "models": {"gpt-oss:20b": {"name": "GPT OSS 20B"}}},
    "corp": {"options": {"baseURL": "https://gate.example.com/v1"}, "models": {"a": {}, "b": {}}},
  },
}`)
	writeTestFile(t, filepath.Join(home, ".config", "opencode", "skill", "notes", "SKILL.md"), "a skill")
	writeTestFile(t, filepath.Join(home, ".config", "opencode", "plugins", "leak.js"), "export default {}")
	writeTestFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"),
		`{"opencode-go": {"type": "api", "key": "go-key"}, "openai": {"type": "oauth", "access": "openai-token"}}`)
	writeTestFile(t, filepath.Join(home, ".cache", "opencode", "models.json"), `{"opencode-go": {"models": {
  "glm-5.3": {"name": "GLM-5.3"}, "grok-4.7": {"name": "Grok 4.7"},
  "deepseek-v4-pro": {"name": "DeepSeek V4 Pro"}, "brand-new": {"name": "Brand New"}}}}`)
	return home
}

func secretByID(models []SecretModel) map[string]SecretModel {
	byID := map[string]SecretModel{}
	for _, model := range models {
		byID[model.ID] = model
	}
	return byID
}

func TestLocalEndpoint(t *testing.T) {
	for url, want := range map[string]bool{
		"http://127.0.0.1:11434/v1":    true,
		"http://localhost:8080":        true,
		"http://[::1]:11434":           true,
		"http://192.168.1.20:11434/v1": true,
		"http://172.27.13.135:11434":   true,
		"https://aigate.i2cat.net/v1":  false,
		"https://8.8.8.8/v1":           false,
		"http://llm-2:11434":           false,
		"not a url at all, just words": false,
	} {
		if _, got := localEndpoint(url); got != want {
			t.Errorf("localEndpoint(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestSecretModelsTiers(t *testing.T) {
	secretHome(t)
	models := SecretModels(Config{}, secretNow)
	byID := secretByID(models)
	for id, want := range map[string]SecretTier{
		"ollama/gpt-oss:20b":          SecretLocal,
		"opencode-go/glm-5.3":         SecretZDR,
		"opencode-go/deepseek-v4-pro": SecretZDR,
		"opencode-go/grok-4.7":        SecretRetained,
		"opencode-go/brand-new":       SecretUnknown,
		"corp/a":                      SecretUnknown,
	} {
		if got, ok := byID[id]; !ok || got.Tier != want {
			t.Errorf("%s: got %+v, want tier %v", id, got, want)
		}
	}
	if !strings.Contains(byID["opencode-go/deepseek-v4-pro"].Note, "until 2026-10-31") {
		t.Errorf("a monthly agreement does not say when it ends: %q", byID["opencode-go/deepseek-v4-pro"].Note)
	}
	// Pickable first, local before zero retention.
	for i := 1; i < len(models); i++ {
		if models[i].Tier < models[i-1].Tier {
			t.Fatalf("not sorted by tier: %+v", models)
		}
	}
	if models[0].ID != "ollama/gpt-oss:20b" {
		t.Errorf("first model is %s", models[0].ID)
	}
	// An agreement that runs out comes after the ones that do not.
	var zdr []string
	for _, model := range models {
		if model.Tier == SecretZDR {
			zdr = append(zdr, model.ID)
		}
	}
	if len(zdr) != 2 || zdr[0] != "opencode-go/glm-5.3" {
		t.Errorf("zero-retention order: %v", zdr)
	}
}

func TestSecretModelsWhenAgreementLapses(t *testing.T) {
	secretHome(t)
	later := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	model := secretByID(SecretModels(Config{}, later))["opencode-go/deepseek-v4-pro"]
	if model.Tier != SecretUnknown || !strings.Contains(model.Note, "ended 2026-10-31") {
		t.Errorf("after its agreement ended: %+v", model)
	}
}

func TestSecretModelsWithoutGoSignIn(t *testing.T) {
	home := secretHome(t)
	writeTestFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{}`)
	for _, model := range SecretModels(Config{}, secretNow) {
		if model.Provider() == "opencode-go" {
			t.Fatalf("offered %s without an OpenCode Go sign-in", model.ID)
		}
	}
}

func TestSecretConfigOverrides(t *testing.T) {
	secretHome(t)
	config, err := ParseConfig([]byte(`{"secret": {"default": "corp/b", "models": {
		"corp/*": "zdr", "opencode-go/glm-5.3": "retained", "elsewhere/m": "local", "bad/x": "sure"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Secret.Default != "corp/b" {
		t.Errorf("default = %q", config.Secret.Default)
	}
	byID := secretByID(SecretModels(config, secretNow))
	for id, want := range map[string]SecretTier{
		"corp/a":              SecretZDR,
		"corp/b":              SecretZDR,
		"opencode-go/glm-5.3": SecretRetained,
		"elsewhere/m":         SecretLocal,
	} {
		if byID[id].Tier != want {
			t.Errorf("%s: got %+v, want tier %v", id, byID[id], want)
		}
	}
	if _, ok := byID["bad/x"]; ok {
		t.Error("an unknown tier name was accepted")
	}
}

func TestFindSecretModelRefusesWhatIsNotPrivate(t *testing.T) {
	secretHome(t)
	models := SecretModels(Config{}, secretNow)
	if _, err := FindSecretModel(models, "opencode-go/glm-5.3"); err != nil {
		t.Errorf("zero retention refused: %v", err)
	}
	for _, id := range []string{"opencode-go/grok-4.7", "corp/a", "opencode-go/brand-new"} {
		if _, err := FindSecretModel(models, id); err == nil || !strings.Contains(err.Error(), "not private enough") {
			t.Errorf("%s: %v", id, err)
		}
	}
	if _, err := FindSecretModel(models, "nowhere/none"); err == nil {
		t.Error("an unknown model was accepted")
	}
}

func TestPrepareSecretStoreKeepsOnlyTheAllowedConfig(t *testing.T) {
	secretHome(t)
	model := secretByID(SecretModels(Config{}, secretNow))["ollama/gpt-oss:20b"]
	dir := t.TempDir()
	store, err := prepareSecretStore(dir, model)
	if err != nil {
		t.Fatal(err)
	}

	written := readOpencodeJSON(filepath.Join(dir, "config", "opencode", "opencode.json"))
	if written["model"] != model.ID || written["small_model"] != model.ID || written["share"] != "disabled" {
		t.Errorf("model or sharing not pinned: %v", written)
	}
	if plugins, _ := written["plugin"].([]any); len(plugins) != 0 {
		t.Errorf("plugins kept: %v", written["plugin"])
	}
	if mcp, _ := written["mcp"].(map[string]any); len(mcp) != 0 {
		t.Errorf("MCPs kept: %v", written["mcp"])
	}
	if providers, _ := written["provider"].(map[string]any); len(providers) != 1 || providers["ollama"] == nil {
		t.Errorf("providers: %v", written["provider"])
	}
	if enabled, _ := written["enabled_providers"].([]any); len(enabled) != 1 || enabled[0] != "ollama" {
		t.Errorf("enabled_providers: %v", written["enabled_providers"])
	}
	if written["permission"] == nil {
		t.Error("allowed settings dropped")
	}

	if _, err := os.Stat(filepath.Join(dir, "config", "opencode", "skill", "notes", "SKILL.md")); err != nil {
		t.Errorf("skills not linked: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "config", "opencode", "plugins")); err == nil {
		t.Error("the plugins directory was linked")
	}

	assertNoSecretOnDisk(t, dir, "go-key", "openai-token")

	env := map[string]string{}
	for _, variable := range store.env {
		env[variable.Name] = variable.Value
	}
	if env["XDG_DATA_HOME"] != filepath.Join(dir, "data") || env["TMPDIR"] != filepath.Join(dir, "tmp") ||
		env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
		t.Errorf("environment: %v", env)
	}
	if value, ok := env["OPENCODE_CONFIG_CONTENT"]; !ok || value != "" {
		t.Error("an inherited OPENCODE_CONFIG_CONTENT is not cleared")
	}
	if _, ok := env[secretKeyVariable]; ok {
		t.Error("a key passed for a provider that has none")
	}
}

// assertNoSecretOnDisk fails when any file in the store holds one of the
// values, following no links.
func assertNoSecretOnDisk(t *testing.T, dir string, values ...string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		raw, _ := os.ReadFile(path)
		for _, value := range values {
			if strings.Contains(string(raw), value) {
				t.Errorf("%s holds %q", path, value)
			}
		}
		return nil
	})
}

func secretEnv(store secretStore) map[string]string {
	env := map[string]string{}
	for _, variable := range store.env {
		env[variable.Name] = variable.Value
	}
	return env
}

// writeCredentialTable is OpenCode 2's credential store, which it reads
// instead of auth.json once that has been imported.
func writeCredentialTable(t *testing.T, home string, rows map[string]string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, ".local", "share", "opencode", "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table credential (id text primary key, integration_id text, label text not null,
		value text not null, connector_id text, method_id text, active integer, time_created integer not null, time_updated integer not null)`); err != nil {
		t.Fatal(err)
	}
	for provider, value := range rows {
		if _, err := db.Exec(`insert into credential values (?, ?, 'API key', ?, null, null, null, 1, 1)`, "cred_"+provider, provider, value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrepareSecretStorePassesTheKeyInTheEnvironment(t *testing.T) {
	secretHome(t)
	model := secretByID(SecretModels(Config{}, secretNow))["opencode-go/glm-5.3"]
	dir := t.TempDir()
	store, err := prepareSecretStore(dir, model)
	if err != nil {
		t.Fatal(err)
	}
	if got := secretEnv(store)[secretKeyVariable]; got != "go-key" {
		t.Errorf("key in the environment: %q", got)
	}
	written := readOpencodeJSON(filepath.Join(dir, "config", "opencode", "opencode.json"))
	provider, _ := written["provider"].(map[string]any)
	entry, _ := provider["opencode-go"].(map[string]any)
	options, _ := entry["options"].(map[string]any)
	if options["apiKey"] != "{env:"+secretKeyVariable+"}" {
		t.Errorf("provider entry: %v", written["provider"])
	}
	assertNoSecretOnDisk(t, dir, "go-key", "openai-token")
}

// OpenCode 2 reads credentials from its database; auth.json is what it
// imported once and may be stale.
func TestSecretCredentialsFromOpencode2Database(t *testing.T) {
	home := secretHome(t)
	writeCredentialTable(t, home, map[string]string{"opencode-go": `{"type": "key", "key": "db-key"}`})
	model := secretByID(SecretModels(Config{}, secretNow))["opencode-go/glm-5.3"]
	store, err := prepareSecretStore(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	if got := secretEnv(store)[secretKeyVariable]; got != "db-key" {
		t.Errorf("key in the environment: %q, want the database's", got)
	}

	// Signed in only in the database still counts as signed in.
	writeTestFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{}`)
	if _, ok := secretByID(SecretModels(Config{}, secretNow))["opencode-go/glm-5.3"]; !ok {
		t.Error("OpenCode Go not offered with a database credential")
	}
}

func TestSecretStoreRefusesOAuthProviders(t *testing.T) {
	secretHome(t)
	model := SecretModel{ID: "openai/some-model", Tier: SecretZDR}
	if _, err := prepareSecretStore(t.TempDir(), model); err == nil || !strings.Contains(err.Error(), "only pass on an API key") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenSecretDryRun(t *testing.T) {
	secretHome(t)
	t.Setenv("AGB_DRY_RUN", "1")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	var out bytes.Buffer
	dryRunOut = &out
	defer func() { dryRunOut = os.Stdout }()

	model := secretByID(SecretModels(Config{}, secretNow))["opencode-go/glm-5.3"]
	if code, err := OpenSecret("", model, OpenOptions{SkipPermissions: true}); err != nil || code != 0 {
		t.Fatalf("code %d, err %v", code, err)
	}
	for _, want := range []string{"opencode --standalone --auto", "XDG_DATA_HOME=<secret store>/data", "opencode-go/glm-5.3", "deleted on exit"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run lacks %q:\n%s", want, out.String())
		}
	}
	if entries, _ := os.ReadDir(os.Getenv("XDG_RUNTIME_DIR")); len(entries) != 0 {
		t.Errorf("a dry run made a store: %v", entries)
	}

	retained := secretByID(SecretModels(Config{}, secretNow))["opencode-go/grok-4.7"]
	if _, err := OpenSecret("", retained, OpenOptions{}); err == nil {
		t.Error("started on a model that keeps data")
	}
}

func TestSweepSecretStores(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, secretPrefix+"live")
	dead := filepath.Join(root, secretPrefix+"dead")
	broken := filepath.Join(root, secretPrefix+"nopid")
	orphan := filepath.Join(root, secretPrefix+"orphan")
	// agb was killed but OpenCode still runs.
	writeTestFile(t, filepath.Join(orphan, "pid"), "2147483646 "+strconv.Itoa(os.Getpid()))
	other := filepath.Join(root, "something-else")
	writeTestFile(t, filepath.Join(live, "pid"), strconv.Itoa(os.Getpid()))
	// Above any real pid_max, so never running.
	writeTestFile(t, filepath.Join(dead, "pid"), "2147483646")
	for _, dir := range []string{broken, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sweepSecretStores(root)
	for dir, want := range map[string]bool{live: true, dead: false, broken: false, other: true, orphan: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
}

func secretPicker(t *testing.T, config Config) *picker {
	t.Helper()
	p := limitsPicker()
	p.clock = func() time.Time { return secretNow }
	p.options.Config = config
	return p
}

func TestNewConversationSecretMode(t *testing.T) {
	secretHome(t)
	p := secretPicker(t, Config{})
	p.openNew("/tmp/project")
	press(t, p, Key{Ctrl: true, Name: "s"})
	dialog := p.newConversation
	if !dialog.secret || dialog.directory != "" {
		t.Fatalf("Ctrl+S: secret=%v directory=%q", dialog.secret, dialog.directory)
	}

	frame := plain(p.frame(160, 40))
	for _, want := range []string{"SECRET", "LOCAL", "ollama/gpt-oss:20b", "ZDR", "opencode-go/glm-5.3",
		"Not offered", "kept 30 days: opencode-go/grok-4.7", "corp/*", "brand-new", "scratch directory", opencodeGoPolicyDate} {
		if !strings.Contains(frame, want) {
			t.Errorf("secret dialog lacks %q:\n%s", want, frame)
		}
	}

	// Tab cycles the private models only.
	pickable := len(dialog.pickableModels())
	for i := 0; i < pickable+1; i++ {
		press(t, p, Key{Name: "tab"})
		if model := dialog.pickableModels()[dialog.model]; !model.Tier.Pickable() {
			t.Fatalf("tab landed on %+v", model)
		}
	}
	press(t, p, Key{Name: "backtab"})

	result := press(t, p, Key{Name: "enter"})
	if result == nil || result.Secret == "" || result.Session.Directory != "" || result.Target.Tool != SourceOpencode || result.Mode != ModeNew {
		t.Fatalf("Enter gave %+v", result)
	}
}

func TestNewConversationSecretOffRestoresDirectory(t *testing.T) {
	secretHome(t)
	p := secretPicker(t, Config{})
	p.openNew("/tmp/project")
	press(t, p, Key{Ctrl: true, Name: "s"}, Key{Ctrl: true, Name: "s"})
	if p.newConversation.secret || p.newConversation.directory != "/tmp/project" {
		t.Fatalf("secret=%v directory=%q", p.newConversation.secret, p.newConversation.directory)
	}
}

func TestNewConversationSecretStartsOnConfiguredDefault(t *testing.T) {
	secretHome(t)
	p := secretPicker(t, Config{Secret: SecretConfig{Default: "opencode-go/glm-5.3"}})
	p.openNew("/tmp/project")
	press(t, p, Key{Ctrl: true, Name: "s"})
	result := press(t, p, Key{Name: "enter"})
	if result == nil || result.Secret != "opencode-go/glm-5.3" {
		t.Fatalf("Enter gave %+v", result)
	}
}

func TestNewConversationSecretWithoutPrivateModels(t *testing.T) {
	home := secretHome(t)
	writeTestFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{}`)
	writeTestFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{}`)
	p := secretPicker(t, Config{})
	p.openNew("/tmp/project")
	press(t, p, Key{Ctrl: true, Name: "s"})
	if result := press(t, p, Key{Name: "enter"}); result != nil {
		t.Fatalf("started without a private model: %+v", result)
	}
	if !strings.Contains(p.newConversation.err, "No model is private enough") {
		t.Errorf("err = %q", p.newConversation.err)
	}
	if frame := plain(p.frame(160, 40)); !strings.Contains(frame, "none: add a local provider") {
		t.Errorf("no hint for an empty list:\n%s", frame)
	}
}

func TestParseNewArgsSecret(t *testing.T) {
	directory, options, err := ParseNewArgs([]string{"--secret", "--model", "opencode-go/glm-5.3", "--dir", "/tmp"})
	if err != nil || !options.Secret || options.SecretModel != "opencode-go/glm-5.3" || directory != "/tmp" {
		t.Fatalf("got %q %+v %v", directory, options, err)
	}
	if _, _, err := ParseNewArgs([]string{"--model", "x/y"}); err == nil {
		t.Error("--model without --secret accepted")
	}
	if _, _, err := ParseNewArgs([]string{"--secret", "--target", "cc1"}); err == nil {
		t.Error("a secret Claude conversation accepted")
	}
	if _, options, err := ParseNewArgs([]string{"--secret", "--target", "oc"}); err != nil || !options.Secret {
		t.Errorf("--target oc refused: %v", err)
	}
}
