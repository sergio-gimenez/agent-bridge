package ocs

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// opencodeStore builds a database with only the columns AgentBridge reads:
// OpenCode 2's tables, and the old ones when legacy is set (a migrated store
// keeps both).
func opencodeStore(t *testing.T, legacy bool) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema := `
		create table session_v2 (id text primary key, project_id text, directory text, title text,
			time_updated integer, time_archived integer);
		create table session_message (id text primary key, session_id text, type text, seq integer,
			time_created integer, data text);`
	if legacy {
		schema += `
		create table session (id text primary key, project_id text, directory text, title text,
			time_updated integer, time_archived integer);
		create table message (id text primary key, session_id text, time_created integer, data text);
		create table part (id text primary key, message_id text, session_id text, time_created integer, data text);`
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestOpencodeReadsAMigratedStore(t *testing.T) {
	path, db := opencodeStore(t, true)
	// Migrated, then kept going in OpenCode 2: the newer row wins.
	mustExec(t, db, `insert into session values ('ses_both', 'p', '/w/a', 'Old title', 100, null)`)
	mustExec(t, db, `insert into session_v2 values ('ses_both', 'p', '/w/a', 'New title', 300, null)`)
	// Only in OpenCode 2, and only in the old store.
	mustExec(t, db, `insert into session_v2 values ('ses_new', 'p', '/w/b', 'Fresh', 400, null)`)
	mustExec(t, db, `insert into session values ('ses_old', 'p', '/w/c', 'Legacy', 50, null)`)
	mustExec(t, db, `insert into session_v2 values ('ses_gone', 'p', '/w/d', 'Archived', 500, 1)`)
	// Its first turn failed, so OpenCode never titled it.
	mustExec(t, db, `insert into session_v2 values ('ses_untitled', null, '/w/e', null, 20, null)`)

	mustExec(t, db, `insert into session_message values ('m1', 'ses_new', 'user', 1, 10, '{"text":"first   question"}')`)
	mustExec(t, db, `insert into session_message values ('m2', 'ses_new', 'assistant', 2, 11,
		'{"content":[{"type":"reasoning","text":"thinking"},{"type":"text","text":"the answer"}]}')`)
	mustExec(t, db, `insert into session_message values ('m3', 'ses_new', 'user', 3, 12, '{"text":"follow-up"}')`)
	mustExec(t, db, `insert into session_message values ('m4', 'ses_new', 'synthetic', 4, 13, '{"text":"Continue if you have next steps"}')`)
	mustExec(t, db, `insert into message values ('lm', 'ses_old', 5, '{"role":"user"}')`)
	mustExec(t, db, `insert into part values ('lp', 'lm', 'ses_old', 5, '{"type":"text","text":"legacy prompt"}')`)

	cache := OpenCache(filepath.Join(t.TempDir(), "index.gob"), true)
	sessions, err := cache.OpencodeSessions(path, ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	var ids, titles []string
	for _, session := range sessions {
		ids, titles = append(ids, session.ID), append(titles, session.Title)
	}
	if !reflect.DeepEqual(ids, []string{"ses_new", "ses_both", "ses_old", "ses_untitled"}) || titles[1] != "New title" {
		t.Fatalf("got %v %v", ids, titles)
	}
	if got := sessions[0]; !reflect.DeepEqual(got.Prompts, []string{"first question", "follow-up"}) ||
		!reflect.DeepEqual(got.AssistantSnippet, []string{"the answer"}) {
		t.Fatalf("OpenCode 2 texts read as %q / %q", got.Prompts, got.AssistantSnippet)
	}
	if got := sessions[2].Prompts; !reflect.DeepEqual(got, []string{"legacy prompt"}) {
		t.Fatalf("old-store session read as %q", got)
	}
}

func TestOpencodeV2DetectionFollowsTheStore(t *testing.T) {
	t.Setenv("OPENCODE_DB_PATH", filepath.Join(t.TempDir(), "none.db"))
	if opencodeV2() {
		t.Fatal("no store should read as OpenCode 1")
	}
	path, _ := opencodeStore(t, false)
	t.Setenv("OPENCODE_DB_PATH", path)
	if !opencodeV2() {
		t.Fatal("a session_v2 store is OpenCode 2")
	}
}

func TestOpencodeV2ExportReadsLikeAConversation(t *testing.T) {
	raw := []byte(`{"info":{"id":"ses_x","location":{"directory":"/w/a"}},"messages":[
		{"type":"user","text":"Implement this"},
		{"type":"assistant","content":[{"type":"reasoning","text":"hmm"},{"type":"text","text":"Done."}]},
		{"type":"assistant","content":[],"error":{"type":"provider.internal"}},
		{"type":"compaction","status":"failed","summary":"Bad summary"},
		{"type":"compaction","status":"completed","summary":"## Objective\nShip it."},
		{"type":"system","text":"The available tools have changed."}]}`)
	transcript, err := parseOpencodeTranscript(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []Turn{{Role: RoleUser, Text: "Implement this"}, {Role: RoleAssistant, Text: "Done."}}
	if transcript.Directory != "/w/a" || !reflect.DeepEqual(transcript.Turns, want) {
		t.Fatalf("got %+v", transcript)
	}
	if transcript.Summary != "## Objective\nShip it." || transcript.SummaryAfter != 2 {
		t.Fatalf("summary %q after %d turns", transcript.Summary, transcript.SummaryAfter)
	}
}

func TestOpencodeV2OpensFreshInTheTUIWithAPrompt(t *testing.T) {
	path, _ := opencodeStore(t, false)
	t.Setenv("OPENCODE_DB_PATH", path)
	t.Setenv("AGB_DRY_RUN", "1")
	var out strings.Builder
	restore := dryRunOut
	dryRunOut = &out
	defer func() { dryRunOut = restore }()
	dir := t.TempDir()
	if _, err := OpenOpencodeFresh(dir, "hello", OpenOptions{SkipPermissions: true}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "opencode --auto --prompt hello "+dir) || strings.Contains(got, " run ") {
		t.Fatalf("got %q", got)
	}
}
