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

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The drift check compares a hash computed here with one computed there by a
// shell script; the two must agree on every input, or every skill reads as drift.
func TestTreeHashMatchesShell(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: x\n---\nbody")
	writeFile(t, filepath.Join(dir, "references", "a b.md"), "spaces in the name")
	writeFile(t, filepath.Join(dir, "references", "Z.md"), "upper case sorts first in C order")
	writeFile(t, filepath.Join(dir, "scripts", "__pycache__", "x.cpython-312.pyc"), "cache")
	writeFile(t, filepath.Join(dir, "scripts", "tool.pyc"), "cache")
	writeFile(t, filepath.Join(dir, "scripts", "tool.py"), "print(1)")
	if err := os.Symlink(filepath.Join(dir, "SKILL.md"), filepath.Join(dir, "linked.md")); err != nil {
		t.Fatal(err)
	}

	local, err := treeHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "-c", treeHashScript+"treehash "+shellQuote(dir)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if shell := strings.TrimSpace(string(out)); shell != local {
		t.Fatalf("shell hash %s, Go hash %s", shell, local)
	}

	missing, _ := exec.Command("bash", "-c", treeHashScript+"treehash "+shellQuote(filepath.Join(dir, "nope"))).Output()
	if strings.TrimSpace(string(missing)) != "missing" {
		t.Fatalf("a missing directory hashes as %q", missing)
	}
}

func TestCheckPrefix(t *testing.T) {
	local := []byte("line 1\nline 2\nline 3\n")
	sha := func(b []byte) string { return fileHashBytes(b) }
	cases := []struct {
		name    string
		size    int64
		sha     string
		wantErr string
	}{
		{"no copy there", -1, "", ""},
		{"older state", 7, sha(local[:7]), ""},
		{"same state", int64(len(local)), sha(local), ""},
		{"continued there", int64(len(local) + 5), "x", "longer"},
		{"diverged", 7, sha([]byte("line X\n")), "diverged"},
	}
	for _, c := range cases {
		err := checkPrefix(local, c.size, c.sha)
		if (err == nil) != (c.wantErr == "") || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.wantErr)
		}
	}
}

func fileHashBytes(raw []byte) string {
	path := filepath.Join(os.TempDir(), "agb-hash-probe")
	_ = os.WriteFile(path, raw, 0o600)
	defer os.Remove(path)
	return fileHash(path)
}

func TestReferencedFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	handoffs := filepath.Join(home, ".cache", "agentbridge", "handoffs")
	projects := filepath.Join(home, ".claude", "projects")
	handoff := filepath.Join(handoffs, "abc.txt")
	other := filepath.Join(projects, "-p", "other.jsonl")
	self := filepath.Join(projects, "-p", "self.jsonl")
	writeFile(t, handoff, "handoff")
	writeFile(t, other, "{}")
	writeFile(t, filepath.Join(projects, "-p", "other", "subagents", "a.jsonl"), "{}")
	writeFile(t, self, "{}")
	writeFile(t, filepath.Join(home, "notes.txt"), "outside the roots")
	memory := filepath.Join(projects, "-p", "memory", "MEMORY.md")
	nested := filepath.Join(projects, "-p", "other", "subagents", "a.jsonl")
	tracker := other + ".wakatime"
	writeFile(t, memory, "- fact")
	writeFile(t, tracker, "not a transcript")

	transcript := []byte(`{"text":"read ` + handoff + `. Also ` + other + `, ` + self + `, ` + memory + `, ` + nested +
		`, ` + tracker + `, ` + filepath.Join(home, "notes.txt") + ` and ` + filepath.Join(handoffs, "gone.txt") + `"}`)
	got := referencedFiles(transcript, self, handoffs, []string{projects})
	// The handoff; the other transcript and its directory (which carries the
	// nested subagent file). Not: itself, memory (merged separately), a
	// non-transcript beside a transcript, files outside the roots, missing files.
	want := []string{handoff, filepath.Join(projects, "-p", "other"), other}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func baseFacts(home string) moveFacts {
	return moveFacts{
		Session:       Session{ID: "s1", Source: SourceClaude, Directory: "/work/p"},
		Home:          filepath.Join(home, ".claude"),
		Transcript:    []byte("a\nb\n"),
		WrittenAgo:    time.Hour,
		LocalSettings: "S",
		LocalSetup:    map[string]string{"config": "C", "skill:x": "X"},
		Remote: map[string]string{"home": home, "dir": "yes", "tool": "yes", "login": "yes", "agb": "yes",
			"settings": "S", "setup:config": "C", "setup:skill:x": "X"},
	}
}

func TestDecideMove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	check := func(name string, mutate func(*moveFacts), opts MoveOptions, wantBlock, wantWarn string) moveDecision {
		t.Helper()
		f := baseFacts(home)
		mutate(&f)
		d := decideMove(f, opts)
		blocks, warns := strings.Join(d.Blockers, "|"), strings.Join(d.Warnings, "|")
		if (wantBlock == "") != (blocks == "") || !strings.Contains(blocks, wantBlock) {
			t.Errorf("%s: blockers %q, want %q", name, blocks, wantBlock)
		}
		if !strings.Contains(warns, wantWarn) {
			t.Errorf("%s: warnings %q, want %q", name, warns, wantWarn)
		}
		return d
	}
	none := func(*moveFacts) {}
	check("clean", none, MoveOptions{}, "", "")
	check("other home", func(f *moveFacts) { f.Remote["home"] = "/home/other" }, MoveOptions{}, "same home path", "")
	check("no directory", func(f *moveFacts) { f.Remote["dir"] = "no" }, MoveOptions{}, "does not exist there", "")
	check("no tool", func(f *moveFacts) { f.Remote["tool"] = "no" }, MoveOptions{}, "not installed", "")
	check("logged out", func(f *moveFacts) { f.Remote["login"] = "no" }, MoveOptions{}, "", "logged in")
	check("continued there", func(f *moveFacts) { f.Remote["session_size"] = "99" }, MoveOptions{}, "longer", "")
	check("still open", func(f *moveFacts) { f.WrittenAgo = time.Second }, MoveOptions{}, "still open", "")
	check("still open, forced", func(f *moveFacts) { f.WrittenAgo = time.Second }, MoveOptions{Force: true}, "", "--force")
	check("dirty", func(f *moveFacts) { f.GitRepo, f.GitDirty = true, true }, MoveOptions{}, "uncommitted", "")
	check("unpushed", func(f *moveFacts) { f.GitRepo, f.GitUnpushed = true, true }, MoveOptions{}, "push it first", "")
	check("other checkout", func(f *moveFacts) { f.GitRepo, f.GitHead, f.Remote["git_head"] = true, "aaa", "bbb" }, MoveOptions{}, "", "pull or switch")
	check("drift", func(f *moveFacts) { f.Remote["setup:skill:x"] = "Y" }, MoveOptions{}, "skill:x", "")
	check("drift, ignored", func(f *moveFacts) { f.Remote["setup:skill:x"] = "Y" }, MoveOptions{IgnoreDrift: true}, "", "ignored")
	check("drift, synced, no agb", func(f *moveFacts) { f.Remote["setup:config"], f.Remote["agb"] = "D", "no" }, MoveOptions{SyncSetup: true}, "needs agb", "")
	d := check("drift, synced", func(f *moveFacts) { f.Remote["setup:config"] = "D" }, MoveOptions{SyncSetup: true}, "", "")
	if strings.Join(d.SetupDrift, ",") != "config" {
		t.Errorf("drift items %v", d.SetupDrift)
	}
	if d := check("no settings there", func(f *moveFacts) { f.Remote["settings"] = "missing" }, MoveOptions{}, "", ""); !d.CopySettings {
		t.Error("settings.json is not copied to a machine that has none")
	}
	check("different settings", func(f *moveFacts) { f.Remote["settings"] = "T" }, MoveOptions{}, "", "differs there")
}

// loopback is a "remote" that is this machine: the probe script really runs, so
// it is tested too; copies are recorded instead of made.
type loopback struct {
	patch  map[string]string
	copies [][]string
	runs   []string
}

func (l *loopback) Probe(script string) (string, error) {
	out, err := exec.Command("bash", "-c", script).Output()
	var extra strings.Builder
	for key, value := range l.patch {
		extra.WriteString(key + "=" + value + "\n")
	}
	return string(out) + extra.String(), err
}

func (l *loopback) Copy(paths []string, flags ...string) error {
	l.copies = append(l.copies, append(append([]string{}, flags...), paths...))
	return nil
}

func (l *loopback) Run(command string) error { l.runs = append(l.runs, command); return nil }

func TestMoveSession(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGB_CACHE_PATH", filepath.Join(home, ".cache", "agentbridge", "index.gob"))
	t.Setenv("AGB_DRY_RUN", "")
	account := Account{Tool: SourceClaude, Name: "cc2", Home: filepath.Join(home, ".claude-cc2")}
	project := filepath.Join(account.Home, "projects", "-work")
	transcriptPath := filepath.Join(project, "s1.jsonl")
	handoff := filepath.Join(home, ".cache", "agentbridge", "handoffs", "h.txt")
	writeFile(t, transcriptPath, `{"cwd":"x","text":"see `+handoff+`"}`+"\n")
	writeFile(t, filepath.Join(project, "s1", "tool-results", "r.txt"), "result")
	writeFile(t, filepath.Join(project, "memory", "MEMORY.md"), "- fact")
	writeFile(t, handoff, "handoff")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(transcriptPath, old, old); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	session := Session{ID: "s1", Title: "t", Source: SourceClaude, Directory: workdir, Account: &account, FilePath: transcriptPath}
	config := Config{ClaudeAccounts: []Account{account}}

	// Dry run: checks, lists, copies nothing.
	there := &loopback{patch: map[string]string{"tool": "yes", "login": "yes"}}
	var out bytes.Buffer
	code, err := moveSession(session, config, filepath.Join(home, "absent.json"), MoveOptions{Host: "desk", DryRun: true}, there, &out)
	if code != 0 || err != nil {
		t.Fatalf("dry run: %d %v\n%s", code, err, out.String())
	}
	if len(there.copies) != 0 {
		t.Fatalf("dry run copied %v", there.copies)
	}
	for _, want := range []string{"copy     ~/.claude-cc2/projects/-work/s1.jsonl", "copy     ~/.claude-cc2/projects/-work/s1\n",
		"copy     ~/.cache/agentbridge/handoffs/h.txt", "merge    ~/.claude-cc2/projects/-work/memory",
		"CLAUDE_CONFIG_DIR='" + account.Home + "' claude --resume s1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out.String())
		}
	}

	// The same copy is already there (an earlier move): fine, and copied again.
	there = &loopback{patch: map[string]string{"tool": "yes", "login": "yes"}}
	out.Reset()
	if code, err := moveSession(session, config, filepath.Join(home, "absent.json"), MoveOptions{Host: "desk"}, there, &out); code != 0 || err != nil {
		t.Fatalf("move: %d %v\n%s", code, err, out.String())
	}
	if len(there.copies) != 2 || there.copies[1][0] != "--update" || !strings.HasSuffix(there.copies[1][1], "memory") {
		t.Fatalf("copies %v", there.copies)
	}

	// A copy there that grew on its own stops the move.
	there = &loopback{patch: map[string]string{"tool": "yes", "login": "yes", "session_size": "999999"}}
	out.Reset()
	if code, _ := moveSession(session, config, filepath.Join(home, "absent.json"), MoveOptions{Host: "desk"}, there, &out); code != 2 || len(there.copies) != 0 {
		t.Fatalf("diverged copy: code %d, copies %v\n%s", code, there.copies, out.String())
	}
}

func TestResumeCommand(t *testing.T) {
	isolated := &Account{Tool: SourceClaude, Name: "cc2", Home: "/h/.claude-cc2"}
	if got := resumeCommand(Session{ID: "a", Source: SourceClaude, Directory: "/w", Account: isolated}, "/h/.claude-cc2"); got != "cd '/w' && CLAUDE_CONFIG_DIR='/h/.claude-cc2' claude --resume a" {
		t.Error(got)
	}
	if got := resumeCommand(Session{ID: "a", Source: SourceClaude, Directory: "/w"}, "/h/.claude"); got != "cd '/w' && claude --resume a" {
		t.Error("default account must not set CLAUDE_CONFIG_DIR:", got)
	}
	if got := resumeCommand(Session{ID: "b", Source: SourceCodex, Directory: "/w"}, "/h/.codex"); got != "cd '/w' && codex resume b" {
		t.Error(got)
	}
}

func TestAnchorAtHome(t *testing.T) {
	got, err := anchorAtHome("/home/u", []string{"/home/u/.claude-cc2/projects/-p/s.jsonl", "/home/u/.agents/skills/x/"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/u/./.claude-cc2/projects/-p/s.jsonl", "/home/u/./.agents/skills/x/"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v", got)
	}
	for _, outside := range []string{"/etc/passwd", "/home/u", "/home/user2/x"} {
		if _, err := anchorAtHome("/home/u", []string{outside}); err == nil {
			t.Errorf("%s was accepted", outside)
		}
	}
}

// The copy itself, through real rsync into a stand-in "remote" home: the
// failure this guards against (rsync recreating and touching /home) only shows
// with a real rsync and --relative.
func TestCopyKeepsBelowHome(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(home, ".claude-cc2", "projects", "-p", "s.jsonl")
	writeFile(t, src, "{}\n")
	dest := t.TempDir()
	anchored, err := anchorAtHome(home, []string{src})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("rsync", append(append([]string{"-a", "--relative", "--"}, anchored...), dest+"/")...).CombinedOutput()
	if err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dest, ".claude-cc2", "projects", "-p", "s.jsonl")); err != nil {
		t.Fatalf("not copied below the destination home: %v", err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 1 || entries[0].Name() != ".claude-cc2" {
		t.Fatalf("copied more than the path below home: %v", entries)
	}
}
