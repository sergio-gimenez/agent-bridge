package ocs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pullFixture is a home where the "remote" (a loopback, so its probe scripts
// really run) holds one cc2 transcript for ~/phd, as a pushed-then-continued
// session would look on desk.
func pullFixture(t *testing.T) (config Config, account Account, transcript, workdir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGB_DRY_RUN", "")
	installed := lookPath
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	t.Cleanup(func() { lookPath = installed })
	account = Account{Tool: SourceClaude, Name: "cc2", Home: filepath.Join(home, ".claude-cc2")}
	workdir = filepath.Join(home, "phd")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(account.Home, "projects", claudeProjectKey(workdir))
	transcript = filepath.Join(project, "0c5ddcb6-abc5-4822-affc-24a191af12f0.jsonl")
	writeFile(t, transcript, `{"type":"user","cwd":"`+workdir+`","message":{"content":"hi"}}`+"\n")
	writeFile(t, filepath.Join(project, "0c5ddcb6-abc5-4822-affc-24a191af12f0", "tool-results", "r.txt"), "r")
	writeFile(t, filepath.Join(project, "memory", "MEMORY.md"), "- fact")
	config = Config{ClaudeAccounts: []Account{{Tool: SourceClaude, Name: "cc1", Home: filepath.Join(home, ".claude")}, account}}
	return config, account, transcript, workdir
}

func TestPullSession(t *testing.T) {
	linuxOnly(t)
	config, account, transcript, workdir := pullFixture(t)
	there := &loopback{}
	var out bytes.Buffer

	// A prefix of the id finds it in whichever account holds it.
	code, err := pullSession(config, PullOptions{Host: "desk", ID: "0c5ddcb6"}, there, &out)
	if code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out.String())
	}
	got := fmt.Sprint(there.fetches)
	for _, want := range []string{
		transcript + " -> " + transcript,
		strings.TrimSuffix(transcript, jsonlExt) + "/ -> " + strings.TrimSuffix(transcript, jsonlExt) + "/",
		"--update " + filepath.Join(filepath.Dir(transcript), "memory") + "/ -> ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fetches lack %q:\n%s", want, got)
		}
	}
	if len(there.copies) != 0 {
		t.Errorf("pull pushed something: %v", there.copies)
	}
	resume := "cd " + shellQuote(workdir) + " && CLAUDE_CONFIG_DIR=" + shellQuote(account.Home) + " claude --resume 0c5ddcb6-abc5-4822-affc-24a191af12f0"
	if !strings.Contains(out.String(), "Pulled CC2 0c5ddcb6-abc5-4822-affc-24a191af12f0 from desk") || !strings.Contains(out.String(), resume) {
		t.Fatalf("output:\n%s", out.String())
	}

	// A dry run fetches nothing.
	there, out = &loopback{}, bytes.Buffer{}
	if code, err := pullSession(config, PullOptions{Host: "desk", ID: "0c5d", DryRun: true}, there, &out); code != 0 || err != nil || len(there.fetches) != 0 {
		t.Fatalf("dry run: %d %v, fetches %v\n%s", code, err, there.fetches, out.String())
	}
}

func TestPullSessionStops(t *testing.T) {
	linuxOnly(t)
	config, _, _, workdir := pullFixture(t)
	cases := []struct {
		name  string
		id    string
		patch map[string]string
		opts  PullOptions
		want  string
	}{
		{"unknown id", "ffff", nil, PullOptions{}, "no session"},
		{"unsafe id", "0c5d'; rm -rf ~", nil, PullOptions{}, "not a session id"},
		{"open there", "0c5d", map[string]string{"open_there": "77"}, PullOptions{Force: true}, "open on desk (pid 77)"},
		{"no directory here", "0c5d", nil, PullOptions{LocalDir: filepath.Join(workdir, "gone")}, "does not exist here"},
		{"no tool here", "0c5d", nil, PullOptions{}, "Claude Code is not installed here"},
		{"bigger here", "0c5d", map[string]string{"size": "3"}, PullOptions{}, "bigger here"},
	}
	for _, c := range cases {
		if c.name == "no tool here" {
			lookPath = func(string) (string, error) { return "", os.ErrNotExist }
		}
		there := &loopback{patch: c.patch}
		var out bytes.Buffer
		opts := c.opts
		opts.Host, opts.ID = "desk", c.id
		code, err := pullSession(config, opts, there, &out)
		if code == 0 || len(there.fetches) != 0 || !strings.Contains(out.String()+fmt.Sprint(err), c.want) {
			t.Errorf("%s: code %d, err %v, fetches %v, want %q\n%s", c.name, code, err, there.fetches, c.want, out.String())
		}
	}
}

// A dirty or unpushed checkout there is a note: the code is git's to carry.
func TestPullSessionGitNotes(t *testing.T) {
	linuxOnly(t)
	config, _, _, _ := pullFixture(t)
	there := &loopback{patch: map[string]string{"git_dirty": "yes", "git_unpushed": "yes"}}
	var out bytes.Buffer
	if code, err := pullSession(config, PullOptions{Host: "desk", ID: "0c5d"}, there, &out); code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out.String())
	}
	for _, want := range []string{"Note: ~/phd has uncommitted changes on desk", "Note: HEAD of ~/phd on desk is on no remote branch"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// Into another directory here, a Claude transcript lands in that directory's
// project folder, and resumes there.
func TestPullSessionToAnotherDirectory(t *testing.T) {
	linuxOnly(t)
	config, account, transcript, _ := pullFixture(t)
	elsewhere := filepath.Join(filepath.Dir(account.Home), "src", "phd")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	there := &loopback{}
	var out bytes.Buffer
	if code, err := pullSession(config, PullOptions{Host: "desk", ID: "0c5d", LocalDir: elsewhere}, there, &out); code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out.String())
	}
	dest := filepath.Join(account.Home, "projects", claudeProjectKey(elsewhere), filepath.Base(transcript))
	if got := fmt.Sprint(there.fetches); !strings.Contains(got, transcript+" -> "+dest) {
		t.Fatalf("not relocated: %s", got)
	}
	if !strings.Contains(out.String(), "cd "+shellQuote(elsewhere)+" && ") {
		t.Fatalf("output:\n%s", out.String())
	}
}
