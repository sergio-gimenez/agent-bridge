package ocs

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

type menuAction struct {
	label string
	run   func() (*PickResult, error)
}

type contextMenu struct {
	items               []menuAction
	active, offset      int
	x, y, width, height int
}

func (p *picker) openMenu(x, y int) {
	selected := p.selected()
	if selected == nil {
		return
	}
	session := *selected
	menu := &contextMenu{x: x, y: y}
	addKey := func(label string, key Key) {
		menu.items = append(menu.items, menuAction{label, func() (*PickResult, error) { return p.handle(key) }})
	}
	if p.view != nil {
		if session.OpenPID == 0 {
			addKey("Pull & resume here", Key{Name: "enter"})
		}
		addKey("Browse next machine", Key{Name: "r", Ctrl: true})
		addKey("Back to local conversations", Key{Name: "escape"})
	} else {
		label := "Resume"
		if target := p.currentTarget(); target != nil {
			label += " in " + AccountLabel(target.Tool, target)
		}
		addKey(label, Key{Name: "enter"})
		addKey("Fork conversation in displayed target", Key{Name: "f", Ctrl: true})
		addKey("Open in next tool", Key{Name: "tab"})
		for _, target := range p.targets {
			target := target
			menu.items = append(menu.items, menuAction{"Open in " + destinationName(&target), func() (*PickResult, error) {
				result := p.finish(session, &target, ModeResume)
				return &result, nil
			}})
		}
		if session.Source == SourceClaude || session.Source == SourceCodex {
			addKey("Push to another machine…", Key{Name: "o", Ctrl: true})
		}
		if len(p.options.Hosts) > 0 {
			addKey("Pull from "+p.options.Hosts[0]+"…", Key{Name: "r", Ctrl: true})
		}
		if suggested := p.recommendedTarget(); suggested != nil {
			addKey("Use suggested account "+strings.ToUpper(suggested.Name), Key{Name: "g", Ctrl: true})
		}
		yolo := "Enable YOLO for this launch"
		if p.yoloFor(p.currentTarget()) {
			yolo = "Disable YOLO for this launch"
		}
		addKey(yolo, Key{Name: "y", Ctrl: true})
	}
	menu.items = append(menu.items, menuAction{"New conversation in this project…", func() (*PickResult, error) {
		p.openNew(session.Directory)
		return nil, nil
	}})
	menu.items = append(menu.items, menuAction{"Close menu", func() (*PickResult, error) { return nil, nil }})
	p.menu = menu
}

func (p *picker) chooseMenu() (*PickResult, error) {
	action := p.menu.items[p.menu.active]
	p.menu = nil
	return action.run()
}

func (p *picker) handleMenu(key Key) (*PickResult, error) {
	menu := p.menu
	switch {
	case key.Ctrl && key.Name == "c":
		return nil, ErrCancelled
	case key.Name == "escape":
		p.menu = nil
	case key.Name == "enter":
		return p.chooseMenu()
	case key.Name == "up":
		menu.active = clampIndex(menu.active-1, len(menu.items))
	case key.Name == "down", key.Name == "tab":
		menu.active = clampIndex(menu.active+1, len(menu.items))
	case key.Name == "pageup":
		menu.active = clampIndex(menu.active-10, len(menu.items))
	case key.Name == "pagedown":
		menu.active = clampIndex(menu.active+10, len(menu.items))
	case key.Name == "home":
		menu.active = 0
	case key.Name == "end":
		menu.active = len(menu.items) - 1
	}
	return nil, nil
}

func (p *picker) handleMenuMouse(mouse MouseEvent, button int) (*PickResult, error) {
	menu := p.menu
	if button == 64 || button == 65 {
		step := 1
		if button == 64 {
			step = -1
		}
		menu.active = clampIndex(menu.active+step, len(menu.items))
		return nil, nil
	}
	if button != 0 {
		return nil, nil
	}
	if mouse.Y == p.mouseFooterRow {
		for _, h := range p.mouseHints {
			if mouse.X >= h.start && mouse.X <= h.end {
				return p.handle(h.key)
			}
		}
	}
	if mouse.X < menu.x || mouse.X >= menu.x+menu.width || mouse.Y < menu.y || mouse.Y >= menu.y+menu.height {
		p.menu = nil // dismiss without launching the underlying conversation
		return nil, nil
	}
	index := menu.offset + mouse.Y - menu.y - 1
	if mouse.Y > menu.y && mouse.Y < menu.y+menu.height-1 && index < len(menu.items) {
		menu.active = index
		return p.chooseMenu()
	}
	return nil, nil
}

func (p *picker) drawMenu(lines []string, width, bodyRows int) {
	menu := p.menu
	menu.width = min(width, 56)
	menu.height = max(3, min(bodyRows+2, len(menu.items)+2))
	menu.x = clamp(menu.x, 1, width-menu.width+1)
	menu.y = clamp(menu.y, 1, len(lines)-menu.height)
	visible := menu.height - 2
	if menu.active < menu.offset {
		menu.offset = menu.active
	}
	if menu.active >= menu.offset+visible {
		menu.offset = menu.active - visible + 1
	}
	var body []string
	for i := menu.offset; i < min(len(menu.items), menu.offset+visible); i++ {
		label := truncatePlain(menu.items[i].label, menu.width-6)
		if i == menu.active {
			body = append(body, accent("▸ "+label))
		} else {
			body = append(body, "  "+label)
		}
	}
	box := boxed(fmt.Sprintf("Actions %d/%d", menu.active+1, len(menu.items)), body, menu.width, menu.height)
	for i, row := range box {
		index := menu.y - 1 + i
		plain := ansiPattern.ReplaceAllString(lines[index], "")
		before := padLine(runewidth.Truncate(plain, menu.x-1, ""), menu.x-1)
		cut := runewidth.Truncate(plain, menu.x-1+menu.width, "")
		after := runewidth.Truncate(plain[len(cut):], max(0, width-(menu.x-1+menu.width)), "")
		lines[index] = before + row + after
	}
}
