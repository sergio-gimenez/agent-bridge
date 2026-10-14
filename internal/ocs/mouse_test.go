package ocs

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDecodeMouseReports(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  *MouseEvent
	}{
		{"\x1b[<0;12;3M", &MouseEvent{X: 12, Y: 3}},
		{"\x1b[<0;12;3m", &MouseEvent{X: 12, Y: 3, Release: true}},
		{"\x1b[<64;300;4M", &MouseEvent{X: 300, Y: 4, Button: 64}},
		{"\x1b[<65;2;4M", &MouseEvent{X: 2, Y: 4, Button: 65}},
		{"\x1b[M" + string([]byte{32, 44, 35}), &MouseEvent{X: 12, Y: 3}},
	} {
		got := DecodeKeys([]byte(tc.input + "x"))
		want := []Key{{Mouse: tc.want}, {Text: "x"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %+v, want %+v", tc.input, got, want)
		}
	}
	for _, input := range []string{"\x1b[<0;0;3M", "\x1b[<0;1M", "\x1b[<0;999999999999999999999;3M"} {
		if got := DecodeKeys([]byte(input)); len(got) != 0 {
			t.Errorf("malformed report became input: %+v", got)
		}
	}
	for _, input := range []string{"\x1b[<0;12;", "\x1b[M", "\x1b[M "} {
		if got := incompleteEscape([]byte("x" + input)); got != 1 {
			t.Errorf("split report %q starts at %d", input, got)
		}
	}
}

func mousePicker() *picker {
	sessions := make([]Session, 20)
	for i := range sessions {
		sessions[i] = claudeTestSession()
		sessions[i].ID = string(rune('a' + i))
	}
	p := &picker{sessions: sessions, targets: targets, pinned: -1}
	p.refilter()
	return p
}

func clickMouse(t *testing.T, p *picker, x, y int) *PickResult {
	t.Helper()
	result, err := p.handle(Key{Mouse: &MouseEvent{X: x, Y: y}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func clickHint(t *testing.T, p *picker, key Key) *PickResult {
	t.Helper()
	for _, h := range p.mouseHints {
		if h.key == key {
			return clickMouse(t, p, h.start, p.mouseFooterRow)
		}
	}
	t.Fatalf("no visible hint for %+v", key)
	return nil
}

func TestMouseSelectionAndOpen(t *testing.T) {
	p := mousePicker()
	now := time.Now()
	p.clock = func() time.Time { return now }
	p.frame(120, 12)
	if got := clickMouse(t, p, 5, 5); got != nil || p.active != 2 {
		t.Fatalf("single click: %+v, active %d", got, p.active)
	}
	p.frame(120, 12)
	// Release reports must not count as another click.
	if got, _ := p.handle(Key{Mouse: &MouseEvent{X: 5, Y: 5, Release: true}}); got != nil {
		t.Fatal("release opened session")
	}
	now = now.Add(100 * time.Millisecond)
	if got := clickMouse(t, p, 5, 5); got == nil || got.Session.ID != "c" || got.Mode != ModeResume {
		t.Fatalf("double click: %+v", got)
	}

	p.active = 12
	p.frame(120, 12)
	offset := p.offset
	clickMouse(t, p, 5, 3)
	if p.active != offset {
		t.Fatalf("scrolled click selected %d, want %d", p.active, offset)
	}
	p.frame(120, 12)
	p.handle(Key{Mouse: &MouseEvent{X: 5, Y: 3, Button: 65}})
	if p.active != offset+3 {
		t.Fatal("wheel did not move selection")
	}
	active := p.active
	clickMouse(t, p, 119, 3)
	if p.active != active {
		t.Fatal("detail card changed selection")
	}
}

func TestMouseFooterUsesVisibleActions(t *testing.T) {
	p := mousePicker()
	p.frame(240, 20)
	clickHint(t, p, Key{Name: "t", Ctrl: true})
	if p.pinned < 0 {
		t.Fatal("target click did not pin")
	}
	p.frame(240, 20)
	clickHint(t, p, Key{Name: "y", Ctrl: true})
	if p.yolo == nil || !*p.yolo {
		t.Fatal("yolo click did not toggle")
	}
	p.frame(240, 20)
	if got := clickHint(t, p, Key{Name: "f", Ctrl: true}); got == nil || got.Mode != ModeFork {
		t.Fatalf("fork click: %+v", got)
	}
	p.frame(40, 20)
	for _, h := range p.mouseHints {
		if h.key.Ctrl && h.key.Name == "y" {
			t.Fatal("hidden yolo hint is clickable")
		}
	}
	// A click in the separator after Enter does nothing.
	if got := clickMouse(t, p, p.mouseHints[0].end+1, p.mouseFooterRow); got != nil {
		t.Fatal("separator launched session")
	}
}

func TestMousePushAndBrowse(t *testing.T) {
	linuxOnly(t)
	p, _, _, session := remoteFixture(t)
	var listing bytes.Buffer
	if err := WriteListing(&listing, []Session{session}, func(Session) int { return 0 }); err != nil {
		t.Fatal(err)
	}
	previous := cannedListing
	cannedListing = listing.String()
	t.Cleanup(func() { cannedListing = previous })
	p.frame(240, 25)
	clickHint(t, p, ctrlO)
	if p.panel == nil || p.panel.state != panelReady {
		t.Fatal("push click did not check destination")
	}
	frame := p.frame(70, 25)
	if !strings.Contains(frame, "Push to desk") {
		t.Fatal("narrow terminal hides push panel")
	}
	// Clicking a conversation behind the panel must not resume it.
	if got := clickMouse(t, p, 5, 3); got != nil {
		t.Fatal("panel click resumed a session")
	}
	clickHint(t, p, enter)
	if p.panel.state != panelDone {
		t.Fatalf("push click left state %v", p.panel.state)
	}
	p.frame(240, 25)
	clickHint(t, p, enter)
	p.frame(240, 25)
	clickHint(t, p, ctrlR)
	if p.view == nil || p.view.host() != "desk" || len(p.filtered) == 0 {
		t.Fatal("browse click did not load desk")
	}
	p.frame(240, 25)
	if got := clickHint(t, p, enter); got == nil || got.PulledFrom != "desk" {
		t.Fatalf("pull click: %+v", got)
	}
}
