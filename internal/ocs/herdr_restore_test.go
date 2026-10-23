package ocs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHerdrLaunchPermissionMarker(t *testing.T) {
	if os.Getenv("AGB_TEST_LAUNCH_HELPER") == "1" {
		code, err := OpenNew(os.Getenv("AGB_TEST_WORKDIR"), Account{Tool: Source(os.Getenv("AGB_TEST_TOOL"))},
			OpenOptions{SkipPermissions: os.Getenv("AGB_TEST_DANGEROUS") == "1"})
		fmt.Fprintln(os.Stderr, err)
		os.Exit(code)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"claude", "codex"} {
		for _, dangerous := range []bool{false, true} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\nprintf '%s' \"$AGB_HERDR_LAUNCH_MODE\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			mode, switchValue := "safe", ""
			if dangerous {
				mode, switchValue = "dangerous", "1"
			}
			cmd := exec.Command(binary, "-test.run=^TestHerdrLaunchPermissionMarker$")
			cmd.Env = childEnvironment([]envOverride{
				{"PATH", dir + string(os.PathListSeparator) + os.Getenv("PATH")},
				{"HERDR_ENV", "1"}, {"AGB_DRY_RUN", ""}, {"OCS_DRY_RUN", ""},
				{"AGB_TEST_LAUNCH_HELPER", "1"}, {"AGB_TEST_TOOL", tool}, {"AGB_TEST_WORKDIR", dir},
				{"AGB_TEST_DANGEROUS", switchValue}, {"AGB_HERDR_LAUNCH_MODE", tool + ":dangerous"},
			})
			if out, err := cmd.CombinedOutput(); err != nil || string(out) != tool+":"+mode {
				t.Fatalf("%s %s launch: %q %v", tool, mode, out, err)
			}
		}
	}
}

func TestHerdrResumeKeepsAccountAndPermissionMode(t *testing.T) {
	for _, tool := range []Source{SourceClaude, SourceCodex} {
		for _, home := range []string{"", filepath.Join(t.TempDir(), "account with spaces; $(touch should-not-exist)")} {
			for _, dangerous := range []bool{false, true} {
				t.Run(string(tool)+home+fmtBool(dangerous), func(t *testing.T) {
					bin := t.TempDir()
					stub := "#!/bin/sh\nprintf '%s\\n' \"${CLAUDE_CONFIG_DIR-unset}\" \"${CODEX_HOME-unset}\" \"${AGB_HERDR_LAUNCH_MODE-unset}\" \"$@\"\n"
					if err := os.WriteFile(filepath.Join(bin, string(tool)), []byte(stub), 0o700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
					t.Setenv("CLAUDE_CONFIG_DIR", "wrong-claude")
					t.Setenv("CODEX_HOME", "wrong-codex")
					mode := string(tool) + ":safe"
					if dangerous {
						mode = string(tool) + ":dangerous"
					}
					argv := herdrResumeArgv(Account{Tool: tool, Home: home}, "session-id", mode)
					// Run the same quoted argv Herdr types into the restored shell.
					var quoted []string
					for _, arg := range argv {
						quoted = append(quoted, shellQuote(arg))
					}
					cmd := exec.Command("sh", "-c", strings.Join(quoted, " "))
					cmd.Dir = bin
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("%s: %v", out, err)
					}
					lines := strings.Split(strings.TrimSpace(string(out)), "\n")
					i := 0
					if tool == SourceCodex {
						i = 1
					}
					want := home
					if want == "" {
						want = "unset"
					}
					if lines[i] != want || lines[2] != mode || lines[4] != "session-id" {
						t.Fatalf("wrong account or session: %q", lines)
					}
					if dangerous != strings.Contains(string(out), "--dangerously-") {
						t.Fatalf("permission mode changed: %s", out)
					}
				})
			}
		}
	}
}

func fmtBool(value bool) string {
	if value {
		return "/dangerous"
	}
	return "/safe"
}

func TestHerdrHookRejectsForeignAndChildSessions(t *testing.T) {
	account := Account{Tool: SourceClaude, Home: t.TempDir()}
	path := filepath.Join(account.Home, "projects", "project", "s1.jsonl")
	input := herdrHookInput{Event: "SessionStart", SessionID: "s1", Transcript: path}
	t.Setenv("CURSOR_VERSION", "")
	if !validHerdrHook(account, input) {
		t.Fatal("new transcript need not exist before SessionStart finishes")
	}
	for _, change := range []func(*herdrHookInput){
		func(p *herdrHookInput) { p.AgentID = "subagent" },
		func(p *herdrHookInput) { p.Event = "Stop" },
		func(p *herdrHookInput) { p.SessionID = "" },
		func(p *herdrHookInput) { p.SessionID = "other-id" },
		func(p *herdrHookInput) { p.Transcript = "" },
		func(p *herdrHookInput) { p.Transcript = filepath.Join(t.TempDir(), "s1.jsonl") },
	} {
		bad := input
		change(&bad)
		if validHerdrHook(account, bad) {
			t.Fatalf("accepted foreign hook: %+v", bad)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.jsonl"), path); err != nil {
		t.Fatal(err)
	}
	if validHerdrHook(account, input) {
		t.Fatal("accepted escaping symlink")
	}
	account.Tool = SourceCodex
	input.Transcript = filepath.Join(account.Home, "sessions", "rollout-s1.jsonl")
	t.Setenv("CODEX_THREAD_ID", "parent-id")
	if validHerdrHook(account, input) {
		t.Fatal("accepted inherited child Codex thread")
	}
	t.Setenv("CODEX_THREAD_ID", "s1")
	if !validHerdrHook(account, input) {
		t.Fatal("rejected root Codex thread")
	}
}

func fakeHerdrReporter(t *testing.T, tool Source, id string) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "herdr")
	log := filepath.Join(dir, "report")
	body := `#!/bin/sh
if [ "$2" = get ]; then
  printf '%s\n' "$AGB_TEST_PANE"
else
  printf '%s\n' "$@" > "$AGB_TEST_REPORT"
fi
`
	if err := os.WriteFile(binary, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "test:p1")
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "socket"))
	t.Setenv("HERDR_BIN_PATH", binary)
	t.Setenv("AGB_TEST_REPORT", log)
	pane, _ := json.Marshal(map[string]any{"result": map[string]any{"pane": map[string]any{
		"agent": tool, "agent_session": map[string]any{"source": "herdr:" + string(tool), "value": id},
	}}})
	t.Setenv("AGB_TEST_PANE", string(pane))
	return log
}

func TestHerdrResumeRequiresMatchingNativeSession(t *testing.T) {
	account := Account{Tool: SourceClaude, Home: t.TempDir()}
	log := fakeHerdrReporter(t, account.Tool, "root-session")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := herdrHookInput{SessionID: "child-session"}
	if err := reportHerdrResume(ctx, account, input, []string{"env", "claude"}); err == nil {
		t.Fatal("accepted a different session")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("reported foreign session")
	}
	input.SessionID = "root-session"
	if err := reportHerdrResume(ctx, account, input, herdrResumeArgv(account, input.SessionID, "")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	report := string(raw)
	for _, want := range []string{"--source\nagb:claude\n", "--agent-session-id\nroot-session\n", "CLAUDE_CONFIG_DIR=" + account.Home, "--resume\nroot-session"} {
		if !strings.Contains(report, want) {
			t.Fatalf("missing %q in %s", want, report)
		}
	}
	if strings.Contains(report, "--state") {
		t.Fatal("agb must not take over native lifecycle detection")
	}
}

func TestHerdrHookRefreshesOnClearAndResume(t *testing.T) {
	account := Account{Tool: SourceClaude, Home: t.TempDir()}
	log := fakeHerdrReporter(t, account.Tool, "new-session")
	native := herdrNativeHook(account)
	if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"startup", "resume", "clear", "compact", "fork"} {
		payload, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "session_id": "new-session", "source": source,
			"transcript_path": filepath.Join(account.Home, "projects", "project", "new-session.jsonl")})
		runHerdrHook(account, bytes.NewReader(payload))
		if raw, err := os.ReadFile(log); err != nil || !strings.Contains(string(raw), "--resume\nnew-session") {
			t.Fatalf("%s: did not save new session: %s %v", source, raw, err)
		}
	}
}

func TestHerdrHookFailsOpenAndSkipsOutsideHerdr(t *testing.T) {
	account := Account{Tool: SourceClaude, Home: t.TempDir()}
	log := fakeHerdrReporter(t, account.Tool, "s1")
	t.Setenv("HERDR_ENV", "")
	runHerdrHook(account, strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s1"}`))
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("reported outside Herdr")
	}
	var out bytes.Buffer
	for _, args := range [][]string{{"hook", "--tool", "other"}, {"hook", "--unknown"}, {"hook", "--tool", "claude"}} {
		if code, err := RunHerdrCommand(args, strings.NewReader("invalid"), &out); code != 0 || err != nil || out.Len() != 0 {
			t.Fatalf("hook disrupted agent: %d %v %s", code, err, &out)
		}
	}
	if validHerdrResumeArgs([]string{"env", "CLAUDE_CONFIG_DIR=has'apostrophe", "claude"}) || validHerdrResumeArgs([]string{"env", "line\nbreak"}) {
		t.Fatal("accepted args Herdr cannot restore")
	}
	_, _ = RunHerdrCommand([]string{"--help"}, strings.NewReader(""), io.Discard)
}
