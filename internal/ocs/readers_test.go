package ocs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func jsonl(t *testing.T, lines ...any) string {
	t.Helper()
	var out []string
	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(data))
	}
	return strings.Join(out, "\n")
}

type obj = map[string]any

func claudeFixture(t *testing.T) string {
	return jsonl(t,
		obj{"type": "mode", "mode": "normal"},
		obj{"type": "ai-title", "aiTitle": "Refactor the picker"},
		obj{"type": "user", "cwd": "/home/dev/project",
			"message": obj{"role": "user", "content": "How do I make the picker responsive?"}},
		obj{"type": "assistant",
			"message": obj{"role": "assistant", "content": []any{obj{"type": "text", "text": "Use terminal width."}}}},
		obj{"type": "user",
			"message": obj{"role": "user", "content": []any{obj{"type": "tool_result", "text": "ignored"}}}},
		obj{"type": "user",
			"message": obj{"role": "user", "content": []any{obj{"type": "text", "text": "Add source badges too."}}}},
	)
}

func parseClaudeString(t *testing.T, raw string) *record {
	t.Helper()
	rec, err := parseClaude(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

var testFile = scannedFile{Path: "/tmp/abc-123.jsonl", ModNs: 1_000_000_000}

func TestClaudeSession(t *testing.T) {
	rec := parseClaudeString(t, claudeFixture(t))
	session := claudeSession(rec, testFile, ScopeUser, nil)

	if session.Source != SourceClaude || session.ID != "abc-123" {
		t.Fatalf("source/id = %s/%s", session.Source, session.ID)
	}
	if session.Title != "Refactor the picker" || session.Directory != "/home/dev/project" {
		t.Fatalf("title/dir = %q/%q", session.Title, session.Directory)
	}
	want := []string{"How do I make the picker responsive?", "Add source badges too."}
	if !reflect.DeepEqual(session.Prompts, want) {
		t.Fatalf("prompts = %q", session.Prompts)
	}
	if !strings.Contains(session.SearchText, "responsive") || strings.Contains(session.SearchText, "Use terminal width") {
		t.Fatalf("search text = %q", session.SearchText)
	}
	for _, prompt := range session.Prompts {
		if prompt == "ignored" {
			t.Fatal("tool-result-only message became a prompt")
		}
	}

	all := claudeSession(rec, testFile, ScopeAll, nil)
	if !strings.Contains(all.SearchText, "Use terminal width") {
		t.Fatal("assistant text missing from the all-scope search text")
	}
}

func TestClaudeSkipsEmptyAndFallsBackToFirstPrompt(t *testing.T) {
	if rec := parseClaudeString(t, jsonl(t, obj{"type": "mode", "mode": "normal"})); rec != nil {
		t.Fatal("a session with no title and no prompts should be skipped")
	}

	rec := parseClaudeString(t, jsonl(t,
		obj{"type": "user", "cwd": "/tmp", "message": obj{"role": "user", "content": "First real question"}}))
	if title := claudeSession(rec, testFile, ScopeUser, nil).Title; title != "First real question" {
		t.Fatalf("title = %q", title)
	}
}

func TestClaudeFilesRecordAccountAndPath(t *testing.T) {
	configDir := t.TempDir()
	projectDir := filepath.Join(configDir, "projects", "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, "owned.jsonl")
	if err := os.WriteFile(path, []byte(claudeFixture(t)), 0o644); err != nil {
		t.Fatal(err)
	}

	account := &Account{Tool: SourceClaude, Name: "cc2", Home: configDir}
	sessions := OpenCache("", false).fileSessions(claudeSource, account, ScopeUser)

	if len(sessions) != 1 || sessions[0].Account != account || sessions[0].FilePath != path {
		t.Fatalf("sessions = %+v", sessions)
	}
}

func TestMergeSessionsSortsNewestFirst(t *testing.T) {
	opencode := []Session{{ID: "o1", UpdatedAtMs: 300}}
	claude := []Session{{ID: "c1", UpdatedAtMs: 500}, {ID: "c2", UpdatedAtMs: 100}}

	var ids []string
	for _, session := range MergeSessions(opencode, claude) {
		ids = append(ids, session.ID)
	}
	if !reflect.DeepEqual(ids, []string{"c1", "o1", "c2"}) {
		t.Fatalf("order = %v", ids)
	}
}

func codexItem(role, kind, text string) obj {
	return obj{
		"timestamp": "2025-06-01T09:30:00.000Z",
		"type":      "response_item",
		"payload":   obj{"type": "message", "role": role, "content": []any{obj{"type": kind, "text": text}}},
	}
}

const codexID = "7c1e5c40-2f6a-4a0b-9c3d-0f1a2b3c4d5e"

func codexFixture(t *testing.T) string {
	return jsonl(t,
		obj{"type": "session_meta", "payload": obj{"id": codexID, "cwd": "/home/dev/project"}},
		codexItem("user", "input_text", "<environment_context>cwd=/home/dev/project</environment_context>"),
		codexItem("user", "input_text", "Why does the mesh VPN handshake time out?"),
		obj{"type": "response_item", "payload": obj{"type": "function_call", "name": "shell"}},
		codexItem("assistant", "output_text", "The WireGuard keepalive is unset."),
		codexItem("user", "input_text", "Set it to 25 seconds then."),
	)
}

func parseRolloutString(t *testing.T, raw string) rollout {
	t.Helper()
	parsed, err := parseRollout(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestParseRolloutEnvelope(t *testing.T) {
	parsed := parseRolloutString(t, codexFixture(t))
	if parsed.ID != codexID || parsed.Directory != "/home/dev/project" {
		t.Fatalf("id/dir = %q/%q", parsed.ID, parsed.Directory)
	}
	// The context Codex feeds itself as an opening user turn is dropped.
	want := []Turn{
		{RoleUser, "Why does the mesh VPN handshake time out?"},
		{RoleAssistant, "The WireGuard keepalive is unset."},
		{RoleUser, "Set it to 25 seconds then."},
	}
	if !reflect.DeepEqual(parsed.Turns, want) {
		t.Fatalf("turns = %+v", parsed.Turns)
	}
}

func TestParseRolloutLegacyAndEvents(t *testing.T) {
	legacy := parseRolloutString(t, jsonl(t,
		obj{"id": "legacy-id", "cwd": "/tmp/legacy"},
		obj{"type": "message", "role": "user", "content": []any{obj{"type": "input_text", "text": "Old format"}}},
		obj{"type": "message", "role": "assistant", "content": []any{obj{"type": "output_text", "text": "Still read."}}},
	))
	if legacy.ID != "legacy-id" || legacy.Directory != "/tmp/legacy" || len(legacy.Turns) != 2 {
		t.Fatalf("legacy = %+v", legacy)
	}

	events := parseRolloutString(t, jsonl(t,
		obj{"type": "session_meta", "payload": obj{"id": "e1", "cwd": "/tmp/e"}},
		obj{"type": "event_msg", "payload": obj{"type": "user_message", "message": "Event only"}},
		obj{"type": "event_msg", "payload": obj{"type": "agent_message", "message": "Answered"}},
	))
	if !reflect.DeepEqual(events.Turns, []Turn{{RoleUser, "Event only"}, {RoleAssistant, "Answered"}}) {
		t.Fatalf("events = %+v", events.Turns)
	}

	// Both records of one turn must not count it twice.
	both := parseRolloutString(t, jsonl(t,
		obj{"type": "session_meta", "payload": obj{"id": "b1", "cwd": "/tmp/b"}},
		codexItem("user", "input_text", "Only once"),
		obj{"type": "event_msg", "payload": obj{"type": "user_message", "message": "Only once"}},
	))
	if !reflect.DeepEqual(both.Turns, []Turn{{RoleUser, "Only once"}}) {
		t.Fatalf("both = %+v", both.Turns)
	}
}

func TestCodexSession(t *testing.T) {
	rec, err := parseCodex(strings.NewReader(codexFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	session := codexSession(rec, scannedFile{Path: "/tmp/from-filename.jsonl"}, ScopeUser, nil)

	// The rollout's own id wins over the one guessed from the file name.
	if session.ID != codexID || session.Source != SourceCodex {
		t.Fatalf("id/source = %s/%s", session.ID, session.Source)
	}
	if session.Title != "Why does the mesh VPN handshake time out?" {
		t.Fatalf("title = %q", session.Title)
	}
	if !strings.Contains(session.SearchText, "handshake") || strings.Contains(session.SearchText, "keepalive") {
		t.Fatalf("search text = %q", session.SearchText)
	}
	if all := codexSession(rec, scannedFile{}, ScopeAll, nil); !strings.Contains(all.SearchText, "keepalive") {
		t.Fatal("assistant text missing from the all-scope search text")
	}

	empty, _ := parseCodex(strings.NewReader(jsonl(t, obj{"type": "session_meta", "payload": obj{"id": "x", "cwd": "/tmp"}})))
	if empty != nil {
		t.Fatal("a rollout with no prompt should be skipped")
	}
}

func TestCodexFilesSkipArchived(t *testing.T) {
	home := t.TempDir()
	dayDir := filepath.Join(home, "sessions", "2025", "06", "01")
	archived := filepath.Join(home, "archived_sessions", "2025", "06", "01")
	for _, dir := range []string{dayDir, archived} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dayDir, "rollout-2025-06-01T09-30-00-"+codexID+".jsonl")
	os.WriteFile(path, []byte(codexFixture(t)), 0o644)
	os.WriteFile(filepath.Join(archived, "rollout-old.jsonl"), []byte(codexFixture(t)), 0o644)

	account := &Account{Tool: SourceCodex, Name: "cx2", Home: home}
	sessions := OpenCache("", false).fileSessions(codexSource, account, ScopeUser)
	if len(sessions) != 1 || sessions[0].FilePath != path || sessions[0].Account != account {
		t.Fatalf("sessions = %+v", sessions)
	}

	missing := &Account{Tool: SourceCodex, Name: "none", Home: filepath.Join(home, "nope")}
	if got := OpenCache("", false).fileSessions(codexSource, missing, ScopeUser); len(got) != 0 {
		t.Fatalf("missing store gave %d sessions", len(got))
	}
}

func TestSeedFromCodexRollout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	os.WriteFile(path, []byte(codexFixture(t)), 0o644)

	rec, _ := parseCodex(strings.NewReader(codexFixture(t)))
	session := codexSession(rec, scannedFile{Path: path}, ScopeUser, nil)
	seed, err := BuildSessionSeed(session)
	if err != nil {
		t.Fatal(err)
	}

	if seed.Directory != "/home/dev/project" {
		t.Fatalf("directory = %q", seed.Directory)
	}
	for _, want := range []string{
		"prior Codex conversation",
		"USER: Why does the mesh VPN handshake time out?",
		"ASSISTANT: The WireGuard keepalive is unset.",
		"Latest user message: Set it to 25 seconds then.",
	} {
		if !strings.Contains(seed.Prompt, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
}

func TestOpencodeSessionAndSearch(t *testing.T) {
	row := opencodeRow{ID: "ses_1", Title: "Mesh VPN troubleshooting", Directory: "/tmp/project-a", TimeUpdated: 100}
	cached := cachedOpencode{
		User:      []string{"How do I configure the mental model for the lab?", "Need to fix the VPN gateway route"},
		Assistant: []string{"Try checking the WireGuard peer configuration."},
	}

	user := opencodeSession(row, cached, ScopeUser)
	if len(user.Prompts) != 2 || len(user.AssistantSnippet) != 1 {
		t.Fatalf("preview = %+v", user)
	}
	if len(SearchSessions([]Session{user}, "mental model")) != 1 {
		t.Fatal("full prompts should be searchable")
	}
	if len(SearchSessions([]Session{user}, "wireguard peer")) != 0 {
		t.Fatal("assistant text matched without --assistant")
	}
	all := opencodeSession(row, cached, ScopeAll)
	if len(SearchSessions([]Session{all}, "wireguard peer")) != 1 {
		t.Fatal("assistant text should match with --assistant")
	}
}

func TestParseTextPart(t *testing.T) {
	if got := parseTextPart(`{"text":"mesh\n\n vpn\t gateway "}`); got != "mesh vpn gateway" {
		t.Fatalf("got %q", got)
	}
	if got := parseTextPart(`{"text":42}`); got != "" {
		t.Fatalf("non-string text gave %q", got)
	}
}
