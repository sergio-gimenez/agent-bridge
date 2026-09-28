package ocs

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

// PickMode: "resume" continues the picked session in place; "fork" branches off
// it into a new session, leaving the original conversation as it was.
type PickMode string

const (
	ModeResume PickMode = "resume"
	ModeFork   PickMode = "fork"
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
}

var ErrCancelled = errors.New("Cancelled.")

func dim(value string) string     { return "\x1b[2m" + value + "\x1b[0m" }
func bold(value string) string    { return "\x1b[1m" + value + "\x1b[0m" }
func cyan(value string) string    { return "\x1b[36m" + value + "\x1b[0m" }
func yellow(value string) string  { return "\x1b[33m" + value + "\x1b[0m" }
func magenta(value string) string { return "\x1b[35m" + value + "\x1b[0m" }
func blue(value string) string    { return "\x1b[34m" + value + "\x1b[0m" }
func green(value string) string   { return "\x1b[32m" + value + "\x1b[0m" }
func red(value string) string     { return "\x1b[1;31m" + value + "\x1b[0m" }

var sourceColor = map[Source]func(string) string{
	SourceOpencode: magenta,
	SourceClaude:   blue,
	SourceCodex:    green,
}

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

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func visibleWidth(value string) int {
	return utf8.RuneCountInString(ansiPattern.ReplaceAllString(value, ""))
}

func truncatePlain(value string, width int) string {
	if width <= 1 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width-1]) + "…"
}

func padLine(value string, width int) string {
	if gap := width - visibleWidth(value); gap > 0 {
		return value + strings.Repeat(" ", gap)
	}
	return value
}

func splitTerms(query string) []string {
	return strings.Fields(strings.ToLower(query))
}

// highlightTerms marks every case-insensitive occurrence of a query term. It
// works on plain text, so a match can never land inside an escape code.
func highlightTerms(value string, terms []string) string {
	if len(terms) == 0 {
		return value
	}
	runes := []rune(value)
	lowered := make([]rune, len(runes))
	for i, r := range runes {
		lowered[i] = unicode.ToLower(r)
	}

	marked := make([]bool, len(runes))
	for _, term := range terms {
		needle := []rune(term)
		for start := 0; start+len(needle) <= len(lowered); start++ {
			if string(lowered[start:start+len(needle)]) == term {
				for k := range needle {
					marked[start+k] = true
				}
			}
		}
	}

	var builder strings.Builder
	for i := 0; i < len(runes); {
		j := i
		for j < len(runes) && marked[j] == marked[i] {
			j++
		}
		if marked[i] {
			builder.WriteString(yellow(string(runes[i:j])))
		} else {
			builder.WriteString(string(runes[i:j]))
		}
		i = j
	}
	return builder.String()
}

func renderPreview(session *Session, terms []string, width int) []string {
	// Every returned line's *visible* length must be <= width, or the
	// terminal wraps it back to column 0 and smears the layout.
	put := func(plain string) string { return highlightTerms(truncatePlain(plain, width), terms) }

	lines := []string{
		bold(put(session.Title)),
		dim(put(ShortenHome(session.Directory))),
		dim(truncatePlain(fmt.Sprintf("%s  %s  %s", session.UpdatedAtLabel, sessionLabel(session), session.ID), width)),
		"",
	}
	if len(session.Prompts) > 0 {
		lines = append(lines, cyan("Recent user prompts"))
		for _, prompt := range session.Prompts {
			lines = append(lines, put("- "+prompt))
		}
		lines = append(lines, "")
	}
	if len(session.AssistantSnippet) > 0 {
		lines = append(lines, cyan("Recent assistant snippets"))
		for _, snippet := range session.AssistantSnippet {
			lines = append(lines, put("- "+snippet))
		}
	}
	return lines
}

func renderList(items []Session, active, pageStart, pageSize int, terms []string, width int, target *Account) []string {
	if len(items) == 0 {
		return []string{dim("No matches")}
	}
	end := min(pageStart+pageSize, len(items))
	indentWidth := max(4, width-6)

	var lines []string
	for index := pageStart; index < end; index++ {
		session := &items[index]
		marker := " "
		if index == active {
			marker = cyan(">")
		}
		badgeText := SessionBadgeText(session, target)
		titleWidth := max(4, width-utf8.RuneCountInString(badgeText)-4)

		lines = append(lines,
			fmt.Sprintf("%s %s %s", marker, sourceColor[session.Source](badgeText),
				highlightTerms(truncatePlain(session.Title, titleWidth), terms)),
			dim("     "+highlightTerms(truncatePlain(ShortenHome(session.Directory), indentWidth), terms)),
			dim("     "+truncatePlain(session.UpdatedAtLabel, indentWidth)),
			"",
		)
	}
	return lines
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

func (p *picker) frame(cols, rows int) string {
	// Layout: [left padded to leftWidth][2-space gap][right]. Keep the whole
	// row <= cols-1 so no terminal wraps a line back to column 0.
	leftWidth := clamp((cols*42+50)/100, 30, 64)
	rightWidth := max(20, cols-leftWidth-3)

	const headerRows, linesPerItem = 6, 4
	availableRows := max(linesPerItem, rows-headerRows-1)
	pageSize := max(1, availableRows/linesPerItem)
	pageIndex := p.active / pageSize
	pageStart := pageIndex * pageSize
	pageCount := max(1, (len(p.filtered)+pageSize-1)/pageSize)

	target := p.currentTarget()
	rowTarget := BadgeTarget(target, p.pinned >= 0)
	selected := p.selected()
	terms := splitTerms(p.query)

	var lines []string
	lines = append(lines, fmt.Sprintf("%s  %s %s  %s %s  %s %s",
		bold("Sessions"), magenta("[OC]"), dim("opencode"), blue("[CC*]"), dim("claude"), green("[CX*]"), dim("codex")))

	enterName := "its own tool"
	if target != nil {
		enterName = destinationName(target)
	}
	openHint := fmt.Sprintf("Enter: %s. Ctrl+F: fork it.", enterName)
	if selected != nil {
		if tabTarget := NextToolTarget(p.targets, selected.Source, target); tabTarget != nil {
			openHint += fmt.Sprintf(" Tab: %s.", destinationName(tabTarget))
		}
	}
	lines = append(lines, dim(truncatePlain("Type to filter. ↑↓ move. PgUp/PgDn jump. "+openHint+" Esc cancels.", cols-1)))

	targetHint := "No target configured."
	if target != nil {
		follows := ""
		if p.pinned < 0 {
			follows = " (follows the selection)"
		}
		targetHint = fmt.Sprintf("Target: %s%s. Ctrl+T / Shift+Tab cycle. Enter follows displayed route.",
			AccountLabel(target.Tool, target), follows)
	}
	lines = append(lines, dim(truncatePlain(targetHint, cols-1)))
	lines = append(lines, "Query: "+p.query)

	// The permission mode sits on the short status line so the header never
	// wraps: it must stay visible, since it decides what the agent may do.
	yoloHint := dim("Permissions: ask. Ctrl+Y: yolo")
	if p.yoloFor(target) {
		yoloHint = red("YOLO") + " " + dim("permission checks bypassed, Ctrl+Y to ask again")
	}
	lines = append(lines,
		dim(fmt.Sprintf("%d matches  Page %d/%d", len(p.filtered), pageIndex+1, pageCount))+"  "+yoloHint,
		"")

	left := renderList(p.filtered, p.active, pageStart, pageSize, terms, leftWidth, rowTarget)
	right := []string{dim("No session selected")}
	if selected != nil {
		right = renderPreview(selected, terms, rightWidth)
	}
	if len(right) > availableRows {
		right = right[:availableRows]
	}
	for i := 0; i < max(len(left), len(right)); i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines = append(lines, padLine(l, leftWidth)+"  "+r)
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}

	// Home, then overwrite each line and clear its tail, then clear below: no
	// full-screen wipe, so nothing flickers between keystrokes.
	return "\x1b[H" + strings.Join(lines, "\x1b[K\r\n") + "\x1b[K\x1b[J"
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
	case key.Ctrl && key.Name == "f":
		// Fork follows the same route as Enter, but branches into a new
		// session instead of writing more turns into the picked one.
		if selected != nil {
			result := p.finish(*selected, p.currentTarget(), ModeFork)
			return &result, nil
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
	p := &picker{sessions: sessions, targets: options.Targets, options: options, query: initialQuery}
	p.pinned = p.indexOfTarget(options.InitialTarget)
	p.refilter()

	in := int(os.Stdin.Fd())
	state, err := term.MakeRaw(in)
	if err != nil {
		return PickResult{}, fmt.Errorf("the picker needs a terminal (try --print): %w", err)
	}

	out := os.Stdout
	// Alternate screen: the picker draws over a blank page and leaves the
	// shell's scrollback exactly as it was.
	fmt.Fprint(out, "\x1b[?1049h")
	restore := func() {
		fmt.Fprint(out, "\x1b[?1049l")
		_ = term.Restore(in, state)
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

	draw()
	if options.AfterFirstDraw != nil {
		options.AfterFirstDraw()
	}
	for {
		select {
		case <-resized:
			draw()
		case chunk, ok := <-input:
			if !ok {
				restore()
				return PickResult{}, ErrCancelled
			}
			for _, key := range DecodeKeys(chunk) {
				result, err := p.handle(key)
				if err != nil || result != nil {
					restore()
					if err != nil {
						return PickResult{}, err
					}
					return *result, nil
				}
			}
			draw()
		}
	}
}
