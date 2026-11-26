package ocs

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Steps as agy 1.5.18 writes them: the prompt wrapped in <USER_REQUEST> with
// metadata after it, a tool-only planner step, tool output as GENERIC, then the
// reply.
func antigravityFixture(t *testing.T) string {
	return jsonl(t,
		obj{"step_index": 0, "source": "USER_EXPLICIT", "type": "USER_INPUT", "status": "DONE",
			"content": "<USER_REQUEST>\nCount the files\nin this repo\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-11-26T12:55:16+01:00.\n</ADDITIONAL_METADATA>"},
		obj{"step_index": 1, "source": "MODEL", "type": "PLANNER_RESPONSE", "status": "DONE", "model": "gemini-4-neon-low",
			"thinking": "List them.", "tool_calls": []any{obj{"name": "run_command", "args": obj{"CommandLine": "ls"}}}},
		obj{"step_index": 2, "source": "MODEL", "type": "GENERIC", "status": "DONE", "content": "Command output:\nbin\ncmd"},
		obj{"step_index": 3, "source": "MODEL", "type": "PLANNER_RESPONSE", "status": "DONE", "model": "gemini-4-neon-low", "content": "There are 24."},
		obj{"step_index": 4, "source": "USER_EXPLICIT", "type": "USER_INPUT", "status": "DONE",
			"content": "<USER_REQUEST>\nAnd hidden ones?\n</USER_REQUEST>"},
	)
}

const antigravityID = "eac18b4c-a710-4142-997a-85ecf9aac21d"

// antigravityStore lays out a store under home: one transcript and the
// summaries index with agy's own schema (trimmed to the columns agb reads,
// plus one it does not).
func antigravityStore(t *testing.T, home string, title, workspaces string) string {
	t.Helper()
	transcript := antigravityTranscriptPath(&Account{Home: home}, antigravityID)
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(antigravityFixture(t)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"create table conversation_summaries (conversation_id text primary key, title text not null default '', workspace_uris text not null, app_data_dir text not null default '')",
		"insert into conversation_summaries values ('" + antigravityID + "', '" + title + "', '" + workspaces + "', 'antigravity-cli')",
		// An Antigravity IDE conversation: indexed, but with no transcript.
		"insert into conversation_summaries values ('4621da49-cd73-43b7-9d1a-b2aa63dd8465', 'Old IDE chat', '[]', 'antigravity')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return transcript
}

func TestAntigravityReaderKeepsOnlyTheConversation(t *testing.T) {
	rec, err := parseAntigravity(strings.NewReader(antigravityFixture(t)))
	if err != nil || rec == nil {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if want := []string{"Count the files in this repo", "And hidden ones?"}; !reflect.DeepEqual(rec.User, want) {
		t.Fatalf("prompts = %q", rec.User)
	}
	if want := []string{"There are 24."}; !reflect.DeepEqual(rec.Assistant, want) {
		t.Fatalf("replies = %q", rec.Assistant)
	}

	onlyTools := jsonl(t, obj{"source": "MODEL", "type": "PLANNER_RESPONSE", "content": "hi"})
	if rec, _ := parseAntigravity(strings.NewReader(onlyTools)); rec != nil {
		t.Fatal("a conversation with no prompt should be skipped")
	}
}

func TestAntigravitySessionsJoinTheSummaryIndex(t *testing.T) {
	home := t.TempDir()
	transcript := antigravityStore(t, home, "Count Repo Files", `["file:///home/dev/agent-bridge","file:///home/dev/other"]`)
	account := &Account{Tool: SourceAntigravity, Name: "ag", Home: home}

	cache := OpenCache(filepath.Join(t.TempDir(), "index.gob"), true)
	sessions := cache.antigravitySessions(account, ScopeUser)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	session := sessions[0]
	if session.ID != antigravityID || session.Source != SourceAntigravity || session.FilePath != transcript {
		t.Fatalf("id/source/file = %s/%s/%s", session.ID, session.Source, session.FilePath)
	}
	if session.Title != "Count Repo Files" || session.Directory != "/home/dev/agent-bridge" {
		t.Fatalf("title/dir = %q/%q", session.Title, session.Directory)
	}
	if !strings.Contains(session.SearchText, "hidden ones") || strings.Contains(session.SearchText, "There are 24") {
		t.Fatalf("user-scope search text = %q", session.SearchText)
	}
	if all := cache.antigravitySessions(account, ScopeAll); !strings.Contains(all[0].SearchText, "There are 24") {
		t.Fatal("reply missing from the all-scope search text")
	}
}

// A conversation from print mode has no title yet, and one whose index row is
// missing has no directory either; it still lists.
func TestAntigravityFallsBackWithoutTitleOrIndex(t *testing.T) {
	home := t.TempDir()
	antigravityStore(t, home, "", `[]`)
	account := &Account{Tool: SourceAntigravity, Name: "ag", Home: home}
	cache := OpenCache(filepath.Join(t.TempDir(), "index.gob"), true)

	session := cache.antigravitySessions(account, ScopeUser)[0]
	if session.Title != "Count the files in this repo" || session.Directory != "unknown" {
		t.Fatalf("title/dir = %q/%q", session.Title, session.Directory)
	}

	if err := os.Remove(filepath.Join(home, "conversation_summaries.db")); err != nil {
		t.Fatal(err)
	}
	if sessions := cache.antigravitySessions(account, ScopeUser); len(sessions) != 1 || sessions[0].Directory != "unknown" {
		t.Fatalf("without the index: %+v", sessions)
	}
}

func TestAntigravityTranscriptKeepsFormatting(t *testing.T) {
	home := t.TempDir()
	antigravityStore(t, home, "Count Repo Files", `["file:///home/dev/agent-bridge"]`)
	account := &Account{Tool: SourceAntigravity, Name: "ag", Home: home}

	transcript, err := antigravityTranscript(Session{ID: antigravityID, Account: account, Directory: "/home/dev/agent-bridge"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Turn{{RoleUser, "Count the files\nin this repo"}, {RoleAssistant, "There are 24."}, {RoleUser, "And hidden ones?"}}
	if !reflect.DeepEqual(transcript.Turns, want) || transcript.Directory != "/home/dev/agent-bridge" {
		t.Fatalf("transcript = %+v", transcript)
	}
}

func TestAntigravityLaunchRoutes(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	antigravityStore(t, home, "Count Repo Files", `["file://`+dir+`"]`)
	ag := Account{Tool: SourceAntigravity, Name: "ag", Home: home}
	session := Session{ID: antigravityID, Source: SourceAntigravity, Account: &ag, Directory: dir,
		FilePath: antigravityTranscriptPath(&ag, antigravityID)}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AGB_CACHE_PATH", "")
	t.Setenv("OCS_CACHE_PATH", "")

	for _, c := range []struct {
		name string
		run  func() (int, error)
		want string
	}{
		{"resume", func() (int, error) { return OpenWith(session, ag, OpenOptions{}) }, "agy --conversation " + antigravityID + "\n"},
		{"yolo", func() (int, error) { return OpenWith(session, ag, OpenOptions{SkipPermissions: true}) },
			"agy --conversation " + antigravityID + " --dangerously-skip-permissions\n"},
		// agy has no fork flag, so a fork is seeded.
		{"fork", func() (int, error) { return OpenWith(session, ag, OpenOptions{Fork: true}) }, "agy -i <transcript seed"},
		{"new", func() (int, error) { return OpenNew(dir, ag, OpenOptions{SkipPermissions: true}) }, "agy --dangerously-skip-permissions\n"},
		{"out to claude", func() (int, error) { return OpenWith(session, cc1, OpenOptions{}) }, "claude <transcript seed"},
		{"in from codex", func() (int, error) {
			return OpenAntigravityFresh(dir, "carry on", OpenOptions{Account: &ag})
		}, "agy -i \"carry on\"\n"},
	} {
		if output := dryRun(t, c.run); !strings.Contains(output, c.want) {
			t.Fatalf("%s: %q does not contain %q", c.name, output, c.want)
		}
	}
}

func TestAntigravityTargetFollowsConfigAndInstall(t *testing.T) {
	installed := false
	previous := antigravityInstalled
	antigravityInstalled = func() bool { return installed }
	t.Cleanup(func() { antigravityInstalled = previous })
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AGB_CONFIG_PATH", path)

	load := func(raw string) []string {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		return targetNames(LoadConfig())
	}

	if got := load(`{}`); !reflect.DeepEqual(got, []string{"oc", "cc", "cx"}) {
		t.Fatalf("not installed: %v", got)
	}
	installed = true
	if got := load(`{}`); !reflect.DeepEqual(got, []string{"oc", "cc", "cx", "ag"}) {
		t.Fatalf("installed: %v", got)
	}
	if got := load(`{"antigravity": false}`); !reflect.DeepEqual(got, []string{"oc", "cc", "cx"}) {
		t.Fatalf("turned off: %v", got)
	}

	installed = false
	if err := os.WriteFile(path, []byte(`{"antigravity": {"skipPermissions": true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LoadConfig()
	if config.Antigravity == nil || !config.DefaultSkipPermissions(nil, *config.Antigravity) {
		t.Fatalf("configured: %+v", config.Antigravity)
	}
	if next := NextToolTarget(config.Targets(), SourceCodex, nil); next == nil || next.Tool != SourceAntigravity {
		t.Fatalf("tab from codex = %+v", next)
	}
}

// agy's MCP file has its own schema, so a profile that names Antigravity is
// refused up front rather than written somewhere wrong.
func TestSetupRefusesAntigravityTargets(t *testing.T) {
	config, path := setupFixture(t)
	ag := AntigravityAccount
	config.Antigravity = &ag
	profile := config.Setup.Profiles["development"]
	profile.Targets = append(profile.Targets, "ag")
	config.Setup.Profiles["development"] = profile

	_, err := BuildSetupPlan(config, path, "development", "")
	if err == nil || !strings.Contains(err.Error(), "does not manage Antigravity") {
		t.Fatalf("err = %v", err)
	}
}
