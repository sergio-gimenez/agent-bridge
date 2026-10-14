package ocs

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextMenuSelectsClickedConversation(t *testing.T) {
	p := mousePicker()
	p.frame(80, 12)
	p.handle(Key{Mouse: &MouseEvent{X: 5, Y: 5, Button: 2}})
	if p.menu == nil || p.active != 2 {
		t.Fatal("right click did not select conversation and open menu")
	}
	frame := p.frame(80, 12)
	if !strings.Contains(frame, "Actions") {
		t.Fatal("menu was not drawn")
	}
	var wanted int
	for i, item := range p.menu.items {
		if item.label == "Open in Codex CX1" {
			wanted = i
		}
	}
	if wanted == 0 {
		t.Fatal("menu has no Codex destination")
	}
	p.menu.active = wanted
	p.frame(80, 12)
	menu := p.menu
	result := clickMouse(t, p, menu.x+2, menu.y+1+wanted-menu.offset)
	if result == nil || result.Session.ID != "c" || result.Target != cx1 || p.menu != nil {
		t.Fatalf("wrong context route: %+v", result)
	}
}

func TestContextMenuDismissalAndScrolling(t *testing.T) {
	p := mousePicker()
	p.openMenu(70, 10)
	p.frame(40, 8)
	if p.menu.x+p.menu.width-1 > 39 || p.menu.y+p.menu.height-1 >= p.mouseFooterRow {
		t.Fatal("menu outside screen")
	}
	p.handle(Key{Mouse: &MouseEvent{X: 2, Y: 3, Button: 65}})
	if p.menu.active != 1 {
		t.Fatal("menu wheel did not move")
	}
	p.handle(Key{Name: "end"})
	p.frame(40, 8)
	if p.menu.offset == 0 {
		t.Fatal("menu did not scroll to reveal last action")
	}
	clickMouse(t, p, 40, p.mouseFooterRow-1)
	if p.menu != nil {
		t.Fatal("outside click did not dismiss menu")
	}
	// An outside click must not fall through to the conversation below.
	p.openMenu(2, 3)
	p.frame(120, 20)
	if got := clickMouse(t, p, 100, 3); got != nil || p.menu != nil {
		t.Fatal("outside click launched a conversation")
	}
}

func TestNewConversationWithoutExistingSessions(t *testing.T) {
	dir := t.TempDir()
	p := &picker{targets: targets, pinned: -1, options: PickOptions{WorkDirectory: dir}}
	p.frame(80, 20)
	clickHint(t, p, Key{Name: "n", Ctrl: true})
	if p.newConversation == nil || p.newConversation.directory != dir {
		t.Fatal("cannot create first conversation")
	}
	p.frame(80, 20)
	clickHint(t, p, Key{Name: "t", Ctrl: true}) // OC -> CC1
	p.frame(80, 20)
	clickHint(t, p, Key{Name: "y", Ctrl: true})
	p.frame(80, 20)
	got := clickHint(t, p, enter)
	if got == nil || got.Mode != ModeNew || got.Target != cc1 || got.Session.Directory != dir || got.Session.ID != "" || got.SkipPermissions == nil || !*got.SkipPermissions {
		t.Fatalf("new result: %+v", got)
	}
}

func TestNewDirectoryValidationAndCancel(t *testing.T) {
	p := mousePicker()
	dir := t.TempDir()
	p.openNew(filepath.Join(dir, "missing"))
	if result := press(t, p, enter); result != nil || p.newConversation.err == "" {
		t.Fatal("accepted missing directory")
	}
	press(t, p, Key{Name: "u", Ctrl: true}, enter)
	if p.newConversation.err != "Enter a project directory" {
		t.Fatal("accepted empty directory")
	}
	press(t, p, Key{Text: dir}, Key{Name: "t", Ctrl: true})
	result := press(t, p, enter)
	if result == nil || result.Mode != ModeNew || result.Session.Directory != dir {
		t.Fatalf("valid directory failed: %+v", result)
	}
	p.openNew(dir)
	query, active := p.query, p.active
	press(t, p, esc)
	if p.newConversation != nil || p.query != query || p.active != active {
		t.Fatal("cancel changed session list")
	}
}

func TestContextMenuCanStartNewInProject(t *testing.T) {
	p := mousePicker()
	dir := t.TempDir()
	p.filtered[0].Directory = dir
	p.openMenu(2, 3)
	for i, item := range p.menu.items {
		if strings.HasPrefix(item.label, "New conversation") {
			p.menu.active = i
		}
	}
	press(t, p, enter)
	if p.menu != nil || p.newConversation == nil || p.newConversation.directory != dir {
		t.Fatal("context new did not use conversation's project")
	}
}

func TestOpenNewUsesBlankInteractiveCommand(t *testing.T) {
	t.Setenv("AGB_DRY_RUN", "1")
	dir := t.TempDir()
	var out bytes.Buffer
	previous := dryRunOut
	dryRunOut = &out
	t.Cleanup(func() { dryRunOut = previous })
	for _, tc := range []struct {
		target  Account
		skip    bool
		command string
	}{
		{OpencodeAccount, false, "opencode\n"},
		{cc1, false, "claude\n"},
		{cc2, true, "CLAUDE_CONFIG_DIR=/tmp/.claude-cc2 claude --dangerously-skip-permissions\n"},
		{cx1, false, "codex\n"},
		{cx2, true, "CODEX_HOME=/tmp/.codex-cx2 codex --dangerously-bypass-approvals-and-sandbox\n"},
	} {
		out.Reset()
		code, err := OpenNew(dir, tc.target, OpenOptions{SkipPermissions: tc.skip})
		if err != nil || code != 0 || !strings.Contains(out.String(), tc.command) {
			t.Fatalf("%+v: %s, %v", tc.target, out.String(), err)
		}
		if strings.Contains(out.String(), "resume") || strings.Contains(out.String(), "--session") || strings.Contains(out.String(), "--prompt") {
			t.Fatal("new conversation got a session or seed")
		}
	}
	if code, err := OpenNew(filepath.Join(dir, "missing"), cc1, OpenOptions{}); code == 0 || err == nil {
		t.Fatal("launched in nonexistent directory")
	}
}

func TestParseNewArgs(t *testing.T) {
	dir, args, err := ParseNewArgs([]string{"--dir", "/tmp/project with spaces", "--target", "CX2", "--safe"})
	if err != nil || dir != "/tmp/project with spaces" || args.Target != "cx2" || args.SkipPermissions == nil || *args.SkipPermissions {
		t.Fatalf("got %q, %+v, %v", dir, args, err)
	}
	for _, argv := range [][]string{{"--target"}, {"--dir", "--safe"}, {"--unknown"}, {"unexpected"}, {"--dir", ""}} {
		if _, _, err := ParseNewArgs(argv); err == nil {
			t.Fatalf("accepted %+v", argv)
		}
	}
}
