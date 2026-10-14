package ocs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeAccountsConfigAndRoutes(t *testing.T) {
	config, err := ParseConfig([]byte(`{"opencodeAccounts":[
		{"name":"opencode-i2cat","command":"opencode-i2cat","dbPath":"~/i2cat/opencode.db","configHome":"~/i2cat/config","skipPermissions":true},
		{"name":"oc","command":"bad","dbPath":"bad"},
		{"name":"missing-store","command":"bad"}
	]}`))
	if err != nil || len(config.OpencodeAccounts) != 1 {
		t.Fatalf("config: %+v, %v", config, err)
	}
	account := config.OpencodeAccounts[0]
	if !strings.Contains(destinationName(&account), "OPENCODE-I2CAT") {
		t.Fatal("wrapper is indistinguishable from default OpenCode in dialogs")
	}
	if config.Targets()[1] != account || account.DBPath != filepath.Join(homeDir(), "i2cat/opencode.db") || !config.DefaultSkipPermissions(nil, account) {
		t.Fatalf("wrapper target lost its settings: %+v", account)
	}
	session := opencodeTestSession()
	if IsNativeTarget(&session, &account) {
		t.Fatal("default store session would resume in isolated store")
	}
	session.Account = &account
	if !IsNativeTarget(&session, &account) || IsNativeTarget(&session, &config.Opencode) {
		t.Fatal("isolated store ownership is incorrect")
	}
	dest, err := setupDestination(account, SetupDestination{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if dest.MCPConfig != filepath.Join(account.ConfigHome, "opencode/opencode.json") {
		t.Fatalf("setup uses another account's config: %+v", dest)
	}
}

func TestOpencodeStoresAndCacheStaySeparate(t *testing.T) {
	defaultPath, defaultDB := opencodeStore(t, false)
	i2catPath, i2catDB := opencodeStore(t, false)
	t.Setenv("OPENCODE_DB_PATH", defaultPath)
	// Matching IDs and timestamps must not share cached messages.
	for _, store := range []struct{ path, text string }{{defaultPath, "personal prompt"}, {i2catPath, "work prompt"}} {
		db := defaultDB
		if store.path == i2catPath {
			db = i2catDB
		}
		mustExec(t, db, `insert into session_v2 values ('same-id', 'p', '/tmp/project', 'title', 100, null)`)
		mustExec(t, db, `insert into session_message values ('m', 'same-id', 'user', 1, 100, ?)`, fmt.Sprintf(`{"text":%q}`, store.text))
	}
	config, _ := ParseConfig([]byte("{}"))
	config.OpencodeAccounts = []Account{{Tool: SourceOpencode, Name: "opencode-i2cat", Command: "opencode-i2cat", DBPath: i2catPath}}
	config.ClaudeAccounts, config.CodexAccounts = nil, nil
	cachePath := filepath.Join(t.TempDir(), "index.gob")
	for pass := 0; pass < 2; pass++ {
		cache := OpenCache(cachePath, false)
		sessions := cache.AllSessions(config, ScopeUser)
		if len(sessions) != 2 {
			t.Fatalf("expected both stores: %+v", sessions)
		}
		for _, session := range sessions {
			want := "personal prompt"
			if session.Account.Name == "opencode-i2cat" {
				want = "work prompt"
			}
			if !strings.Contains(session.SearchText, want) {
				t.Fatalf("wrong store content: %+v", session)
			}
		}
		if err := cache.Save(); err != nil {
			t.Fatal(err)
		}
		if len(cache.next.OpencodeStores) != 2 {
			t.Fatal("cache lost an OpenCode store")
		}
	}
}

func TestOpencodeWrapperLaunchAndExport(t *testing.T) {
	// The default database is v2, but this wrapper has an independent v1 store.
	defaultPath, _ := opencodeStore(t, false)
	t.Setenv("OPENCODE_DB_PATH", defaultPath)
	account := Account{Tool: SourceOpencode, Name: "opencode-i2cat", Command: filepath.Join(t.TempDir(), "opencode-i2cat"), DBPath: filepath.Join(t.TempDir(), "missing.db")}
	dir := t.TempDir()
	for _, c := range []struct {
		run  func() (int, error)
		want string
	}{
		{func() (int, error) { return OpenNew(dir, account, OpenOptions{}) }, account.Command},
		{func() (int, error) {
			return OpenWith(Session{ID: "sid", Source: SourceOpencode, Account: &account, Directory: dir}, account, OpenOptions{Fork: true})
		}, "--session sid --fork"},
		{func() (int, error) { return OpenOpencodeFresh(dir, "seed", OpenOptions{Account: &account}) }, "run --dir"},
	} {
		output := dryRun(t, c.run)
		if !strings.Contains(output, account.Command) || !strings.Contains(output, c.want) {
			t.Fatalf("wrong wrapper launch: %s", output)
		}
	}
	// Only the wrapper can export this conversation; choosing the default v2
	// command or 'session export' fails this fixture.
	if err := os.WriteFile(account.Command, []byte("#!/bin/sh\n[ \"$1\" = export ] && [ \"$2\" = sid ] || exit 1\nprintf '%s' '{\"info\":{\"directory\":\"/tmp/project\"},\"messages\":[{\"info\":{\"role\":\"user\"},\"parts\":[{\"type\":\"text\",\"text\":\"work context\"}]}]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	transcript, err := opencodeAccountTranscript("sid", &account)
	if err != nil || len(transcript.Turns) != 1 || transcript.Turns[0].Text != "work context" {
		t.Fatalf("wrong export: %+v, %v", transcript, err)
	}
}
