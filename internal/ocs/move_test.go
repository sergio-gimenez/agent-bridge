package ocs

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// linuxOnly skips tests that run push and pull for real: their scripts use
// GNU stat and find, /proc, and rsync --mkpath, which macOS lacks. Push and
// pull need Linux on both machines for now (docs/push-pull.md).
func linuxOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("push and pull need Linux")
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
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
		Session:     Session{ID: "s1", Source: SourceClaude, Directory: "/work/p"},
		RemoteDir:   "/work/p",
		Home:        filepath.Join(home, ".claude"),
		Transcript:  []byte("a\nb\n"),
		WrittenAgo:  time.Hour,
		CanSeeProcs: true,
		Remote:      map[string]string{"home": home, "dir": "yes", "tool": "yes", "login": "yes"},
	}
}

func TestDecideMove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	check := func(name string, mutate func(*moveFacts), opts MoveOptions, wantBlock, wantWarn string) moveDecision {
		t.Helper()
		f := baseFacts(home)
		mutate(&f)
		opts.Host = "desk"
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
	// What passed is listed too, for the picker's push panel.
	clean := check("clean", none, MoveOptions{}, "", "")
	passed := strings.Join(clean.Passed, "|")
	for _, want := range []string{"/work/p exists on desk", "Claude Code on desk, CC logged in", "not open here or on desk", "no copy on desk yet"} {
		if !strings.Contains(passed, want) {
			t.Errorf("passed %q lacks %q", passed, want)
		}
	}
	check("other home", func(f *moveFacts) { f.Remote["home"] = "/home/other" }, MoveOptions{}, "same home path", "")
	check("no directory", func(f *moveFacts) { f.Remote["dir"] = "no" }, MoveOptions{}, "does not exist on desk", "")
	check("no tool", func(f *moveFacts) { f.Remote["tool"] = "no" }, MoveOptions{}, "not installed", "")
	check("logged out", func(f *moveFacts) { f.Remote["login"] = "no" }, MoveOptions{}, "", "logged in")
	// A bigger copy there went on without this one: replacing it loses turns.
	check("copy there is bigger", func(f *moveFacts) { f.Remote["session_size"] = "99" }, MoveOptions{}, "bigger on desk (99 B vs 4 B)", "")
	check("copy there is bigger, forced", func(f *moveFacts) { f.Remote["session_size"] = "99" }, MoveOptions{Force: true}, "", "--force")
	check("older copy there", func(f *moveFacts) { f.Remote["session_size"] = "2" }, MoveOptions{}, "", "")
	check("open here", func(f *moveFacts) { f.OpenPID = 4242 }, MoveOptions{}, "open here (pid 4242)", "")
	check("open here, forced", func(f *moveFacts) { f.OpenPID = 4242 }, MoveOptions{Force: true}, "", "--force")
	check("just written, but no process has it", func(f *moveFacts) { f.WrittenAgo = time.Second }, MoveOptions{}, "", "")
	check("no /proc, just written", func(f *moveFacts) { f.CanSeeProcs, f.WrittenAgo = false, time.Second }, MoveOptions{}, "probably still open", "")
	check("open there", func(f *moveFacts) { f.Remote["open_there"] = "77" }, MoveOptions{Force: true}, "open on desk (pid 77)", "")
	// Code travels through git, which agb leaves to you: a note, never a stop.
	check("dirty", func(f *moveFacts) { f.GitRepo, f.GitDirty = true, true }, MoveOptions{}, "", "uncommitted")
	check("unpushed", func(f *moveFacts) { f.GitRepo, f.GitUnpushed = true, true }, MoveOptions{}, "", "on no remote branch")
	check("other checkout", func(f *moveFacts) { f.GitRepo, f.GitHead, f.Remote["git_head"] = true, "aaa", "bbb" }, MoveOptions{}, "", "pull or switch")
}

func TestArriveCommand(t *testing.T) {
	session := Session{ID: "s1", Title: "it's a title", Directory: "/w/p q"}
	got := arriveCommand(`herdr tab create --label {title} --cwd {dir} && herdr pane run "$p" {resume} # {id} {other}`, session, "cd '/w/p q' && claude --resume s1")
	want := `herdr tab create --label 'it'\''s a title' --cwd '/w/p q' && herdr pane run "$p" 'cd '\''/w/p q'\'' && claude --resume s1' # 's1' {other}`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// loopback is a "remote" that is this machine: the probe script really runs, so
// it is tested too; copies are recorded instead of made.
type loopback struct {
	patch   map[string]string
	copies  [][]string
	fetches [][]string
	runs    []string
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

func (l *loopback) CopyTo(src, dst string, flags ...string) error {
	l.copies = append(l.copies, append(append([]string{}, flags...), src+" -> "+dst))
	return nil
}

func (l *loopback) Fetch(src, dst string, flags ...string) error {
	l.fetches = append(l.fetches, append(append([]string{}, flags...), src+" -> "+dst))
	return nil
}

func (l *loopback) Run(command string) error { l.runs = append(l.runs, command); return nil }

func TestMoveSession(t *testing.T) {
	linuxOnly(t)
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
	code, err := moveSession(session, config, MoveOptions{Host: "desk", DryRun: true}, there, &out)
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
	if code, err := moveSession(session, config, MoveOptions{Host: "desk"}, there, &out); code != 0 || err != nil {
		t.Fatalf("move: %d %v\n%s", code, err, out.String())
	}
	// Its own files overwrite; what it only refers to (a handoff, another
	// session's transcript) and memory keep a newer copy there, since another
	// session may be carrying on with them on that machine.
	if len(there.copies) != 3 || strings.Join(there.copies[0], " ") != strings.TrimSuffix(transcriptPath, ".jsonl")+" "+transcriptPath ||
		strings.Join(there.copies[1], " ") != "--update "+handoff ||
		there.copies[2][0] != "--update" || !strings.HasSuffix(there.copies[2][1], "memory") {
		t.Fatalf("copies %v", there.copies)
	}

	// Without an arrive hook it says how to resume there, and runs nothing.
	if len(there.runs) != 0 || !strings.Contains(out.String(), "ssh -t desk ") {
		t.Fatalf("no hook: runs %v\n%s", there.runs, out.String())
	}

	// A bigger copy there went on without this one: a stop, unless forced.
	there = &loopback{patch: map[string]string{"tool": "yes", "login": "yes", "session_size": "999999"}}
	out.Reset()
	if code, _ := moveSession(session, config, MoveOptions{Host: "desk"}, there, &out); code != 2 || len(there.copies) != 0 {
		t.Fatalf("bigger copy there: code %d, copies %v\n%s", code, there.copies, out.String())
	}
	there = &loopback{patch: map[string]string{"tool": "yes", "login": "yes", "session_size": "999999"}}
	if code, err := moveSession(session, config, MoveOptions{Host: "desk", Force: true}, there, &out); code != 0 || err != nil || len(there.copies) == 0 {
		t.Fatalf("bigger copy there, forced: code %d %v, copies %v", code, err, there.copies)
	}

	// The host's arrive hook runs there once the copy is done.
	hooked := config
	hooked.Arrive = map[string]string{"desk": "land {id} {resume}"}
	there = &loopback{patch: map[string]string{"tool": "yes", "login": "yes"}}
	out.Reset()
	if code, err := moveSession(session, hooked, MoveOptions{Host: "desk"}, there, &out); code != 0 || err != nil {
		t.Fatalf("hook: %d %v\n%s", code, err, out.String())
	}
	if len(there.runs) != 1 || !strings.HasPrefix(there.runs[0], "land 's1' 'cd ") || !strings.HasSuffix(there.runs[0], "claude --resume s1'") {
		t.Fatalf("hook runs %v", there.runs)
	}
	// --no-arrive skips it; a dry run never runs it.
	for _, opts := range []MoveOptions{{Host: "desk", NoArrive: true}, {Host: "desk", DryRun: true}} {
		there = &loopback{patch: map[string]string{"tool": "yes", "login": "yes"}}
		if _, err := moveSession(session, hooked, opts, there, &out); err != nil || len(there.runs) != 0 {
			t.Fatalf("%+v: %v, runs %v", opts, err, there.runs)
		}
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
	linuxOnly(t)
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

func TestClaudeProjectKey(t *testing.T) {
	for dir, want := range map[string]string{
		"/home/sergio/phd":                       "-home-sergio-phd",
		"/home/sergio/i2cat/GÉANT":               "-home-sergio-i2cat-G-ANT",
		"/home/sergio/mystuff/sergiogimenez.com": "-home-sergio-mystuff-sergiogimenez-com",
		"/home/u/a_b c":                          "-home-u-a-b-c",
	} {
		if got := claudeProjectKey(dir); got != want {
			t.Errorf("%s: got %s, want %s", dir, got, want)
		}
	}
}

// A different directory there moves the transcript, its folder and memory into
// that directory's project folder, and resumes there.
func TestMoveSessionToAnotherDirectory(t *testing.T) {
	linuxOnly(t)
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGB_CACHE_PATH", filepath.Join(home, ".cache", "agentbridge", "index.gob"))
	t.Setenv("AGB_DRY_RUN", "")
	account := Account{Tool: SourceClaude, Name: "cc2", Home: filepath.Join(home, ".claude-cc2")}
	here, there := filepath.Join(home, "phd"), filepath.Join(home, "src", "phd")
	for _, dir := range []string{here, there} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	projects := filepath.Join(account.Home, "projects")
	transcriptPath := filepath.Join(projects, claudeProjectKey(here), "s1.jsonl")
	writeFile(t, transcriptPath, "{}\n")
	writeFile(t, filepath.Join(projects, claudeProjectKey(here), "s1", "tool-results", "r.txt"), "r")
	writeFile(t, filepath.Join(projects, claudeProjectKey(here), "memory", "MEMORY.md"), "- fact")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(transcriptPath, old, old); err != nil {
		t.Fatal(err)
	}
	session := Session{ID: "s1", Source: SourceClaude, Directory: here, Account: &account, FilePath: transcriptPath}
	link := &loopback{patch: map[string]string{"tool": "yes", "login": "yes"}}
	var out bytes.Buffer
	code, err := moveSession(session, Config{ClaudeAccounts: []Account{account}},
		MoveOptions{Host: "desk", RemoteDir: there}, link, &out)
	if code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out.String())
	}
	dest := filepath.Join(projects, claudeProjectKey(there))
	got := fmt.Sprint(link.copies)
	for _, want := range []string{
		transcriptPath + " -> " + filepath.Join(dest, "s1.jsonl"),
		filepath.Join(projects, claudeProjectKey(here), "s1") + "/ -> " + filepath.Join(dest, "s1") + "/",
		"--update " + filepath.Join(projects, claudeProjectKey(here), "memory") + "/ -> " + filepath.Join(dest, "memory") + "/",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("copies lack %q:\n%s", want, got)
		}
	}
	if !strings.Contains(out.String(), "there in ~/src/phd") || !strings.Contains(out.String(), there+"'\\'' && CLAUDE_CONFIG_DIR") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestOpenSession(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	home := t.TempDir()
	transcript := filepath.Join(home, "projects", "-p", "s1.jsonl")
	writeFile(t, transcript, "{}\n")
	session := Session{ID: "s1", Source: SourceClaude, FilePath: transcript}
	self := os.Getpid()
	start, _ := procStart(self)
	record := func(pid int, id, start string) {
		writeFile(t, filepath.Join(home, "sessions", fmt.Sprintf("%d.json", pid)),
			fmt.Sprintf(`{"pid":%d,"sessionId":%q,"procStart":%q}`, pid, id, start))
	}

	if pid, ok := openSession(home, session); !ok || pid != 0 {
		t.Fatalf("no record, no open file: got %d %v", pid, ok)
	}
	record(self, "other-session", start)
	if pid, _ := openSession(home, session); pid != 0 {
		t.Fatalf("another session's record counted: %d", pid)
	}
	record(self, "s1", start+"0")
	if pid, _ := openSession(home, session); pid != 0 {
		t.Fatalf("a record with another start time (a reused pid) counted: %d", pid)
	}
	record(self, "s1", start)
	if pid, _ := openSession(home, session); pid != self {
		t.Fatalf("a live record: got %d, want %d", pid, self)
	}
	if err := os.RemoveAll(filepath.Join(home, "sessions")); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(transcript)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if pid, _ := openSession(home, Session{ID: "s1", Source: SourceCodex, FilePath: transcript}); pid != self {
		t.Fatalf("a held transcript: got %d, want %d", pid, self)
	}
}

// A tool started to resume the session counts as open before it writes any
// record or opens the transcript (Claude Code waiting at its trust prompt).
func TestOpenSessionByCommandLine(t *testing.T) {
	linuxOnly(t)
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	home := t.TempDir()
	transcript := filepath.Join(home, "projects", "-p", "0c5d-1.jsonl")
	writeFile(t, transcript, "{}\n")
	resumer := exec.Command("bash", "-c", "sleep 30; :", "claude", "--resume", "0c5d-1")
	if err := resumer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resumer.Process.Kill(); _, _ = resumer.Process.Wait() })
	// Something merely mentioning the id, or another id it prefixes, does not count.
	bystander := exec.Command("bash", "-c", "sleep 30; :", "agb", "push", "desk", "0c5d-1", "--resume", "0c5d-10")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bystander.Process.Kill(); _, _ = bystander.Process.Wait() })

	for _, source := range []Source{SourceClaude, SourceCodex} {
		if pid, _ := openSession(home, Session{ID: "0c5d-1", Source: source, FilePath: transcript}); pid != resumer.Process.Pid {
			t.Errorf("%s here: got pid %d, want %d", source, pid, resumer.Process.Pid)
		}
	}
	if pid, _ := openSession(home, Session{ID: "0c5d-2", Source: SourceClaude, FilePath: transcript}); pid != 0 {
		t.Errorf("another session counted: %d", pid)
	}

	out, err := exec.Command("bash", "-c", "kv() { printf '%s=%s\\n' \"$1\" \"$2\"; }\n"+
		openThereScript(Session{ID: "0c5d-1", Source: SourceClaude}, home, transcript)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := parseProbe(string(out))["open_there"]; got != strconv.Itoa(resumer.Process.Pid) {
		t.Errorf("there: open_there=%q, want %d\n%s", got, resumer.Process.Pid, out)
	}
}
