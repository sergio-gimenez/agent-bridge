package ocs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type newConversation struct {
	directory string
	target    int
	err       string
	yolo      *bool
}

func (p *picker) openNew(directory string) {
	if directory == "" {
		directory, _ = os.Getwd()
	}
	target := p.indexOfTarget(p.currentTarget())
	if target < 0 {
		target = 0
	}
	p.newConversation = &newConversation{directory: directory, target: target, yolo: p.yolo}
}

func (p *picker) newYoloFor(target *Account) bool {
	if p.newConversation.yolo != nil {
		return *p.newConversation.yolo
	}
	return p.yoloFor(target)
}

func (p *picker) newTarget() *Account {
	if len(p.targets) == 0 {
		return nil
	}
	return &p.targets[p.newConversation.target]
}

func (p *picker) handleNew(key Key) (*PickResult, error) {
	dialog := p.newConversation
	switch {
	case key.Ctrl && key.Name == "c":
		return nil, ErrCancelled
	case key.Name == "escape":
		p.newConversation = nil
	case key.Name == "enter":
		target := p.newTarget()
		if target == nil {
			dialog.err = "Configure an agent account first"
			return nil, nil
		}
		if strings.TrimSpace(dialog.directory) == "" {
			dialog.err = "Enter a project directory"
			return nil, nil
		}
		directory, err := filepath.Abs(expandHome(dialog.directory))
		if err == nil {
			info, statErr := os.Stat(directory)
			if statErr != nil || !info.IsDir() {
				err = fmt.Errorf("Directory does not exist: %s", directory)
			}
		}
		if err != nil {
			dialog.err = err.Error()
			return nil, nil
		}
		result := p.finish(Session{Directory: directory, Source: target.Tool}, target, ModeNew)
		result.SkipPermissions = dialog.yolo
		return &result, nil
	case key.Name == "tab", key.Name == "right", key.Ctrl && key.Name == "t":
		dialog.target = NextTarget(len(p.targets), dialog.target, 1)
	case key.Name == "backtab", key.Name == "left":
		dialog.target = NextTarget(len(p.targets), dialog.target, -1)
	case key.Ctrl && key.Name == "y":
		value := !p.newYoloFor(p.newTarget())
		dialog.yolo = &value
	case key.Ctrl && key.Name == "u":
		dialog.directory, dialog.err = "", ""
	case key.Name == "backspace":
		runes := []rune(dialog.directory)
		if len(runes) > 0 {
			dialog.directory = string(runes[:len(runes)-1])
		}
		dialog.err = ""
	case key.Text != "" && !key.Ctrl:
		dialog.directory += key.Text
		dialog.err = ""
	}
	return nil, nil
}

func (p *picker) drawNew(lines []string, width, bodyRows int) {
	dialog := p.newConversation
	target := p.newTarget()
	label := "No configured accounts"
	if target != nil {
		label = destinationName(target)
	}
	permissions := "Ask for permissions"
	if p.newYoloFor(target) {
		permissions = "YOLO: bypass permission checks"
	}
	body := []string{
		bold(truncatePlain(label, width-4)),
		dim(permissions),
		"Directory: " + truncatePlain(dialog.directory+"▏", max(1, width-15)),
	}
	if dialog.err != "" {
		body = append(body, red(truncatePlain(dialog.err, width-4)))
	}
	body = append(body, dim(truncatePlain("Type to edit · Ctrl+U clears the directory", width-4)), dim(truncatePlain("Tab / Ctrl+T changes agent/account · Ctrl+Y toggles YOLO", width-4)))
	box := boxed("New conversation", body, width, bodyRows)
	for i := 0; i < bodyRows; i++ {
		lines[i+2] = box[i]
	}
}
