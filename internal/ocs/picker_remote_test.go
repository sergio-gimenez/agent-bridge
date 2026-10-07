package ocs

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// remoteFixture is a cc2 session in ~/phd and a picker set up to push it and
// browse "desk" and "sna", both of which are this machine (a loopback), so the
// probe scripts really run. Background work runs inline: no async channel.
func remoteFixture(t *testing.T) (*picker, *loopback, *string, Session) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGB_CACHE_PATH", filepath.Join(home, ".cache", "agentbridge", "index.gob"))
	t.Setenv("AGB_DRY_RUN", "")
	installed := lookPath
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	t.Cleanup(func() { lookPath = installed })

	account := Account{Tool: SourceClaude, Name: "cc2", Home: filepath.Join(home, ".claude-cc2")}
	workdir := filepath.Join(home, "phd")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(account.Home, "projects", claudeProjectKey(workdir), "s1-0000.jsonl")
	writeFile(t, transcript, `{"type":"user","cwd":"`+workdir+`","message":{"content":"hi"}}`+"\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(transcript, old, old); err != nil {
		t.Fatal(err)
	}
	session := Session{ID: "s1-0000", Title: "Herdr setup", Source: SourceClaude, Directory: workdir, Account: &account,
		FilePath: transcript, UpdatedAtMs: float64(old.UnixMilli())}
	config := Config{ClaudeAccounts: []Account{account}, Arrive: map[string]string{"desk": "land {id}"}}

	link := &loopback{patch: map[string]string{"tool": "yes", "login": "yes"}}
	dialled := new(string)
	p := &picker{sessions: []Session{session}, targets: config.Targets(), pinned: -1,
		clock: func() time.Time { return old.Add(time.Hour) },
		options: PickOptions{Targets: config.Targets(), Config: config, Hosts: []string{"desk", "sna"}, Self: "laptop",
			Dial: func(host string, _ io.Writer) remote { *dialled = host; return listingRemote{link} }}}
	p.refilter()
	return p, link, dialled, session
}

// listingRemote answers `agb --print --json` with a canned listing and runs
// every other script like loopback.
type listingRemote struct{ *loopback }

var cannedListing string

func (l listingRemote) Probe(script string) (string, error) {
	if strings.Contains(script, "agb --print --json") {
		return cannedListing, nil
	}
	return l.loopback.Probe(script)
}

func screen(p *picker) string { return ansiPattern.ReplaceAllString(p.frame(150, 34), "") }

func press(t *testing.T, p *picker, keys ...Key) *PickResult {
	t.Helper()
	var result *PickResult
	for _, key := range keys {
		r, err := p.handle(key)
		if err != nil {
			t.Fatalf("%+v: %v", key, err)
		}
		if r != nil {
			result = r
		}
	}
	return result
}

var (
	ctrlO = Key{Name: "o", Ctrl: true}
	ctrlR = Key{Name: "r", Ctrl: true}
	ctrlK = Key{Name: "k", Ctrl: true}
	enter = Key{Name: "enter"}
	esc   = Key{Name: "escape"}
	right = Key{Name: "right"}
	left  = Key{Name: "left"}
)

func TestPushPanel(t *testing.T) {
	linuxOnly(t)
	p, link, dialled, _ := remoteFixture(t)
	press(t, p, ctrlO)
	if p.panel == nil || p.panel.plan == nil || *dialled != "desk" {
		t.Fatalf("panel %+v, dialled %q", p.panel, *dialled)
	}
	got := screen(p)
	for _, want := range []string{"Push to desk", "Herdr setup", "‹ desk ›", "sna", "✓ ~/phd exists on desk", "✓ not open here or on desk",
		"sends transcript", "then", "arrive hook on desk", "enter push", "←→ host", "esc back"} {
		if !strings.Contains(got, want) {
			t.Errorf("panel lacks %q:\n%s", want, got)
		}
	}

	// ←→ switch host and check again.
	press(t, p, right)
	if *dialled != "sna" || !strings.Contains(screen(p), "‹ sna ›") {
		t.Fatalf("right: dialled %q\n%s", *dialled, screen(p))
	}
	press(t, p, left)

	press(t, p, enter)
	if len(link.copies) == 0 || len(link.runs) != 1 || link.runs[0] != "land 's1-0000'" {
		t.Fatalf("copies %v, runs %v", link.copies, link.runs)
	}
	if got := screen(p); !strings.Contains(got, "Pushed to desk") || !strings.Contains(got, "arrive hook ran") {
		t.Fatalf("after push:\n%s", got)
	}
	press(t, p, enter)
	if p.panel != nil {
		t.Fatal("enter after the push does not close the panel")
	}
}

func TestPushPanelBlocked(t *testing.T) {
	linuxOnly(t)
	p, link, _, _ := remoteFixture(t)
	link.patch["open_there"] = "77"
	press(t, p, ctrlO)
	got := screen(p)
	if !strings.Contains(got, "✗ the session is open on desk (pid 77)") || strings.Contains(got, "enter push") {
		t.Fatalf("blocked panel:\n%s", got)
	}
	press(t, p, enter)
	if len(link.copies) != 0 {
		t.Fatalf("pushed despite a stop: %v", link.copies)
	}
	press(t, p, esc)
	if p.panel != nil {
		t.Fatal("esc does not close the panel")
	}
}

// Open here: ^k ends it, checks again and pushes.
func TestPushPanelStopsTheLocalSession(t *testing.T) {
	linuxOnly(t)
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	p, link, _, _ := remoteFixture(t)
	running := exec.Command("bash", "-c", "sleep 30; :", "claude", "--resume", "s1-0000")
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = running.Process.Kill(); _, _ = running.Process.Wait() })
	var stopped int
	p.stop = func(pid int) error {
		stopped = pid
		_ = running.Process.Kill()
		_, _ = running.Process.Wait()
		return nil
	}

	press(t, p, ctrlO)
	if got := screen(p); !strings.Contains(got, fmt.Sprintf("open here (pid %d)", running.Process.Pid)) || !strings.Contains(got, "^k stop it here & push") {
		t.Fatalf("open here:\n%s", got)
	}
	press(t, p, ctrlK)
	if stopped != running.Process.Pid || len(link.copies) == 0 || !strings.Contains(screen(p), "Pushed to desk") {
		t.Fatalf("stopped %d, copies %v\n%s", stopped, link.copies, screen(p))
	}
}

func TestBrowseAndPull(t *testing.T) {
	linuxOnly(t)
	p, link, dialled, session := remoteFixture(t)
	var listing bytes.Buffer
	open := session
	open.ID, open.Title, open.FilePath = "s2-0000", "Busy on desk", filepath.Join(filepath.Dir(session.FilePath), "s2-0000.jsonl")
	if err := WriteListing(&listing, []Session{session, open}, func(s Session) int {
		if s.ID == "s2-0000" {
			return 55
		}
		return 0
	}); err != nil {
		t.Fatal(err)
	}
	cannedListing = listing.String()

	press(t, p, ctrlR)
	if p.view == nil || p.view.host() != "desk" || *dialled != "desk" || len(p.sessions) != 2 {
		t.Fatalf("view %+v, dialled %q, sessions %d", p.view, *dialled, len(p.sessions))
	}
	got := screen(p)
	for _, want := range []string{"laptop", "▸ desk", "Busy on desk", "enter pull & resume here", "^r sna"} {
		if !strings.Contains(got, want) {
			t.Errorf("browsing desk lacks %q:\n%s", want, got)
		}
	}

	// Open there: Enter says so and pulls nothing.
	press(t, p, Key{Text: "busy"})
	if result := press(t, p, enter); result != nil || len(link.fetches) != 0 {
		t.Fatalf("pulled an open session: %+v %v", result, link.fetches)
	}
	if got := screen(p); !strings.Contains(got, "open on desk (pid 55)") || strings.Contains(got, "enter pull") {
		t.Fatalf("an open session still offers a pull:\n%s", got)
	}

	// Not open: Enter pulls it and resumes it here, natively.
	press(t, p, Key{Name: "backspace"}, Key{Name: "backspace"}, Key{Name: "backspace"}, Key{Name: "backspace"}, Key{Text: "herdr"})
	result := press(t, p, enter)
	if result == nil || result.Session.ID != "s1-0000" || result.Session.FilePath != session.FilePath || result.Target.Name != "cc2" ||
		result.Mode != ModeResume || result.PulledFrom != "desk" || len(link.fetches) == 0 {
		t.Fatalf("result %+v, fetches %v\n%s", result, link.fetches, screen(p))
	}
}

func TestBrowseCyclesBackHome(t *testing.T) {
	linuxOnly(t)
	p, _, dialled, _ := remoteFixture(t)
	cannedListing = "[]\n"
	press(t, p, ctrlR, ctrlR)
	if p.view == nil || *dialled != "sna" {
		t.Fatalf("second ^r: view %+v, dialled %q", p.view, *dialled)
	}
	press(t, p, ctrlR)
	if p.view != nil || len(p.sessions) != 1 {
		t.Fatalf("third ^r is not back home: view %+v, %d sessions", p.view, len(p.sessions))
	}
	// Esc while browsing goes home too, rather than quitting.
	press(t, p, ctrlR, esc)
	if p.view != nil {
		t.Fatal("esc while browsing did not go home")
	}
}
