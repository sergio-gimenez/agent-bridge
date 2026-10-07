package ocs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHConfigHosts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config"), "Include conf.d/*\nHost desk sna-12\n  HostName 1.2.3.4\nHost *.internal !bad\nHost 6genablers-dlt-1 dlt-1\nhost desk\n")
	writeFile(t, filepath.Join(dir, "conf.d", "extra"), "Host homelab-box\n")
	got := strings.Join(sshConfigHosts(filepath.Join(dir, "config")), " ")
	if want := "homelab-box desk sna-12 6genablers-dlt-1 dlt-1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if hosts := sshConfigHosts(filepath.Join(dir, "missing")); len(hosts) != 0 {
		t.Fatalf("missing file gave %v", hosts)
	}
}

// dialogFixture: a Claude session in ~/phd (cc2) on a stand-in home, and an ssh
// config with a few hosts. The loopback "remote" is this machine.
func dialogFixture(t *testing.T) (Session, Config, string, string) {
	t.Helper()
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGB_CACHE_PATH", filepath.Join(home, ".cache", "agentbridge", "index.gob"))
	t.Setenv("AGB_DRY_RUN", "")
	account := Account{Tool: SourceClaude, Name: "cc2", Home: filepath.Join(home, ".claude-cc2")}
	dir := filepath.Join(home, "phd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(account.Home, "projects", claudeProjectKey(dir), "s1.jsonl")
	writeFile(t, transcript, "{}\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(transcript, old, old); err != nil {
		t.Fatal(err)
	}
	sshConfig := filepath.Join(home, ".ssh", "config")
	writeFile(t, sshConfig, "Host desk\nHost sna-12\nHost 6genablers-dlt-1\n")
	session := Session{ID: "s1", Title: "t", Source: SourceClaude, Directory: dir, Account: &account, FilePath: transcript}
	return session, Config{ClaudeAccounts: []Account{account}, MoveHosts: []string{"desk"}}, sshConfig, home
}

func runDialog(t *testing.T, session Session, config Config, sshConfig, input string) (*loopback, string, string, int, error) {
	t.Helper()
	link := &loopback{patch: map[string]string{"tool": "yes", "login": "yes"}}
	var host string
	var out bytes.Buffer
	code, err := runMoveDialog(session, config, sshConfig,
		strings.NewReader(input), &out, func(h string) remote { host = h; return link })
	return link, host, out.String(), code, err
}

func TestMoveDialogDefaults(t *testing.T) {
	linuxOnly(t)
	session, config, sshConfig, _ := dialogFixture(t)
	// Enter: first configured host; Enter: same directory; Enter: push.
	link, host, out, code, err := runDialog(t, session, config, sshConfig, "\n\n\n")
	if code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out)
	}
	if host != "desk" || !strings.Contains(out, "Directory on desk [~/phd]") || !strings.Contains(out, "Pushed.") {
		t.Fatalf("host %q\n%s", host, out)
	}
	if len(link.copies) == 0 || !strings.HasSuffix(link.copies[0][len(link.copies[0])-1], "s1.jsonl") {
		t.Fatalf("copies %v", link.copies)
	}
}

func TestMoveDialogSSHConfigAndTypedPath(t *testing.T) {
	linuxOnly(t)
	session, config, sshConfig, home := dialogFixture(t)
	elsewhere := filepath.Join(home, "src", "phd")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(session.Directory); err != nil { // not there: the path is asked for
		t.Fatal(err)
	}
	// s: pick from ssh config; filter "dlt"; 1: the only match; type ~/src/phd; y.
	link, host, out, code, err := runDialog(t, session, config, sshConfig, "s\ndlt\n1\n~/src/phd\ny\n")
	if host != "6genablers-dlt-1" {
		t.Fatalf("host %q\n%s", host, out)
	}
	if !strings.Contains(out, "~/phd is not on 6genablers-dlt-1. Directory there:") {
		t.Fatalf("no path question:\n%s", out)
	}
	// The session's own directory is gone here too, so the move refuses only if
	// the remote side is wrong; it checks the directory there, which exists.
	if code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out)
	}
	if !strings.Contains(strings.Join(link.copies[0], " "), "-> "+filepath.Join(session.Account.Home, "projects", claudeProjectKey(elsewhere), "s1.jsonl")) {
		t.Fatalf("not relocated: %v", link.copies)
	}
}

func TestMoveDialogDeclined(t *testing.T) {
	linuxOnly(t)
	session, config, sshConfig, _ := dialogFixture(t)
	link, _, out, _, err := runDialog(t, session, config, sshConfig, "\n\nn\n")
	if err != ErrCancelled || len(link.copies) != 0 {
		t.Fatalf("err %v, copies %v\n%s", err, link.copies, out)
	}
}
