package ocs

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// The other machine's agb lists its sessions as JSON; this side reads them
// back as Sessions under its own accounts of the same names.
func TestListingRoundTrip(t *testing.T) {
	cc2 := &Account{Tool: SourceClaude, Name: "cc2", Home: "/h/.claude-cc2"}
	sessions := []Session{
		{ID: "a", Title: "t", Directory: "/h/phd", Source: SourceClaude, Account: cc2, UpdatedAtMs: 1000,
			Prompts: []string{"hi"}, AssistantSnippet: []string{"yo"}, FilePath: "/h/.claude-cc2/projects/-h-phd/a.jsonl"},
		{ID: "b", Title: "u", Directory: "/h/x", Source: SourceCodex, UpdatedAtMs: 500, FilePath: "/h/.codex/sessions/b.jsonl"},
		{ID: "ses_1", Title: "oc", Source: SourceOpencode},
	}
	var out bytes.Buffer
	if err := WriteListing(&out, sessions, func(s Session) int {
		if s.ID == "a" {
			return 42
		}
		return 0
	}); err != nil {
		t.Fatal(err)
	}

	here := Config{ClaudeAccounts: []Account{{Tool: SourceClaude, Name: "cc1"}, {Tool: SourceClaude, Name: "cc2", Home: "/h/.claude-cc2"}},
		CodexAccounts: []Account{{Tool: SourceCodex, Name: "cx"}}}
	got, err := ReadListing(out.Bytes(), here)
	if err != nil {
		t.Fatal(err)
	}
	// OpenCode sessions live in a database and cannot travel, so they are left out.
	if len(got) != 2 {
		t.Fatalf("got %d sessions: %+v", len(got), got)
	}
	a := got[0]
	if a.ID != "a" || a.Account == nil || a.Account.Name != "cc2" || a.OpenPID != 42 || !reflect.DeepEqual(a.Prompts, []string{"hi"}) ||
		a.FilePath != sessions[0].FilePath || a.Directory != "/h/phd" || a.UpdatedAtMs != 1000 || a.UpdatedAtLabel == "" {
		t.Errorf("claude session read back as %+v", a)
	}
	// No account name: the tool's default account here.
	if b := got[1]; b.Source != SourceCodex || b.Account == nil || b.Account.Name != "cx" || b.OpenPID != 0 {
		t.Errorf("codex session read back as %+v", b)
	}
}

func TestStopProcess(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	// The test is the parent, so the stopped child lingers as a zombie until
	// reaped; stopProcess must count that as gone.
	if err := stopProcess(child.Process.Pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()

	stubborn := exec.Command("bash", "-c", "trap '' TERM; sleep 30")
	if err := stubborn.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stubborn.Process.Kill(); _ = stubborn.Wait() }()
	time.Sleep(100 * time.Millisecond) // let the trap be set
	if err := stopProcess(stubborn.Process.Pid, 300*time.Millisecond); err == nil {
		t.Fatal("a process ignoring SIGTERM was reported stopped")
	}
}
