package ocs

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// PickMode: "resume" continues the picked session in place; "fork" branches off
// it into a new session, leaving the original conversation as it was.
type PickMode string

const (
	ModeResume PickMode = "resume"
	ModeFork   PickMode = "fork"
	// ModeMove hands the session to another machine (agb move); the caller
	// asks where.
	ModeMove PickMode = "move"
)

type PickResult struct {
	Session Session
	Target  Account
	Mode    PickMode
	// Set only when Ctrl+Y was used: the caller's own default applies
	// otherwise.
	SkipPermissions *bool
}

type PickOptions struct {
	// Every place a session can be opened, in cycling order.
	Targets       []Account
	InitialTarget *Account
	// Whether a launch into this target bypasses permission checks when
	// Ctrl+Y has not been pressed.
	SkipPermissions func(Account) bool
	// Runs once the first frame is on screen, for work that should not
	// delay it.
	AfterFirstDraw func()
	// The split to start with, and where to keep it when Ctrl+←/→ changed
	// it.
	Layout     Layout
	SaveLayout func(Layout)
}

var ErrCancelled = errors.New("Cancelled.")

func sessionLabel(session *Session) string {
	return AccountLabel(session.Source, session.Account)
}

// SessionBadgeText is the route: one label when the target is where the
// session already lives, "from→to" when following it means landing somewhere
// else.
func SessionBadgeText(session *Session, target *Account) string {
	from := sessionLabel(session)
	if target == nil || IsNativeTarget(session, target) {
		return "[" + from + "]"
	}
	return "[" + from + "→" + AccountLabel(target.Tool, target) + "]"
}

func EnterDestination(session Session, target *Account, mode PickMode) PickResult {
	result := PickResult{Session: session, Mode: mode}
	switch {
	case target != nil:
		result.Target = *target
	case session.Account != nil:
		result.Target = *session.Account
	default:
		result.Target = Account{Tool: session.Source, Name: strings.ToLower(sessionLabel(&session))}
	}
	return result
}

// BadgeTarget: while the target still follows the selection, no row is going
// anywhere but its own account, so every badge stays native. Only a pinned
// target turns the other rows into routes.
func BadgeTarget(target *Account, pinned bool) *Account {
	if pinned {
		return target
	}
	return nil
}

// "Claude Code CC2" rather than a bare label, so the hint line reads as a
// sentence.
func destinationName(target *Account) string {
	if target.Tool == SourceOpencode {
		return ToolName(target.Tool)
	}
	return ToolName(target.Tool) + " " + AccountLabel(target.Tool, target)
}

func clamp(value, low, high int) int {
	return max(low, min(high, value))
}

func clampIndex(index, length int) int {
	if length == 0 || index < 0 {
		return 0
	}
	if index >= length {
		return length - 1
	}
	return index
}

type picker struct {
	sessions []Session
	targets  []Account
	options  PickOptions

	query    string
	active   int
	filtered []Session
	// Until the target is cycled it follows the selection, so Enter resumes
	// what you picked where it already lives. Cycling pins a destination, and
	// from then on the badge shows the route to it.
	pinned int
	// Ctrl+Y flips yolo for this one launch. Until then each target keeps its
	// configured default, so the hint tracks whatever the route lands on.
	yolo *bool
	// First list row on screen; the window scrolls to follow the selection.
	offset int
	// Stands in for time.Now in tests, so relative times are stable.
	clock func() time.Time

	layout        Layout
	layoutChanged bool
	// Width the last frame split between list and card; zero while the card
	// is hidden, which makes resizing a no-op.
	splitWidth int
}

// resize moves the divider between list and card by delta percent, staying
// within what the current width can actually show, so pressing the other way
// always moves it straight back.
func (p *picker) resize(delta int) {
	width := p.splitWidth
	if width == 0 {
		return
	}
	current := p.layout.ListPercent
	if current == 0 {
		list, _ := splitWidths(width, 0)
		current = (list*100 + width/2) / width
	}
	lowest := (minListCols*100 + width - 1) / width
	highest := (width - minCardCols - 1) * 100 / width
	next := clamp(current+delta, lowest, max(lowest, highest))
	if next != p.layout.ListPercent {
		p.layout.ListPercent = next
		p.layoutChanged = true
	}
}

func (p *picker) indexOfTarget(wanted *Account) int {
	if wanted == nil {
		return -1
	}
	for i, target := range p.targets {
		if target.Tool == wanted.Tool && target.Name == wanted.Name {
			return i
		}
	}
	return -1
}

func (p *picker) selected() *Session {
	if p.active < len(p.filtered) {
		return &p.filtered[p.active]
	}
	return nil
}

func (p *picker) currentTarget() *Account {
	if p.pinned >= 0 {
		return &p.targets[p.pinned]
	}
	if selected := p.selected(); selected != nil {
		for i := range p.targets {
			if IsNativeTarget(selected, &p.targets[i]) {
				return &p.targets[i]
			}
		}
	}
	return nil
}

func (p *picker) cycleTarget(step int) {
	from := p.pinned
	if from < 0 {
		from = p.indexOfTarget(p.currentTarget())
	}
	p.pinned = NextTarget(len(p.targets), from, step)
}

func (p *picker) yoloFor(target *Account) bool {
	if p.yolo != nil {
		return *p.yolo
	}
	if target == nil || p.options.SkipPermissions == nil {
		return false
	}
	return p.options.SkipPermissions(*target)
}

func (p *picker) finish(session Session, target *Account, mode PickMode) PickResult {
	result := EnterDestination(session, target, mode)
	result.SkipPermissions = p.yolo
	return result
}

func (p *picker) refilter() {
	p.filtered = SearchSessions(p.sessions, p.query)
	p.active = clampIndex(p.active, len(p.filtered))
}

// handle applies one key. It returns a result, ErrCancelled, or neither when
// the picker should keep going.
func (p *picker) handle(key Key) (*PickResult, error) {
	selected := p.selected()

	switch {
	case key.Ctrl && key.Name == "c", key.Name == "escape":
		return nil, ErrCancelled
	case key.Name == "enter":
		if selected != nil {
			result := p.finish(*selected, p.currentTarget(), ModeResume)
			return &result, nil
		}
	case key.Ctrl && key.Name == "y":
		value := !p.yoloFor(p.currentTarget())
		p.yolo = &value
	// Ctrl+←/→ drag the divider between the list and the card.
	case key.Ctrl && key.Name == "right":
		p.resize(resizeStep)
	case key.Ctrl && key.Name == "left":
		p.resize(-resizeStep)
	case key.Ctrl && key.Name == "f":
		// Fork follows the same route as Enter, but branches into a new
		// session instead of writing more turns into the picked one.
		if selected != nil {
			result := p.finish(*selected, p.currentTarget(), ModeFork)
			return &result, nil
		}
	case key.Ctrl && key.Name == "o":
		// Ctrl+M would be the natural key, but terminals send it as Enter.
		if selected != nil && (selected.Source == SourceClaude || selected.Source == SourceCodex) {
			return &PickResult{Session: *selected, Mode: ModeMove}, nil
		}
	case key.Name == "backtab":
		p.cycleTarget(-1)
	case key.Name == "tab":
		// Tab skips straight to the next tool rather than stepping through
		// the other accounts of the current one, and opens there right away.
		if selected != nil {
			if tabTarget := NextToolTarget(p.targets, selected.Source, p.currentTarget()); tabTarget != nil {
				result := p.finish(*selected, tabTarget, ModeResume)
				return &result, nil
			}
		}
	case key.Ctrl && key.Name == "t":
		p.cycleTarget(1)
	case key.Name == "up":
		p.active = clampIndex(p.active-1, len(p.filtered))
	case key.Name == "down":
		p.active = clampIndex(p.active+1, len(p.filtered))
	case key.Name == "pageup":
		p.active = clampIndex(p.active-10, len(p.filtered))
	case key.Name == "pagedown":
		p.active = clampIndex(p.active+10, len(p.filtered))
	case key.Name == "home":
		p.active = 0
	case key.Name == "end":
		p.active = clampIndex(len(p.filtered)-1, len(p.filtered))
	case key.Name == "backspace":
		if runes := []rune(p.query); len(runes) > 0 {
			p.query = string(runes[:len(runes)-1])
			p.active = 0
			p.refilter()
		}
	case key.Text != "" && !key.Ctrl:
		p.query += key.Text
		p.active = 0
		p.refilter()
	}
	return nil, nil
}

func PickSession(sessions []Session, initialQuery string, options PickOptions) (PickResult, error) {
	p := &picker{sessions: sessions, targets: options.Targets, options: options, query: initialQuery,
		layout: options.Layout}
	p.pinned = p.indexOfTarget(options.InitialTarget)
	p.refilter()

	in := int(os.Stdin.Fd())
	state, err := term.MakeRaw(in)
	if err != nil {
		return PickResult{}, fmt.Errorf("the picker needs a terminal (try --print): %w", err)
	}

	out := os.Stdout
	// Alternate screen: the picker draws over a blank page and leaves the
	// shell's scrollback exactly as it was. The real cursor is hidden; the
	// query line draws its own.
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	restore := func() {
		fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		_ = term.Restore(in, state)
		if p.layoutChanged && options.SaveLayout != nil {
			options.SaveLayout(p.layout)
		}
	}

	draw := func() {
		cols, rows, err := term.GetSize(int(out.Fd()))
		if err != nil {
			cols, rows = 100, 30
		}
		fmt.Fprint(out, p.frame(cols, rows))
	}

	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)

	input := make(chan []byte)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buffer)
			if err != nil {
				close(input)
				return
			}
			chunk := make([]byte, n)
			copy(chunk, buffer[:n])
			input <- chunk
		}
	}()

	// apply runs every key in data. It reports whether the picker is done.
	apply := func(data []byte) (bool, PickResult, error) {
		for _, key := range DecodeKeys(data) {
			result, err := p.handle(key)
			if err != nil {
				return true, PickResult{}, err
			}
			if result != nil {
				return true, *result, nil
			}
		}
		return false, PickResult{}, nil
	}

	// A lone ESC is either the Escape key or the start of a sequence whose
	// rest has not arrived yet (a slow SSH link can split one). Hold it
	// briefly instead of cancelling on half an arrow key.
	var pending []byte
	var escapeTimeout <-chan time.Time

	draw()
	if options.AfterFirstDraw != nil {
		options.AfterFirstDraw()
	}
	for {
		var data []byte
		select {
		case <-resized:
			draw()
			continue
		case <-escapeTimeout:
			data, pending, escapeTimeout = pending, nil, nil
		case chunk, ok := <-input:
			if !ok {
				restore()
				return PickResult{}, ErrCancelled
			}
			data = append(pending, chunk...)
			pending, escapeTimeout = nil, nil
			if cut := incompleteEscape(data); cut >= 0 {
				data, pending = data[:cut], append([]byte(nil), data[cut:]...)
				escapeTimeout = time.After(escapeWait)
			}
		}

		done, result, err := apply(data)
		if done {
			restore()
			return result, err
		}
		draw()
	}
}
