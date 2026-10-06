package ocs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func seedFile(t *testing.T, source Source, raw string) Session {
	t.Helper()
	t.Setenv("OCS_CACHE_PATH", filepath.Join(t.TempDir(), "index.gob"))
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return Session{ID: "seed-test", Source: source, Directory: t.TempDir(), FilePath: path}
}

func claudeMessage(role, text string) obj {
	return obj{"type": role, "message": obj{"content": []any{obj{"type": "text", "text": text}}}}
}

func requireSeed(t *testing.T, session Session) SessionSeed {
	t.Helper()
	seed, err := BuildSessionSeed(session)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(seed.TranscriptPath) || !strings.Contains(seed.Prompt, seed.TranscriptPath) {
		t.Fatalf("handoff path missing or relative: %q", seed.TranscriptPath)
	}
	if len(seed.Prompt) > maxSeedBytes || !utf8.ValidString(seed.Prompt) {
		t.Fatalf("invalid prompt: %d bytes, UTF-8 = %v", len(seed.Prompt), utf8.ValidString(seed.Prompt))
	}
	return seed
}

func readSeed(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSeedPreservesLongMessagesAndFormatting(t *testing.T) {
	question := "Keep compatibility.\n\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```"
	answer := "BEGIN\n" + strings.Repeat("Detailed result.\n", 400) + "END: all checks passed."
	for _, source := range []Source{SourceClaude, SourceCodex} {
		t.Run(string(source), func(t *testing.T) {
			var raw string
			if source == SourceClaude {
				raw = jsonl(t, claudeMessage("user", question), claudeMessage("assistant", answer))
			} else {
				raw = jsonl(t, codexItem("user", "input_text", question), codexItem("assistant", "output_text", answer))
			}
			session := seedFile(t, source, raw)
			seed := requireSeed(t, session)
			for _, text := range []string{question, answer} {
				if !strings.Contains(seed.Prompt, text) || !strings.Contains(readSeed(t, seed.TranscriptPath), text) {
					t.Fatal("formatting or text beyond 4,000 characters was lost")
				}
			}
			if strings.Contains(seed.Prompt, "partial excerpt") || strings.Contains(seed.Prompt, "full transcript") {
				t.Fatal("short transcript incorrectly described")
			}
			if !strings.Contains(readSeed(t, seed.TranscriptPath), session.FilePath) {
				t.Fatal("raw source reference missing")
			}
		})
	}
}

func TestLongSeedRecoversOmittedHistoryAndKeepsSnapshots(t *testing.T) {
	var lines []any
	lines = append(lines, claudeMessage("user", "Original goal: preserve compatibility."))
	for i := 0; i < 35; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		lines = append(lines, claudeMessage(role, fmt.Sprintf("HISTORY-%d\n%s\nEND-%d", i, strings.Repeat("x", 5000), i)))
	}
	latest := "Latest change:\n" + strings.Repeat("Full requirement.\n", 350) + "FINAL REQUIREMENT: keep the CLI stable."
	lines = append(lines, claudeMessage("user", latest), claudeMessage("assistant", "Already updated the CLI; tests remain."))
	session := seedFile(t, SourceClaude, jsonl(t, lines...))
	seed := requireSeed(t, session)
	full := readSeed(t, seed.TranscriptPath)
	for _, want := range []string{"Original goal: preserve compatibility.", latest, "Already updated the CLI; tests remain.", "partial excerpt", "recover omitted requirements", "No readable saved compaction summary"} {
		if !strings.Contains(seed.Prompt, want) {
			t.Fatalf("seed lacks %q", want)
		}
	}
	if strings.Contains(seed.Prompt, "HISTORY-1\n") || !strings.Contains(full, "HISTORY-1\n") || !strings.Contains(full, "END-1") {
		t.Fatal("omitted history was not recoverable from the archive")
	}
	for _, path := range []string{seed.TranscriptPath, filepath.Dir(seed.TranscriptPath)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("handoff is not private: %s (%v)", path, err)
		}
	}
	// An unchanged fork reuses the snapshot; a later fork keeps the old one valid.
	if again := requireSeed(t, session); again.TranscriptPath != seed.TranscriptPath {
		t.Fatal("unchanged transcript produced a different snapshot")
	}
	lines = append(lines, claudeMessage("user", "Now finish the tests."))
	if err := os.WriteFile(session.FilePath, []byte(jsonl(t, lines...)), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := requireSeed(t, session)
	if updated.TranscriptPath == seed.TranscriptPath || readSeed(t, seed.TranscriptPath) != full {
		t.Fatal("later fork replaced the previous handoff")
	}
}

func TestSeedReusesLatestReadableSummary(t *testing.T) {
	summary := "## Objective\nPreserve compatibility.\n" + strings.Repeat("Decision.\n", 500) + "## Next Move\nRun tests."
	for _, source := range []Source{SourceClaude, SourceCodex} {
		t.Run(string(source), func(t *testing.T) {
			var old, current any
			var first, last any
			if source == SourceClaude {
				old = obj{"type": "user", "isCompactSummary": true, "message": obj{"content": "Old summary"}}
				current = obj{"type": "user", "isCompactSummary": true, "message": obj{"content": summary}}
				first, last = claudeMessage("user", "Initial task"), claudeMessage("user", "Actually, add a regression test too.")
			} else {
				old = obj{"type": "compacted", "payload": obj{"message": "Old summary"}}
				current = obj{"type": "compacted", "payload": obj{"message": summary}}
				first, last = codexItem("user", "input_text", "Initial task"), codexItem("user", "input_text", "Actually, add a regression test too.")
			}
			seed := requireSeed(t, seedFile(t, source, jsonl(t, first, old, current, last)))
			if !strings.Contains(seed.Prompt, "SUMMARY (recorded after turn 1)") || !strings.Contains(seed.Prompt, summary) || strings.Contains(seed.Prompt, "Old summary") {
				t.Fatal("latest summary was not carried intact in its own section")
			}
			if !strings.Contains(seed.Prompt, "Latest user message: [turn 2]\nActually, add a regression test too.") {
				t.Fatal("saved summary became a user request")
			}
		})
	}
}

func TestCodexSeedLegacyEventsAndOpaqueCompaction(t *testing.T) {
	for _, raw := range []string{
		jsonl(t, obj{"type": "message", "role": "user", "content": "Question\nwith formatting"}, obj{"type": "message", "role": "assistant", "content": "Answer"}),
		jsonl(t, obj{"type": "event_msg", "payload": obj{"type": "user_message", "message": "Question\nwith formatting"}}, obj{"type": "event_msg", "payload": obj{"type": "agent_message", "message": "Answer"}}),
		jsonl(t, codexItem("user", "input_text", "Question\nwith formatting"), obj{"type": "event_msg", "payload": obj{"type": "user_message", "message": "duplicate"}}, obj{"type": "compacted", "payload": obj{"message": "", "replacement_history": []any{obj{"type": "compaction", "encrypted_content": "opaque"}}}}, codexItem("assistant", "output_text", "Answer")),
	} {
		transcript, err := codexTranscript(seedFile(t, SourceCodex, raw))
		want := []Turn{{RoleUser, "Question\nwith formatting"}, {RoleAssistant, "Answer"}}
		if err != nil || !reflect.DeepEqual(transcript.Turns, want) || transcript.Summary != "" {
			t.Fatalf("transcript = %+v, error = %v", transcript, err)
		}
	}
}

func TestSeedHugeUnicodeMessagesStayWithinBudget(t *testing.T) {
	for _, summary := range []string{"", "SUMMARY-START\n" + strings.Repeat("界", 30000) + "\nSUMMARY-END"} {
		request := "REQUEST-START\n" + strings.Repeat("😀界", 30000) + "\nREQUEST-END"
		answer := "ANSWER-START\n" + strings.Repeat("界😀", 30000) + "\nANSWER-END"
		transcript := sessionTranscript{Summary: summary, SummaryAfter: 1, Turns: []Turn{{RoleUser, "Initial task"}, {RoleUser, request}, {RoleAssistant, answer}}}
		prompt := continuationPrompt(Session{}, transcript, "/tmp/complete-transcript.txt")
		if len(prompt) > maxSeedBytes || !utf8.ValidString(prompt) {
			t.Fatalf("prompt = %d bytes, valid UTF-8 = %v", len(prompt), utf8.ValidString(prompt))
		}
		for _, want := range []string{"REQUEST-START", "REQUEST-END", "ANSWER-START", "ANSWER-END", "middle omitted", "partial excerpt"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("clipped prompt lacks %q", want)
			}
		}
		if summary != "" && (!strings.Contains(prompt, "SUMMARY-START") || !strings.Contains(prompt, "SUMMARY-END")) {
			t.Fatal("summary lost one of its ends")
		}
	}
}

func TestOpencodeSeedPreservesExportAndSummary(t *testing.T) {
	t.Setenv("OCS_CACHE_PATH", filepath.Join(t.TempDir(), "index.gob"))
	dir := t.TempDir()
	question := "Implement this:\n```go\n\treturn nil\n```"
	message := func(role, text string, summary bool) obj {
		return obj{"info": obj{"role": role, "summary": summary}, "parts": []any{obj{"type": "text", "text": text}}}
	}
	failed := message("assistant", "Failed summary", true)
	failed["info"].(obj)["error"] = obj{"name": "APIError"}
	raw := jsonl(t, obj{"info": obj{"directory": dir}, "messages": []any{
		message("user", question, false), message("assistant", "## Objective\nKeep code formatting.", true), failed,
		obj{"info": obj{"role": "assistant"}, "parts": []any{obj{"type": "tool", "state": obj{"output": "FULL TOOL OUTPUT"}}, obj{"type": "text", "text": "Done."}}},
	}})
	exportPath := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(exportPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	// Export is stubbed so this test never touches a real OpenCode store.
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte("#!/bin/sh\n[ \"$1\" = export ] || exit 1\nprintf 'Exporting session: %s\\n' \"$2\"\ncat \"$OCS_TEST_EXPORT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OCS_TEST_EXPORT", exportPath)
	seed := requireSeed(t, Session{ID: "opencode-test", Source: SourceOpencode})
	full := readSeed(t, seed.TranscriptPath)
	if seed.Directory != dir || !strings.Contains(seed.Prompt, question) || !strings.Contains(seed.Prompt, "## Objective\nKeep code formatting.") || strings.Contains(seed.Prompt, "Failed summary") {
		t.Fatal("OpenCode export lost formatting or chose a failed summary")
	}
	const prefix = "Raw source (tool outputs and attachments): "
	for _, line := range strings.Split(full, "\n") {
		if strings.HasPrefix(line, prefix) {
			path := strings.TrimPrefix(line, prefix)
			if saved := readSeed(t, path); !bytes.Equal([]byte(saved), []byte(raw)) {
				t.Fatal("OpenCode raw export changed or lost tool outputs")
			}
			return
		}
	}
	t.Fatal("raw export path missing")
}

func TestSeedReportsArchiveWriteFailure(t *testing.T) {
	session := seedFile(t, SourceClaude, jsonl(t, claudeMessage("user", "Continue")))
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCS_CACHE_PATH", filepath.Join(blocker, "index.gob"))
	if _, err := BuildSessionSeed(session); err == nil || !strings.Contains(err.Error(), "Could not save the handoff transcript") {
		t.Fatalf("archive failure was not reported: %v", err)
	}
}
