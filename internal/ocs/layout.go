package ocs

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Layout is the part of the picker's look you can change from the keyboard,
// remembered between launches.
type Layout struct {
	// Share of the width given to the session list, in percent. Zero means
	// the default split.
	ListPercent int `json:"listPercent,omitempty"`
}

const (
	defaultListPercent = 48
	resizeStep         = 4
	minListCols        = 24
	minCardCols        = 32
)

// LayoutPath sits beside the session cache: it is a preference, but one that
// is safe to lose.
func LayoutPath() string {
	return filepath.Join(filepath.Dir(CachePath()), "layout.json")
}

// LoadLayout returns the saved layout, or the default when there is none.
func LoadLayout(path string) Layout {
	var layout Layout
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &layout)
	}
	if layout.ListPercent != 0 {
		layout.ListPercent = clamp(layout.ListPercent, 10, 90)
	}
	return layout
}

func SaveLayout(path string, layout Layout) error {
	raw, err := json.Marshal(layout)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// splitWidths divides width cells between the list, a one-cell gap and the
// card. The default split is capped so the list does not sprawl on very wide
// terminals; once you have resized it, your percentage is used as is, within
// the minimums both sides need to stay readable.
func splitWidths(width, listPercent int) (list, card int) {
	if listPercent == 0 {
		list = clamp(width*defaultListPercent/100, 44, 72)
	} else {
		list = width * listPercent / 100
	}
	list = clamp(list, minListCols, width-minCardCols-1)
	return list, width - list - 1
}
