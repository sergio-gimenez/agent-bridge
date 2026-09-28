package ocs

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// Plain SGR styles. Only the 8 basic colours plus bold and dim, so the picker
// follows whatever palette the terminal has, light or dark.
func style(code, value string) string { return "\x1b[" + code + "m" + value + "\x1b[0m" }

func dim(value string) string     { return style("2", value) }
func bold(value string) string    { return style("1", value) }
func cyan(value string) string    { return style("36", value) }
func yellow(value string) string  { return style("33", value) }
func magenta(value string) string { return style("35", value) }
func blue(value string) string    { return style("34", value) }
func green(value string) string   { return style("32", value) }
func red(value string) string     { return style("1;31", value) }
func accent(value string) string  { return style("1;36", value) }
func alarm(value string) string   { return style("1;7;31", value) }

var sourceColor = map[Source]func(string) string{
	SourceOpencode: magenta,
	SourceClaude:   blue,
	SourceCodex:    green,
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// visibleWidth is how many terminal cells a string takes once its escape codes
// are stripped. Emoji and CJK take two cells, so counting runes is not enough
// to keep columns aligned.
func visibleWidth(value string) int {
	return runewidth.StringWidth(ansiPattern.ReplaceAllString(value, ""))
}

// truncatePlain cuts unstyled text to at most width cells, ending in "…" when
// something was cut.
func truncatePlain(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return runewidth.Truncate(value, width, "…")
}

// padLine pads a styled string with spaces to exactly width cells. It never
// cuts; callers truncate the plain text before styling it.
func padLine(value string, width int) string {
	if gap := width - visibleWidth(value); gap > 0 {
		return value + strings.Repeat(" ", gap)
	}
	return value
}

// alignRight puts right at the far end of width cells, after left.
func alignRight(left, right string, width int) string {
	gap := width - visibleWidth(left) - visibleWidth(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
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

// relativeTime is the age of a timestamp in the fewest characters that still
// read at a glance: "now", "12m", "3h", "5d", then a date.
func relativeTime(ms float64, now time.Time) string {
	then := time.UnixMilli(int64(ms))
	age := now.Sub(then)
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	case age < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	case then.Year() == now.Year():
		return then.Format("Jan 2")
	}
	return then.Format("2006")
}

// wrapText breaks prose into lines of at most width cells, splitting a word
// only when it is longer than a whole line.
func wrapText(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		for runewidth.StringWidth(word) > width {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			head := runewidth.Truncate(word, width, "")
			if head == "" {
				// A two-cell character in a one-cell width: take it anyway.
				head = string([]rune(word)[:1])
			}
			lines = append(lines, head)
			word = word[len(head):]
		}
		switch {
		case line == "":
			line = word
		case runewidth.StringWidth(line)+1+runewidth.StringWidth(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// Layout of one frame:
//
//	 ❯ query▏                              3 of 16 · target CX1 · YOLO
//	                                                   (blank)
//	▌ CC1  title …          project  12m  ╭─ title ──────────╮
//	  OC   title …          project  41m  │ meta             │
//	  …                                   ╰──────────────────╯
//	                                                   (blank)
//	 enter resume in CC1 · ^f fork · tab → CX1 · ^t target · …
const (
	chromeRows  = 4  // header, blank, blank, footer
	cardMinCols = 90 // below this the card is dropped and the list takes the width
	cardMinRows = 6
	promptLines = 3 // lines a single prompt may wrap onto inside the card
)

func (p *picker) now() time.Time {
	if p.clock != nil {
		return p.clock()
	}
	return time.Now()
}

// scrollTo keeps the selection inside the visible window of height rows,
// moving the window as little as possible.
func (p *picker) scrollTo(height int) {
	if p.active < p.offset {
		p.offset = p.active
	}
	if p.active >= p.offset+height {
		p.offset = p.active - height + 1
	}
	p.offset = clamp(p.offset, 0, max(0, len(p.filtered)-height))
}

func (p *picker) frame(cols, rows int) string {
	// Keep every row <= cols-1 cells, so no terminal ever wraps one back to
	// column 0 and smears the layout.
	width := max(20, cols-1)
	bodyRows := max(1, rows-chromeRows)

	listWidth := width
	cardWidth := 0
	if cols >= cardMinCols && bodyRows >= cardMinRows {
		listWidth = clamp(width*48/100, 44, 72)
		cardWidth = width - listWidth - 1
	}

	p.scrollTo(bodyRows)
	target := p.currentTarget()
	selected := p.selected()
	terms := splitTerms(p.query)

	list := p.renderList(listWidth, bodyRows, terms, BadgeTarget(target, p.pinned >= 0))
	var card []string
	if cardWidth > 0 {
		card = p.renderCard(selected, target, cardWidth, bodyRows, terms)
	}

	lines := []string{p.renderHeader(width, target), ""}
	for i := 0; i < bodyRows; i++ {
		row := padLine(list[i], listWidth)
		if card != nil {
			row += " " + card[i]
		}
		lines = append(lines, row)
	}
	lines = append(lines, "", p.renderFooter(width, target, selected))

	// Home, then overwrite each line and clear its tail, then clear below: no
	// full-screen wipe, so nothing flickers between keystrokes.
	return "\x1b[H" + strings.Join(lines, "\x1b[K\r\n") + "\x1b[K\x1b[J"
}

func (p *picker) renderHeader(width int, target *Account) string {
	left := " " + accent("❯") + " " + bold(truncatePlain(p.query, max(1, width/2))) + accent("▏")

	count := fmt.Sprintf("%d sessions", len(p.sessions))
	if strings.TrimSpace(p.query) != "" {
		count = fmt.Sprintf("%d of %d", len(p.filtered), len(p.sessions))
	}
	targetPart := ""
	switch {
	case p.pinned >= 0 && target != nil:
		targetPart = dim("target ") + accent(AccountLabel(target.Tool, target))
	case target != nil:
		targetPart = dim("target follows selection")
	}
	yoloPart := ""
	if p.yoloFor(target) {
		yoloPart = alarm(" YOLO ")
	}

	// Drop the least important parts until the header fits on one row: the
	// target first, then the count. The yolo badge stays longest, since it
	// decides what the agent may do.
	for _, parts := range [][]string{
		{dim(count), targetPart, yoloPart},
		{dim(count), yoloPart},
		{yoloPart},
	} {
		var kept []string
		for _, part := range parts {
			if part != "" {
				kept = append(kept, part)
			}
		}
		right := strings.Join(kept, dim(" · ")) + " "
		if visibleWidth(left)+visibleWidth(right)+2 <= width {
			return alignRight(left, right, width)
		}
	}
	return left
}

// projectName is the last element of a session's directory, or "~" for a
// session started in the home directory itself.
func projectName(directory string) string {
	if short := ShortenHome(directory); short == "~" {
		return short
	}
	return filepath.Base(directory)
}

// badgeCell is a row's source label, plus an arrow in the target tool's colour
// when Enter would carry it somewhere else. The destination is the same for
// every row and sits in the header, so repeating "→CX2" on each one would only
// crowd the titles.
func badgeCell(session *Session, target *Account, width int) string {
	label := sessionLabel(session)
	cell := sourceColor[session.Source](label)
	if target != nil && !IsNativeTarget(session, target) {
		cell += " " + sourceColor[target.Tool]("→")
	}
	return padLine(cell, width)
}

func badgeCellWidth(session *Session, target *Account) int {
	width := runewidth.StringWidth(sessionLabel(session))
	if target != nil && !IsNativeTarget(session, target) {
		width += 2
	}
	return width
}

func (p *picker) renderList(width, height int, terms []string, badgeTarget *Account) []string {
	lines := make([]string, height)
	if len(p.filtered) == 0 {
		message := "No sessions yet"
		if strings.TrimSpace(p.query) != "" {
			message = "No sessions match “" + strings.TrimSpace(p.query) + "”"
		}
		lines[0] = "  " + dim(truncatePlain(message, width-2))
		return lines
	}

	end := min(p.offset+height, len(p.filtered))
	visible := p.filtered[p.offset:end]

	// Columns are sized from the rows on screen, so badges and times line up
	// without reserving room for values that are scrolled away.
	badgeWidth, timeWidth := 0, 0
	now := p.now()
	for i := range visible {
		badgeWidth = max(badgeWidth, badgeCellWidth(&visible[i], badgeTarget))
		timeWidth = max(timeWidth, len(relativeTime(visible[i].UpdatedAtMs, now)))
	}

	for i := range visible {
		session := &visible[i]
		active := p.offset+i == p.active

		marker := " "
		if active {
			marker = accent("▌")
		}
		badge := badgeCell(session, badgeTarget, badgeWidth)
		age := relativeTime(session.UpdatedAtMs, now)
		age = strings.Repeat(" ", timeWidth-len(age)) + age

		// marker, space, badge, space ... title ... project, two spaces, age, space
		fixed := 2 + badgeWidth + 1 + 2 + timeWidth + 1
		project := projectName(session.Directory)
		projectWidth := min(runewidth.StringWidth(project), 20)
		titleWidth := width - fixed - projectWidth - 2
		if titleWidth < 16 || project == "." || project == "/" {
			project, projectWidth = "", 0
			titleWidth = width - fixed
		}

		title := highlightTerms(truncatePlain(session.Title, titleWidth), terms)
		if active {
			title = bold(title)
		}
		left := marker + " " + badge + " " + title
		right := ""
		if project != "" {
			right = dim(highlightTerms(truncatePlain(project, projectWidth), terms)) + "  "
		}
		right += dim(age) + " "
		if active {
			right = strings.Replace(right, dim(age), age, 1)
		}
		lines[i] = alignRight(left, right, width)
	}
	return lines
}

// renderCard draws the selected session in a rounded box exactly height rows
// tall and width cells wide.
func (p *picker) renderCard(session *Session, target *Account, width, height int, terms []string) []string {
	inner := width - 4
	border := func(value string) string { return dim(value) }

	lines := make([]string, 0, height)
	title := ""
	if session != nil {
		title = truncatePlain(session.Title, inner-2)
	}
	top := border("╭─")
	if title != "" {
		top += " " + bold(highlightTerms(title, terms)) + " "
	}
	top += border(strings.Repeat("─", max(0, width-visibleWidth(top)-1)) + "╮")
	lines = append(lines, top)

	var body []string
	if session != nil {
		meta := strings.Join([]string{
			ShortenHome(session.Directory),
			sessionLabel(session),
			relativeTime(session.UpdatedAtMs, p.now()) + " ago",
			session.ID,
		}, " · ")
		body = append(body, dim(highlightTerms(truncatePlain(meta, inner), terms)))

		if target != nil && !IsNativeTarget(session, target) {
			route := "→ opens in " + destinationName(target) + " as a seeded fork"
			body = append(body, accent(truncatePlain(route, inner)))
		}

		section := func(heading string, color func(string) string, texts []string) {
			if len(texts) == 0 {
				return
			}
			body = append(body, "", color(heading))
			for _, text := range texts {
				// Previews arrive cut to a fixed length with "..."; a single
				// ellipsis character reads better inside the card.
				if trimmed, cut := strings.CutSuffix(text, "..."); cut {
					text = trimmed + "…"
				}
				wrapped := wrapText(text, inner-2)
				if len(wrapped) > promptLines {
					wrapped = wrapped[:promptLines]
					wrapped[promptLines-1] = truncatePlain(strings.TrimSuffix(wrapped[promptLines-1], "…")+"…", inner-2)
				}
				for j, line := range wrapped {
					prefix := "  "
					if j == 0 {
						prefix = dim("› ")
					}
					body = append(body, prefix+highlightTerms(line, terms))
				}
			}
		}
		section("You", accent, session.Prompts)
		section("Assistant", func(value string) string { return style("1;35", value) }, session.AssistantSnippet)
	}

	for i := 0; i < height-2; i++ {
		content := ""
		if i < len(body) {
			content = body[i]
		}
		lines = append(lines, border("│")+" "+padLine(content, inner)+" "+border("│"))
	}
	lines = append(lines, border("╰"+strings.Repeat("─", width-2)+"╯"))
	return lines
}

type hint struct{ key, label string }

func (p *picker) renderFooter(width int, target *Account, selected *Session) string {
	enter := "open"
	if target != nil {
		label := AccountLabel(target.Tool, target)
		if selected != nil && !IsNativeTarget(selected, target) {
			enter = "→ " + label
		} else {
			enter = "resume in " + label
		}
	}

	hints := []hint{{"enter", enter}, {"^f", "fork"}}
	if selected != nil {
		if next := NextToolTarget(p.targets, selected.Source, target); next != nil {
			hints = append(hints, hint{"tab", "→ " + AccountLabel(next.Tool, next)})
		}
	}
	yolo := "yolo"
	if p.yoloFor(target) {
		yolo = "ask again"
	}
	hints = append(hints, hint{"^t", "target"}, hint{"^y", yolo}, hint{"esc", "quit"})

	// Drop hints from the end until the row fits: the first ones say what
	// Enter will do, which matters most.
	for len(hints) > 0 {
		parts := make([]string, len(hints))
		for i, h := range hints {
			parts[i] = accent(h.key) + " " + dim(h.label)
		}
		line := " " + strings.Join(parts, dim(" · "))
		if visibleWidth(line) <= width {
			return line
		}
		hints = hints[:len(hints)-1]
	}
	return ""
}
