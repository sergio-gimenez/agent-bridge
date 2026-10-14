package ocs

import (
	"strings"
	"testing"
)

func TestHelpPreservesPickerAndDialog(t *testing.T) {
	session := claudeTestSession()
	p := &picker{sessions: []Session{session}, targets: targets, query: "Old", pinned: 1}
	p.refilter()
	p.openNew("/tmp/project")
	dialog := p.newConversation
	p.handle(Key{Name: "f1"})
	p.frame(80, 24)
	for _, key := range []Key{{Name: "enter"}, {Name: "y", Ctrl: true}, {Text: "typing"}, {Name: "t", Ctrl: true}} {
		result, err := p.handle(key)
		if result != nil || err != nil {
			t.Fatalf("help launched an action: %v, %v", result, err)
		}
	}
	p.handle(Key{Name: "end"})
	if p.helpOffset != p.helpMax || p.helpOffset == 0 {
		t.Fatal("End did not scroll to the bottom")
	}
	p.frame(40, 10)
	p.handle(Key{Name: "escape"})
	if p.helpOpen || p.query != "Old" || p.pinned != 1 || p.yolo != nil || p.newConversation != dialog || dialog.directory != "/tmp/project" {
		t.Fatalf("help changed the underlying view: %+v", p)
	}
}

func TestHelpScrollingAndMouse(t *testing.T) {
	p := &picker{helpOpen: true}
	for _, size := range [][2]int{{120, 30}, {40, 12}} {
		frame := p.frame(size[0], size[1])
		for _, line := range strings.Split(frame, "\r\n") {
			if visibleWidth(line) >= size[0] {
				t.Fatalf("help wraps beyond %d columns: %q", size[0], line)
			}
		}
		p.handle(Key{Name: "home"})
		p.handle(Key{Name: "pagedown"})
		if p.helpOffset != p.helpPage {
			t.Fatal("PageDown did not scroll a page")
		}
		p.handle(Key{Mouse: &MouseEvent{Button: 65, X: 3, Y: 5}})
		if p.helpOffset != p.helpPage+3 {
			t.Fatal("wheel did not scroll help")
		}
	}
	p.frame(80, 24)
	p.handle(Key{Mouse: &MouseEvent{Button: 0, X: 3, Y: p.mouseFooterRow}})
	if p.helpOpen {
		t.Fatal("clicking back did not close help")
	}
}

func TestHelpShortcutDecoding(t *testing.T) {
	for _, sequence := range []string{"\x1bOP", "\x1b[11~", "\x1b[P"} {
		keys := DecodeKeys([]byte(sequence))
		if len(keys) != 1 || keys[0].Name != "f1" {
			t.Fatalf("F1 %q decoded as %+v", sequence, keys)
		}
	}
	p := &picker{pinned: -1}
	p.handle(Key{Text: "?"})
	if !p.helpOpen || p.query != "" {
		t.Fatal("? did not open help on an empty search")
	}
	p.handle(Key{Text: "?"})
	p.handle(Key{Text: "why"})
	p.handle(Key{Text: "?"})
	if p.helpOpen || p.query != "why?" {
		t.Fatal("? interrupted a nonempty search")
	}
}
