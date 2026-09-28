package ocs

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	cc1     = Account{Tool: SourceClaude, Name: "cc1"}
	cc2     = Account{Tool: SourceClaude, Name: "cc2", Home: "/tmp/.claude-cc2"}
	cx1     = Account{Tool: SourceCodex, Name: "cx1"}
	cx2     = Account{Tool: SourceCodex, Name: "cx2", Home: "/tmp/.codex-cx2"}
	targets = []Account{OpencodeAccount, cc1, cc2, cx1}
)

func claudeTestSession() Session {
	account := cc1
	return Session{ID: "sid", Title: "Old session", Directory: "/tmp/project", Source: SourceClaude, Account: &account}
}

func codexTestSession() Session {
	account := cx1
	session := claudeTestSession()
	session.Source, session.Account = SourceCodex, &account
	return session
}

func opencodeTestSession() Session {
	session := claudeTestSession()
	session.Source, session.Account = SourceOpencode, nil
	return session
}

func ptr[T any](value T) *T { return &value }

func TestRouteBadge(t *testing.T) {
	session, codex, opencode := claudeTestSession(), codexTestSession(), opencodeTestSession()
	cc2Session := claudeTestSession()
	cc2Session.Account = ptr(cc2)

	cases := []struct {
		session *Session
		target  *Account
		want    string
	}{
		{&session, &cc2, "[CC1→CC2]"},
		{&session, &cc1, "[CC1]"},
		{&cc2Session, &cc1, "[CC2→CC1]"},
		{&opencode, &cx1, "[OC→CX1]"},
		{&codex, &cc2, "[CX1→CC2]"},
		{&session, &OpencodeAccount, "[CC1→OC]"},
		{&opencode, &OpencodeAccount, "[OC]"},
		{&codex, &cx1, "[CX1]"},
		{&codex, &cx2, "[CX1→CX2]"},
		// A row only shows a route once a destination is pinned.
		{&opencode, BadgeTarget(&cx1, false), "[OC]"},
		{&opencode, BadgeTarget(&cx1, true), "[OC→CX1]"},
	}
	for _, c := range cases {
		if got := SessionBadgeText(c.session, c.target); got != c.want {
			t.Errorf("badge = %s, want %s", got, c.want)
		}
	}
}

func TestEnterDestination(t *testing.T) {
	session, codex := claudeTestSession(), codexTestSession()

	if got := EnterDestination(session, &cc2, ModeResume); got.Target != cc2 || got.Mode != ModeResume {
		t.Fatalf("got %+v", got)
	}
	if got := EnterDestination(session, &cc1, ModeFork); got.Mode != ModeFork {
		t.Fatalf("fork mode lost: %+v", got)
	}
	// No target configured: the session's own account.
	if got := EnterDestination(codex, nil, ModeResume); got.Target != cx1 {
		t.Fatalf("fallback = %+v", got.Target)
	}
}

func TestTargetCycling(t *testing.T) {
	if NextTarget(len(targets), 3, 1) != 0 || NextTarget(len(targets), 0, -1) != 3 {
		t.Fatal("cycling does not wrap")
	}
	if NextTarget(0, 0, 1) != 0 {
		t.Fatal("cycling an empty list moved")
	}

	// From OpenCode the next tool is Claude, and with no Claude target held it
	// takes the first configured Claude account.
	if got := NextToolTarget(targets, SourceOpencode, nil); *got != cc1 {
		t.Fatalf("got %+v", got)
	}
	// A held target on the tool being jumped to wins.
	if got := NextToolTarget(targets, SourceOpencode, &cc2); *got != cc2 {
		t.Fatalf("got %+v", got)
	}
	// From Codex it wraps round to OpenCode.
	if got := NextToolTarget(targets, SourceCodex, &cx1); *got != OpencodeAccount {
		t.Fatalf("got %+v", got)
	}
	// A tool without targets is skipped.
	if got := NextToolTarget([]Account{OpencodeAccount, cx1}, SourceClaude, nil); *got != cx1 {
		t.Fatalf("got %+v", got)
	}
	if NextToolTarget(nil, SourceClaude, nil) != nil {
		t.Fatal("no targets should give none")
	}
}

func TestDecodeKeys(t *testing.T) {
	cases := map[string][]Key{
		"\r":            {{Name: "enter"}},
		"\n":            {{Name: "enter"}},
		"a":             {{Text: "a"}},
		"ñ":             {{Text: "ñ"}},
		"\x1b":          {{Name: "escape"}},
		"\x1b[A\x1b[B":  {{Name: "up"}, {Name: "down"}},
		"\x1bOA":        {{Name: "up"}},
		"\x1b[5~":       {{Name: "pageup"}},
		"\x1b[6~":       {{Name: "pagedown"}},
		"\x1b[Z":        {{Name: "backtab"}},
		"\x1b[1;5A":     {{Name: "up"}},
		"\x14":          {{Name: "t", Ctrl: true}},
		"\x19":          {{Name: "y", Ctrl: true}},
		"\x7f":          {{Name: "backspace"}},
		"\t":            {{Name: "tab"}},
		"\x1bb":         nil, // Alt+b never reaches the query
		"mesh vpn\r":    {{Text: "m"}, {Text: "e"}, {Text: "s"}, {Text: "h"}, {Text: " "}, {Text: "v"}, {Text: "p"}, {Text: "n"}, {Name: "enter"}},
		"\x1b[200~x":    {{Text: "x"}},
		"\x1b[3~\x1b[H": {{Name: "delete"}, {Name: "home"}},
	}
	for input, want := range cases {
		if got := DecodeKeys([]byte(input)); !reflect.DeepEqual(got, want) {
			t.Errorf("DecodeKeys(%q) = %+v, want %+v", input, got, want)
		}
	}
}

func newTestPicker(sessions []Session, skip func(Account) bool) *picker {
	p := &picker{sessions: sessions, targets: targets, pinned: -1,
		options: PickOptions{Targets: targets, SkipPermissions: skip}}
	p.refilter()
	return p
}

func TestPickerKeys(t *testing.T) {
	sessions := []Session{claudeTestSession(), codexTestSession()}
	sessions[1].SearchText = "wireguard"

	p := newTestPicker(sessions, nil)
	for _, key := range DecodeKeys([]byte("wire")) {
		p.handle(key)
	}
	if len(p.filtered) != 1 {
		t.Fatalf("filtered = %d", len(p.filtered))
	}
	// Enter while the target follows the selection resumes where it lives.
	result, _ := p.handle(Key{Name: "enter"})
	if result == nil || result.Target != cx1 || result.SkipPermissions != nil {
		t.Fatalf("result = %+v", result)
	}

	if _, err := newTestPicker(sessions, nil).handle(Key{Name: "escape"}); err != ErrCancelled {
		t.Fatalf("escape gave %v", err)
	}
}

func TestPickerYoloToggle(t *testing.T) {
	sessions := []Session{claudeTestSession()}
	skipCodexOnly := func(target Account) bool { return target.Tool == SourceCodex }

	p := newTestPicker(sessions, skipCodexOnly)
	if strings.Contains(p.frame(120, 40), "YOLO") {
		t.Fatal("YOLO shown before it was asked for")
	}
	p.handle(Key{Name: "y", Ctrl: true})
	if !strings.Contains(p.frame(120, 40), "YOLO") {
		t.Fatal("Ctrl+Y did not turn YOLO on")
	}
	result, _ := p.handle(Key{Name: "enter"})
	if result.SkipPermissions == nil || !*result.SkipPermissions {
		t.Fatalf("yolo not carried to the launch: %+v", result)
	}

	// Without Ctrl+Y the target's own default shows, and nothing is forced.
	p = newTestPicker(sessions, skipCodexOnly)
	p.pinned = 3 // cx1
	if !strings.Contains(p.frame(120, 40), "YOLO") {
		t.Fatal("a yolo-by-default target should show YOLO")
	}
	result, _ = p.handle(Key{Name: "enter"})
	if result.SkipPermissions != nil {
		t.Fatal("the default should be left to the caller")
	}
}

func TestHighlightNeverSplitsEscapes(t *testing.T) {
	got := highlightTerms("Mesh VPN mesh", []string{"mesh"})
	want := yellow("Mesh") + " VPN " + yellow("mesh")
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.Local)
	ago := func(d time.Duration) float64 { return float64(now.Add(-d).UnixMilli()) }
	cases := map[float64]string{
		ago(20 * time.Second):     "now",
		ago(12 * time.Minute):     "12m",
		ago(3 * time.Hour):        "3h",
		ago(49 * time.Hour):       "2d",
		ago(20 * 24 * time.Hour):  "Sep 8",
		ago(400 * 24 * time.Hour): "2025",
	}
	for ms, want := range cases {
		if got := relativeTime(ms, now); got != want {
			t.Errorf("relativeTime = %q, want %q", got, want)
		}
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("the quick brown fox jumps over", 10)
	if !reflect.DeepEqual(got, []string{"the quick", "brown fox", "jumps over"}) {
		t.Fatalf("got %q", got)
	}
	// A word longer than the line is split rather than overflowing.
	for _, line := range wrapText("supercalifragilistic", 8) {
		if len(line) > 8 {
			t.Fatalf("line %q is too wide", line)
		}
	}
	// A two-cell character in a one-cell width must not loop forever.
	if got := wrapText("🍷", 1); len(got) != 1 {
		t.Fatalf("got %q", got)
	}
}

func TestIncompleteEscape(t *testing.T) {
	cases := map[string]int{
		"abc":        -1,
		"\x1b":       0,
		"ab\x1b":     2,
		"\x1b[":      0,
		"\x1b[1;5":   0,
		"\x1b[A":     -1,
		"x\x1bO":     1,
		"\x1b[A\x1b": 3,
	}
	for input, want := range cases {
		if got := incompleteEscape([]byte(input)); got != want {
			t.Errorf("incompleteEscape(%q) = %d, want %d", input, got, want)
		}
	}
}

func frameLines(t *testing.T, p *picker, cols, rows int) []string {
	t.Helper()
	frame := strings.TrimPrefix(p.frame(cols, rows), "\x1b[H")
	lines := strings.Split(frame, "\r\n")
	if len(lines) != rows {
		t.Fatalf("frame is %d rows, want %d", len(lines), rows)
	}
	for _, line := range lines {
		if width := visibleWidth(line); width > cols-1 {
			t.Fatalf("line is %d cells wide in a %d-column terminal: %q", width, cols, line)
		}
	}
	return lines
}

func TestFrameFitsEveryTerminalSize(t *testing.T) {
	session := claudeTestSession()
	session.Title = "Hermes 🍷 Vino " + strings.Repeat("very long title ", 20)
	session.Prompts = []string{strings.Repeat("prompt 漢字 ", 40)}
	session.AssistantSnippet = []string{"short"}
	sessions := []Session{session, codexTestSession(), opencodeTestSession()}

	for _, size := range [][2]int{{60, 10}, {80, 24}, {100, 30}, {160, 50}, {220, 12}} {
		p := newTestPicker(sessions, nil)
		frameLines(t, p, size[0], size[1])
		p.handle(Key{Name: "t", Ctrl: true})
		frameLines(t, p, size[0], size[1])
	}
}

func TestListScrollsToTheSelection(t *testing.T) {
	var sessions []Session
	for i := 0; i < 50; i++ {
		session := claudeTestSession()
		session.Title = "session " + strconv.Itoa(i)
		sessions = append(sessions, session)
	}
	p := newTestPicker(sessions, nil)
	for i := 0; i < 30; i++ {
		p.handle(Key{Name: "down"})
	}
	lines := frameLines(t, p, 120, 20)
	if !strings.Contains(strings.Join(lines, "\n"), "session 30") {
		t.Fatal("the selected row scrolled out of view")
	}
}

func TestRouteArrowOnlyWhenCrossing(t *testing.T) {
	native, codex := claudeTestSession(), codexTestSession()
	if strings.Contains(badgeCell(&native, &cc1, 0), "→") {
		t.Fatal("a native row should not show an arrow")
	}
	if !strings.Contains(badgeCell(&codex, &cc1, 0), "→") {
		t.Fatal("a crossing row should show an arrow")
	}
}
