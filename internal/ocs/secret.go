package ocs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A secret conversation is an OpenCode session that leaves nothing behind: its
// whole store lives in a fresh directory, in RAM where the system has one, and
// is deleted when OpenCode exits. Keeping nothing locally is only half of it,
// so it is also limited to models whose provider keeps nothing either.

type SecretTier int

const (
	// The endpoint is this machine or a private network address.
	SecretLocal SecretTier = iota
	// The provider documents zero data retention and no training.
	SecretZDR
	// The provider keeps requests for a while or trains on them.
	SecretRetained
	// No policy is known; never assumed private.
	SecretUnknown
)

func (tier SecretTier) Pickable() bool { return tier == SecretLocal || tier == SecretZDR }

func (tier SecretTier) Label() string {
	switch tier {
	case SecretLocal:
		return "LOCAL"
	case SecretZDR:
		return "ZDR"
	case SecretRetained:
		return "KEEPS"
	}
	return "?"
}

var secretTierNames = map[string]SecretTier{
	"local":    SecretLocal,
	"zdr":      SecretZDR,
	"retained": SecretRetained,
	"unknown":  SecretUnknown,
}

type SecretModel struct {
	// provider/model, as OpenCode names it.
	ID   string
	Name string
	Tier SecretTier
	// Why it has that tier: "0 days, no training", "endpoint 127.0.0.1".
	Note string
	// The last day its zero-retention agreement is known to hold, when it
	// has one; such models come after the ones without.
	Until string
}

func (model SecretModel) Provider() string {
	provider, _, _ := strings.Cut(model.ID, "/")
	return provider
}

type SecretConfig struct {
	// Per model ("provider/model") or per provider ("provider/*"), a tier
	// that replaces what agb would otherwise decide.
	Models map[string]SecretTier
	// The model the dialog starts on, when it is pickable.
	Default string
}

func parseSecretConfig(raw any) SecretConfig {
	var config SecretConfig
	fields, _ := raw.(map[string]any)
	if value, ok := fields["default"].(string); ok {
		config.Default = strings.TrimSpace(value)
	}
	models, _ := fields["models"].(map[string]any)
	for id, value := range models {
		name, _ := value.(string)
		tier, ok := secretTierNames[strings.ToLower(strings.TrimSpace(name))]
		if !ok || strings.TrimSpace(id) == "" {
			continue
		}
		if config.Models == nil {
			config.Models = map[string]SecretTier{}
		}
		config.Models[strings.TrimSpace(id)] = tier
	}
	return config
}

type secretPolicy struct {
	tier SecretTier
	note string
	// The last day a zero-retention agreement is known to hold; after it the
	// model is unknown again.
	until string
}

// opencodeGoPolicyDate is when opencodeGoPolicy was copied from
// https://opencode.ai/docs/go/#privacy. There is no API for it, so it is a
// snapshot: a model missing from it is unknown, never assumed private, and the
// secret.models config overrides it.
const opencodeGoPolicyDate = "2026-10-29"

const opencodeGoPolicySource = "opencode.ai/docs/go"

var opencodeGoPolicy = func() map[string]secretPolicy {
	policy := map[string]secretPolicy{}
	for _, id := range []string{
		"gemini-4-argon", "glm-5.3-flash", "glm-5.3", "glm-5.2", "kimi-k3.1-pro", "kimi-k3",
		"kimi-k2.7-code", "longcat-2.0", "mimo-v2.6-pro", "mimo-v2.6-flash", "mimo-v2.5-pro",
		"mimo-v2.5", "qwen3.8-max", "qwen3.8-flash", "qwen3.7-plus", "minimax-m3.1-flash",
		"minimax-m3", "hy4-preview", "hy3", "step-5-preview",
	} {
		policy[id] = secretPolicy{tier: SecretZDR, note: "0 days, no training"}
	}
	// "ZDR agreement is renewed monthly"; the published one runs to this day.
	for _, id := range []string{"deepseek-v4.1-flash", "deepseek-v4-pro"} {
		policy[id] = secretPolicy{tier: SecretZDR, note: "0 days, no training", until: "2026-10-31"}
	}
	for _, id := range []string{"grok-4.7", "gpt-6-luna", "gpt-5.6-luna", "claude-haiku-5-5", "beam-free"} {
		policy[id] = secretPolicy{tier: SecretRetained, note: "kept 30 days"}
	}
	for _, id := range []string{"muse-spark-1.3-contributor", "muse-spark-1.2-contributor", "iceman-contributor-free"} {
		policy[id] = secretPolicy{tier: SecretRetained, note: "trains on your prompts"}
	}
	return policy
}()

func (policy secretPolicy) at(now time.Time) (SecretTier, string) {
	if policy.until != "" && now.Format("2006-01-02") > policy.until {
		return SecretUnknown, "ZDR agreement ended " + policy.until
	}
	note := policy.note
	if policy.until != "" {
		note += " until " + policy.until
	}
	return policy.tier, note
}

// localEndpoint names the host of a base URL that never leaves this machine or
// its private network. Only literal addresses and localhost count: a name that
// happens to resolve to a private address today is not a promise.
func localEndpoint(baseURL string) (string, bool) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	host := parsed.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return host, true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host, false
	}
	return host, ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// opencodeConfigDir is where the user's own OpenCode reads its global config.
func opencodeConfigDir() string {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		root = filepath.Join(homeDir(), ".config")
	}
	return filepath.Join(root, "opencode")
}

func opencodeDataDir() string {
	root := os.Getenv("XDG_DATA_HOME")
	if root == "" {
		root = filepath.Join(homeDir(), ".local", "share")
	}
	return filepath.Join(root, "opencode")
}

func opencodeCacheDir() string {
	root := os.Getenv("XDG_CACHE_HOME")
	if root == "" {
		root = filepath.Join(homeDir(), ".cache")
	}
	return filepath.Join(root, "opencode")
}

// readOpencodeJSON reads one of OpenCode's JSON or JSONC files; a missing or
// unreadable file is an empty object.
func readOpencodeJSON(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	clean, err := normalizedJSON(raw)
	if err != nil {
		return map[string]any{}
	}
	var parsed map[string]any
	if json.Unmarshal(clean, &parsed) != nil || parsed == nil {
		return map[string]any{}
	}
	return parsed
}

// opencodeCredential is the user's saved credential for a provider, such as
// {"type": "key", "key": "…"}. OpenCode 2 keeps them in the credential table
// of its database and reads auth.json only to import it once; older stores
// have only auth.json.
func opencodeCredential(provider string) (map[string]any, bool) {
	if db, err := openOpencodeDB(filepath.Join(opencodeDataDir(), "opencode.db")); err == nil {
		defer db.Close()
		var value string
		err := db.QueryRow(`select value from credential where integration_id = ? and (active is null or active != 0)
			order by time_updated desc limit 1`, provider).Scan(&value)
		var parsed map[string]any
		if err == nil && json.Unmarshal([]byte(value), &parsed) == nil {
			return parsed, true
		}
	}
	entry, ok := readOpencodeJSON(filepath.Join(opencodeDataDir(), "auth.json"))[provider].(map[string]any)
	return entry, ok
}

func userOpencodeConfig() map[string]any {
	dir := opencodeConfigDir()
	for _, name := range []string{"opencode.jsonc", "opencode.json"} {
		if fileExists(filepath.Join(dir, name)) {
			return readOpencodeJSON(filepath.Join(dir, name))
		}
	}
	return map[string]any{}
}

// SecretModels lists every model a secret conversation could use, pickable ones
// first: local endpoints, then zero-retention ones, then the rest so the
// dialog can say why they are not offered.
func SecretModels(config Config, now time.Time) []SecretModel {
	opencode := userOpencodeConfig()
	catalog := readOpencodeJSON(filepath.Join(opencodeCacheDir(), "models.json"))

	var models []SecretModel
	seen := map[string]bool{}
	add := func(model SecretModel) {
		if seen[model.ID] {
			return
		}
		seen[model.ID] = true
		if tier, ok := secretOverride(config.Secret, model.ID); ok {
			model.Tier, model.Note = tier, "set in agb config"
		}
		models = append(models, model)
	}

	// Providers configured by hand: an endpoint of its own, local or not.
	providers, _ := opencode["provider"].(map[string]any)
	for _, provider := range sortedKeys(providers) {
		fields, _ := providers[provider].(map[string]any)
		options, _ := fields["options"].(map[string]any)
		baseURL, _ := options["baseURL"].(string)
		if baseURL == "" {
			continue
		}
		host, local := localEndpoint(baseURL)
		tier, note := SecretUnknown, "no known policy for "+host
		if local {
			tier, note = SecretLocal, "endpoint "+host
		}
		entries, _ := fields["models"].(map[string]any)
		for _, id := range sortedKeys(entries) {
			name := id
			if entry, ok := entries[id].(map[string]any); ok {
				if value, ok := entry["name"].(string); ok && value != "" {
					name = value
				}
			}
			add(SecretModel{ID: provider + "/" + id, Name: name, Tier: tier, Note: note})
		}
	}

	// OpenCode Go, when signed in: the catalog's models, each with its row
	// from the published privacy table.
	if _, signedIn := opencodeCredential("opencode-go"); signedIn || os.Getenv("OPENCODE_API_KEY") != "" {
		ids := map[string]string{}
		if provider, ok := catalog["opencode-go"].(map[string]any); ok {
			entries, _ := provider["models"].(map[string]any)
			for id, entry := range entries {
				fields, _ := entry.(map[string]any)
				name, _ := fields["name"].(string)
				ids[id] = name
			}
		}
		if len(ids) == 0 {
			for id := range opencodeGoPolicy {
				ids[id] = ""
			}
		}
		for _, id := range sortedKeys(ids) {
			model := SecretModel{ID: "opencode-go/" + id, Name: ids[id], Tier: SecretUnknown, Note: "not in the " + opencodeGoPolicyDate + " privacy table"}
			if policy, ok := opencodeGoPolicy[id]; ok {
				model.Tier, model.Note = policy.at(now)
				if model.Tier == SecretZDR {
					model.Until = policy.until
				}
			}
			if model.Name == "" {
				model.Name = id
			}
			add(model)
		}
	}

	// A model named only in the config is still offered: an override is the
	// user vouching for it.
	for id, tier := range config.Secret.Models {
		if !strings.HasSuffix(id, "/*") && strings.Contains(id, "/") {
			add(SecretModel{ID: id, Name: id, Tier: tier})
		}
	}

	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Tier != models[j].Tier {
			return models[i].Tier < models[j].Tier
		}
		return models[i].Until == "" && models[j].Until != ""
	})
	return models
}

func secretOverride(config SecretConfig, id string) (SecretTier, bool) {
	if tier, ok := config.Models[id]; ok {
		return tier, true
	}
	provider, _, _ := strings.Cut(id, "/")
	tier, ok := config.Models[provider+"/*"]
	return tier, ok
}

// FindSecretModel returns id only when a secret conversation may use it.
func FindSecretModel(models []SecretModel, id string) (SecretModel, error) {
	for _, model := range models {
		if model.ID != id {
			continue
		}
		if !model.Tier.Pickable() {
			return model, fmt.Errorf("%s is not private enough for a secret conversation: %s", id, model.Note)
		}
		return model, nil
	}
	return SecretModel{}, fmt.Errorf("No model %s for a secret conversation; agb new --secret lists them", id)
}

// The keys of the user's OpenCode config a secret conversation keeps: how the
// agent behaves and looks. Everything else, plugins and MCPs first, is left
// out, so a key OpenCode adds later is excluded until it is known to be safe.
var secretConfigKeys = []string{"$schema", "agent", "command", "permission", "instructions", "theme", "keybinds", "formatter", "lsp", "tools", "username", "compaction"}

// And the files and directories beside it, linked rather than copied.
var secretConfigFiles = []string{"AGENTS.md", "agent", "agents", "command", "commands", "skill", "skills", "themes", "tui.json", "tui.jsonc"}

// secretRoot is where secret stores are made: the per-user runtime directory,
// a RAM filesystem on Linux, or the temporary directory where there is none.
func secretRoot() (root string, inRAM bool) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, true
		}
	}
	return os.TempDir(), false
}

const secretPrefix = "agb-secret-"

// sweepSecretStores deletes stores whose agb and OpenCode are both gone, after
// a crash or a kill -9 that skipped the cleanup. OpenCode outlives a killed
// agb, so agb's pid alone would delete a store still in use.
func sweepSecretStores(root string) {
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), secretPrefix) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		raw, _ := os.ReadFile(filepath.Join(dir, "pid"))
		alive := false
		for _, field := range strings.Fields(string(raw)) {
			if pid, err := strconv.Atoi(field); err == nil && pid > 0 && syscall.Kill(pid, 0) == nil {
				alive = true
			}
		}
		if !alive {
			_ = os.RemoveAll(dir)
		}
	}
}

type secretStore struct {
	dir string
	// Where OpenCode starts when no directory was chosen.
	scratch string
	env     []envOverride
}

// secretKeyVariable carries the provider's API key to OpenCode. The store's
// database starts empty, so OpenCode has no credential of its own; the config
// names this variable as the provider's apiKey, and the key is never written
// into the store.
const secretKeyVariable = "AGB_SECRET_API_KEY"

// prepareSecretStore lays out an OpenCode home in dir: a config made from the
// allowed part of the user's, the model's provider's API key in the
// environment, and empty data, state and cache.
func prepareSecretStore(dir string, model SecretModel) (secretStore, error) {
	store := secretStore{dir: dir, scratch: filepath.Join(dir, "scratch")}
	config := filepath.Join(dir, "config", "opencode")
	data := filepath.Join(dir, "data", "opencode")
	for _, path := range []string{config, data, filepath.Join(dir, "state"), filepath.Join(dir, "cache"), filepath.Join(dir, "tmp"), store.scratch} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return store, err
		}
	}

	user := userOpencodeConfig()
	settings := map[string]any{}
	for _, key := range secretConfigKeys {
		if value, ok := user[key]; ok {
			settings[key] = value
		}
	}
	provider := model.Provider()
	entry := map[string]any{}
	if providers, ok := user["provider"].(map[string]any); ok {
		if fields, ok := providers[provider].(map[string]any); ok {
			entry = fields
		}
	}
	if credential, ok := opencodeCredential(provider); ok {
		// auth.json says "api", OpenCode 2's credential table "key".
		key, _ := credential["key"].(string)
		if kind := credential["type"]; kind != "api" && kind != "key" || key == "" {
			return store, fmt.Errorf("%s is signed in with %v; a secret conversation can only pass on an API key", provider, credential["type"])
		}
		options := map[string]any{}
		if fields, ok := entry["options"].(map[string]any); ok {
			for name, value := range fields {
				options[name] = value
			}
		}
		options["apiKey"] = "{env:" + secretKeyVariable + "}"
		copied := map[string]any{}
		for name, value := range entry {
			copied[name] = value
		}
		copied["options"] = options
		entry = copied
		store.env = append(store.env, envOverride{secretKeyVariable, key})
	}
	if len(entry) > 0 {
		settings["provider"] = map[string]any{provider: entry}
	}
	settings["model"] = model.ID
	settings["small_model"] = model.ID
	settings["enabled_providers"] = []string{provider}
	settings["share"] = "disabled"
	settings["autoupdate"] = false
	settings["plugin"] = []string{}
	settings["mcp"] = map[string]any{}
	if err := writeJSONFile(filepath.Join(config, "opencode.json"), settings); err != nil {
		return store, err
	}
	for _, name := range secretConfigFiles {
		source := filepath.Join(opencodeConfigDir(), name)
		if _, err := os.Stat(source); err == nil {
			if err := os.Symlink(source, filepath.Join(config, name)); err != nil {
				return store, err
			}
		}
	}

	store.env = append(secretEnvironment(dir), store.env...)
	return store, nil
}

func secretEnvironment(dir string) []envOverride {
	env := []envOverride{
		{"XDG_CONFIG_HOME", filepath.Join(dir, "config")},
		{"XDG_DATA_HOME", filepath.Join(dir, "data")},
		{"XDG_STATE_HOME", filepath.Join(dir, "state")},
		{"XDG_CACHE_HOME", filepath.Join(dir, "cache")},
		{"TMPDIR", filepath.Join(dir, "tmp")},
		// A project's own opencode.json or .opencode/ could bring plugins
		// and MCPs back.
		{"OPENCODE_DISABLE_PROJECT_CONFIG", "1"},
		{"OPENCODE_DISABLE_AUTOUPDATE", "1"},
		// Inherited config would be merged over the secret one.
		{"OPENCODE_CONFIG", ""},
		{"OPENCODE_CONFIG_DIR", ""},
		{"OPENCODE_CONFIG_CONTENT", ""},
	}
	// The model catalog is read from the user's cache instead of fetched
	// into the new one.
	if catalog := filepath.Join(opencodeCacheDir(), "models.json"); fileExists(catalog) {
		env = append(env, envOverride{"OPENCODE_MODELS_PATH", catalog}, envOverride{"OPENCODE_DISABLE_MODELS_FETCH", "1"})
	}
	return env
}

func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// OpenSecret runs OpenCode in a throwaway store and deletes it when OpenCode
// exits. Unlike every other launch, agb stays as its parent to do that. An
// empty directory starts in the store's own scratch directory: no project, no
// AGENTS.md, nothing tying the conversation to a repository.
func OpenSecret(directory string, model SecretModel, opts OpenOptions) (int, error) {
	if !model.Tier.Pickable() {
		return 1, fmt.Errorf("%s is not private enough for a secret conversation: %s", model.ID, model.Note)
	}
	if directory != "" {
		if err := AssertDirectory(directory); err != nil {
			return 1, err
		}
	}
	command := opencodeCommand(nil)
	args := []string{"--standalone"}
	if opts.SkipPermissions {
		args = append(args, "--auto")
	}

	if dryRunEnabled() {
		where := directory
		if where == "" {
			where = "<secret store>/scratch"
		}
		fmt.Fprintf(dryRunOut, "\x1b[36mwould run\x1b[0m %s\n", RenderCommand(command, append(args, where), where, secretEnvironment("<secret store>")))
		fmt.Fprintf(dryRunOut, "  secret: %s (%s %s), store deleted on exit\n", model.ID, model.Tier.Label(), model.Note)
		return 0, nil
	}

	binary, err := exec.LookPath(command)
	if err != nil {
		return 127, fmt.Errorf("%s not found on PATH", command)
	}
	root, inRAM := secretRoot()
	sweepSecretStores(root)
	dir, err := os.MkdirTemp(root, secretPrefix)
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return 1, err
	}
	store, err := prepareSecretStore(dir, model)
	if err != nil {
		return 1, err
	}
	if directory == "" {
		directory = store.scratch
	}
	if !inRAM {
		fmt.Fprintf(os.Stderr, "No RAM directory here; the secret store is on disk in %s until OpenCode exits.\n", dir)
	}

	child := exec.Command(binary, append(args, directory)...)
	child.Dir = directory
	child.Env = childEnvironment(store.env)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Ctrl+C reaches OpenCode from the terminal; agb must outlive it to clean
	// up, and passes on the signals that come only to itself.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := child.Start(); err != nil {
		return 1, fmt.Errorf("could not start %s: %w", command, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(fmt.Sprintf("%d %d\n", os.Getpid(), child.Process.Pid)), 0o600)
	go func() {
		for sig := range signals {
			if sig == syscall.SIGTERM || sig == syscall.SIGHUP {
				_ = child.Process.Signal(sig)
			}
		}
	}()
	err = child.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// -1: killed by a signal rather than exiting.
		return max(exit.ExitCode(), 1), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}
