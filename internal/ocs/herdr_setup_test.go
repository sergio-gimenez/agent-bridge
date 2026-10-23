package ocs

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrHookInstallPreservesSettingsAndIsRepeatable(t *testing.T) {
	raw := []byte(`{"permissions":{"allow":["Bash(go test:*)"]},"number":9007199254740993,"hooks":{"SessionStart":[{"matcher":"resume","hooks":[{"type":"command","command":"my-own-hook","timeout":42}]}],"Stop":[{"hooks":[{"type":"command","command":"other-stop"}]}]}}`)
	command := herdrHookCommand("/a path/agb", Account{Tool: SourceClaude, Home: "/a path/.claude-cc2"})
	installed, err := editHerdrHooks(raw, command)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"my-own-hook", "other-stop", "9007199254740993", "Bash(go test:*)", "\"matcher\": \"resume\""} {
		if !bytes.Contains(installed, []byte(want)) {
			t.Fatalf("lost setting %s: %s", want, installed)
		}
	}
	again, err := editHerdrHooks(installed, command)
	if err != nil || !bytes.Equal(again, installed) {
		t.Fatalf("repeated install changed settings: %v", err)
	}
	updated, err := editHerdrHooks(installed, herdrHookCommand("/new/agb", Account{Tool: SourceClaude, Home: "/a path/.claude-cc2"}))
	if err != nil || bytes.Contains(updated, []byte("/a path/agb")) || bytes.Count(updated, []byte(herdrHookMarker)) != 2 {
		t.Fatalf("binary upgrade left stale/duplicate commands: %s %v", updated, err)
	}
	removed, err := editHerdrHooks(installed, "")
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	_ = json.Unmarshal(raw, &before)
	_ = json.Unmarshal(removed, &after)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if !bytes.Equal(b, a) {
		t.Fatalf("uninstall lost unrelated settings: %s", removed)
	}
}

func TestHerdrHookInstallRejectsUnknownShapes(t *testing.T) {
	for _, raw := range []string{"null", "[]", `{}`, `{"hooks":null}`, `{"hooks":[]}`, `{"hooks":{"SessionStart":{}}}`, `{"hooks":{"Stop":[{"hooks":false}]}}`, `{"hooks":{"Stop":[{"hooks":[null]}]}}`, `{} {}`} {
		_, err := editHerdrHooks([]byte(raw), "cmd"+herdrHookMarker)
		if raw != `{}` && err == nil {
			t.Fatalf("accepted unknown format: %s", raw)
		}
	}
	raw := []byte("{\n  \"other\": true\n}\n")
	removed, err := editHerdrHooks(raw, "")
	if err != nil || !bytes.Equal(raw, removed) {
		t.Fatal("uninstall changed a file without our hooks")
	}
}

func TestHerdrInstallerBacksUpAndKeepsSymlinks(t *testing.T) {
	root := t.TempDir()
	account := Account{Tool: SourceClaude, Name: "cc2", Home: root}
	settings := herdrSettingsPath(account)
	realSettings := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"enabledPlugins":{"keep":true}}`)
	if err := os.WriteFile(realSettings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSettings, settings); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "herdr")
	stub := "#!/bin/sh\n[ \"$1 $2 $3\" = 'integration install claude' ] || exit 1\n[ \"$CLAUDE_CONFIG_DIR\" = \"$AGB_TEST_HOME\" ] || exit 2\n"
	if err := os.WriteFile(binary, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", binary)
	t.Setenv("AGB_TEST_HOME", root)
	if err := configureHerdrAccounts([]Account{account}, "/bin/agb", true, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Lstat(settings); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced dotfile symlink")
	}
	backups, _ := filepath.Glob(filepath.Join(root, ".agb-herdr-backup-*"))
	if len(backups) != 1 {
		t.Fatalf("missing backup: %v", backups)
	}
	backup := filepath.Join(backups[0], "settings.json")
	if raw, _ := os.ReadFile(backup); !bytes.Equal(raw, original) {
		t.Fatal("backup does not contain original settings")
	}
	if info, _ := os.Stat(backup); info.Mode().Perm() != 0o600 {
		t.Fatal("backup is not private")
	}
	if err := configureHerdrAccounts([]Account{account}, "/bin/agb", false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(realSettings); bytes.Contains(raw, []byte(herdrHookMarker)) || !bytes.Contains(raw, []byte("enabledPlugins")) {
		t.Fatalf("uninstall changed unrelated settings: %s", raw)
	}
}

func TestHerdrInstallerPreflightsAllAccountsAndDryRun(t *testing.T) {
	accounts := []Account{{Tool: SourceClaude, Name: "cc1", Home: t.TempDir()}, {Tool: SourceCodex, Name: "cx2", Home: t.TempDir()}}
	path := herdrSettingsPath(accounts[1])
	if err := os.WriteFile(path, []byte(`{"hooks":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", "/does/not/exist")
	if err := configureHerdrAccounts(accounts, "/bin/agb", true, false, io.Discard); err == nil {
		t.Fatal("accepted malformed later account")
	}
	if entries, _ := os.ReadDir(accounts[0].Home); len(entries) != 0 {
		t.Fatal("changed an earlier account before validating all accounts")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configureHerdrAccounts(accounts, "/bin/agb", true, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(accounts[0].Home); len(entries) != 0 {
		t.Fatal("dry run wrote files or invoked Herdr")
	}
}

func TestHerdrInstallerFailureKeepsRecoveryCopy(t *testing.T) {
	account := Account{Tool: SourceCodex, Name: "cx2", Home: t.TempDir()}
	config := filepath.Join(account.Home, "config.toml")
	if err := os.WriteFile(config, []byte("# existing config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", "/does/not/exist")
	err := configureHerdrAccounts([]Account{account}, "/bin/agb", true, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "backup:") {
		t.Fatalf("missing recovery information: %v", err)
	}
	if raw, _ := os.ReadFile(config); string(raw) != "# existing config\n" {
		t.Fatal("failed install changed config")
	}
	backups, _ := filepath.Glob(filepath.Join(account.Home, ".agb-herdr-backup-*", "config.toml"))
	if len(backups) != 1 {
		t.Fatal("did not back up Codex config")
	}
}
