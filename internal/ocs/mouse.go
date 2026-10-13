package ocs

import (
	"strings"
	"time"
)

type mouseHint struct {
	start, end int // inclusive, one-based cells
	key        Key
}

// Use the fitted footer itself for hit testing, so hidden controls and the
// separators between them never react to a click.
func (p *picker) clickableHints(hints []hint, width int) string {
	line := fitHints(hints, width)
	p.mouseHints = nil
	x := 2 // the footer starts with one space
	for _, h := range hints {
		cells := visibleWidth(h.key + " " + h.label)
		if x+cells-1 > visibleWidth(line) {
			break
		}
		key := Key{Name: h.key}
		switch h.key {
		case "esc":
			key.Name = "escape"
		case "←→":
			key.Name = "right"
		case "^←→":
			mid := x + cells/2
			p.mouseHints = append(p.mouseHints, mouseHint{x, mid - 1, Key{Name: "left", Ctrl: true}}, mouseHint{mid, x + cells - 1, Key{Name: "right", Ctrl: true}})
			x += cells + 3
			continue
		case "●":
			x += cells + 3
			continue
		default:
			if strings.HasPrefix(h.key, "^") {
				key.Name, key.Ctrl = h.key[1:], true
			}
		}
		p.mouseHints = append(p.mouseHints, mouseHint{x, x + cells - 1, key})
		x += cells + 3 // space, middle dot, space
	}
	return line
}

func (p *picker) handleMouse(mouse MouseEvent) (*PickResult, error) {
	if mouse.Release || mouse.X < 1 || mouse.Y < 1 {
		return nil, nil
	}
	button := mouse.Button &^ (4 | 8 | 16) // Shift, Alt, Ctrl
	if p.menu != nil {
		return p.handleMenuMouse(mouse, button)
	}
	if p.newConversation != nil {
		if button == 0 && mouse.Y == p.mouseFooterRow {
			for _, h := range p.mouseHints {
				if mouse.X >= h.start && mouse.X <= h.end {
					return p.handle(h.key)
				}
			}
		}
		return nil, nil
	}
	if button == 64 || button == 65 {
		p.lastClickID = ""
		if p.panel == nil && mouse.X <= p.mouseListWidth && mouse.Y >= 3 && mouse.Y < 3+p.mouseBodyRows {
			step := 3
			if button == 64 {
				step = -step
			}
			p.active = clampIndex(p.active+step, len(p.filtered))
		}
		return nil, nil
	}
	if button == 2 && p.panel == nil && mouse.X <= p.mouseListWidth && mouse.Y >= 3 && mouse.Y < 3+p.mouseBodyRows {
		index := p.offset + mouse.Y - 3
		if index < len(p.filtered) {
			p.active = index
			p.lastClickID = ""
			p.openMenu(mouse.X, mouse.Y)
		}
		return nil, nil
	}
	if button != 0 {
		return nil, nil
	} // ignore drag and other buttons
	if mouse.Y == p.mouseFooterRow {
		p.lastClickID = ""
		for _, h := range p.mouseHints {
			if mouse.X >= h.start && mouse.X <= h.end {
				return p.handle(h.key)
			}
		}
		return nil, nil
	}
	if p.panel != nil || mouse.X > p.mouseListWidth || mouse.Y < 3 || mouse.Y >= 3+p.mouseBodyRows {
		p.lastClickID = ""
		return nil, nil
	}
	index := p.offset + mouse.Y - 3
	if index >= len(p.filtered) {
		p.lastClickID = ""
		return nil, nil
	}
	p.active = index
	session := p.filtered[index]
	id := string(session.Source) + ":" + sessionLabel(&session) + ":" + session.ID
	now := p.now()
	if id == p.lastClickID && now.Sub(p.lastClickAt) >= 0 && now.Sub(p.lastClickAt) <= 400*time.Millisecond {
		return p.handle(Key{Name: "enter"})
	}
	p.lastClickID, p.lastClickAt = id, now
	return nil, nil
}
