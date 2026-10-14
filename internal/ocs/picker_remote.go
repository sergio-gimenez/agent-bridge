package ocs

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"
)

// The picker's two ways to hand a session between machines, without leaving
// it: Ctrl+O opens a push panel in place of the card, and Ctrl+R browses
// another machine's sessions, where Enter pulls one and resumes it here.
// Anything that talks to the other machine runs in the background, so the
// picker keeps drawing (and its spinner turning) meanwhile.

type panelState int

const (
	panelChecking panelState = iota
	panelReady
	panelPushing
	panelDone
	panelFailed
)

type pushPanel struct {
	session Session
	hosts   []string
	host    int
	// Where the project is on that host: the same path, or checkouts of the
	// same git origin. ^d steps through them.
	dirs  []string
	dir   int
	plan  *pushPlan
	state panelState
	// While pushing: what it is doing. Once done: what it did.
	status []string
	err    string
	// Bumped by every check or push, so a result that comes back after the
	// host was switched is dropped.
	gen int
	// No to copying the project over: the question goes, ^p still does it.
	declined bool
	// Where the question's buttons were last drawn, for clicks.
	buttons []panelButton
}

// panelButton is a clickable stretch of one line of a panel or card body:
// the line's index, and the first and last cell counted from where the body's
// content starts.
type panelButton struct {
	row, start, end int
	key             Key
}

// asking reports whether the panel asks to copy the project over: it is
// missing there and that is all that stops the push.
func (panel *pushPanel) asking() bool {
	return panel.state == panelReady && panel.plan.offersDirCopy() && !panel.declined
}

// typed reports a plain letter key, typed or clicked in the footer.
func typed(key Key, letter string) bool {
	return key.Text == letter || !key.Ctrl && key.Name == letter
}

type remoteView struct {
	hosts   []string
	index   int
	loading bool
	pulling bool
	err     string
	// Why the last pull did not happen.
	notice []string
	// The session whose pull stopped only because its project is not here,
	// which ^p copies over, and whether the question about it was put away.
	copyOffer    string
	copyDeclined bool
	// Where the question's buttons were last drawn, for clicks.
	buttons []panelButton
	gen     int
}

// asking reports whether the card asks to copy the selected session's project
// over and pull it.
func (v *remoteView) asking(selected *Session) bool {
	return selected != nil && selected.ID == v.copyOffer && !v.copyDeclined && !v.pulling
}

func (v *remoteView) host() string { return v.hosts[v.index] }

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (p *picker) spinner() string { return spinnerFrames[p.spin%len(spinnerFrames)] }

// busy reports whether something is under way, so the spinner should turn.
func (p *picker) busy() bool {
	return p.panel != nil && (p.panel.state == panelChecking || p.panel.state == panelPushing) ||
		p.view != nil && (p.view.loading || p.view.pulling)
}

// background runs work off the picker's loop and applies what it returns on
// the loop. Without a loop (tests) it runs inline.
func (p *picker) background(work func() func()) {
	if p.async == nil {
		work()()
		return
	}
	go func() { p.async <- work() }()
}

// dial reaches a host quietly: output from copies and hooks would scribble
// over the picker.
func (p *picker) dial(host string) remote {
	if p.options.Dial != nil {
		return p.options.Dial(host, io.Discard)
	}
	return dialRemote(host, io.Discard)
}

// The push panel.

func (p *picker) openPanel(session Session) {
	panel := &pushPanel{session: session, hosts: p.options.Hosts}
	p.panel = panel
	if len(panel.hosts) == 0 {
		panel.state, panel.err = panelFailed, `no machine to push to: add "moveHosts": ["desk"] to the agb config`
		return
	}
	p.checkPanel(true)
}

// checkPanel probes the selected host and works out the push. With findDirs it
// first asks where the project is there.
func (p *picker) checkPanel(findDirs bool) {
	panel := p.panel
	panel.gen++
	gen := panel.gen
	panel.state, panel.plan, panel.err, panel.status, panel.declined = panelChecking, nil, "", nil, false
	host, session, config := panel.hosts[panel.host], panel.session, p.options.Config
	dirs, dir := panel.dirs, ""
	if !findDirs && len(dirs) > 0 {
		dir = dirs[panel.dir]
	}
	p.background(func() func() {
		link := p.dial(host)
		if findDirs {
			dirs = nil
			if same, repos, err := remoteDirCandidates(link, session); err == nil {
				if same != "" {
					dirs = append(dirs, same)
				}
				dirs = append(dirs, repos...)
			}
			if len(dirs) == 0 {
				dirs = []string{session.Directory}
			}
			dir = dirs[0]
		}
		plan, err := planPush(session, config, MoveOptions{Host: host, RemoteDir: dir}, link)
		return func() {
			if p.panel != panel || panel.gen != gen {
				return
			}
			if findDirs {
				panel.dirs, panel.dir = dirs, 0
			}
			if err != nil {
				panel.state, panel.err = panelFailed, err.Error()
				return
			}
			panel.plan, panel.state = &plan, panelReady
		}
	})
}

// runPush copies and arrives, and says what it did.
func runPush(plan pushPlan, link remote) ([]string, error) {
	if err := plan.copy(link); err != nil {
		return nil, err
	}
	done := []string{"Pushed to " + plan.Host}
	if plan.Hook == "" {
		return append(done, "resume it there: ssh -t "+plan.Host+" "+shellQuote(plan.Resume)), nil
	}
	if err := plan.arrive(link); err != nil {
		return done, err
	}
	return append(done, "arrive hook ran on "+plan.Host+": it carries on there"), nil
}

func (p *picker) push() {
	panel := p.panel
	plan := *panel.plan
	panel.gen++
	gen := panel.gen
	panel.state, panel.status = panelPushing, []string{"copying to " + plan.Host + "…"}
	p.background(func() func() {
		done, err := runPush(plan, p.dial(plan.Host))
		return func() { p.pushed(panel, gen, done, err) }
	})
}

// stopAndPush ends the session here, checks again, and pushes if nothing else
// stops it.
func (p *picker) stopAndPush() {
	pid := p.panel.plan.LocalPID
	stop := p.stop
	if stop == nil {
		stop = func(pid int) error { return stopProcess(pid, 10*time.Second) }
	}
	p.fixAndPush(fmt.Sprintf("stopping it here (pid %d)…", pid), func(remote) error { return stop(pid) })
}

// copyDirAndPush copies the project over to where it is missing, checks again,
// and pushes if nothing else stops it.
func (p *picker) copyDirAndPush() {
	plan := *p.panel.plan
	p.fixAndPush(fmt.Sprintf("copying %s to %s…", ShortenHome(plan.Session.Directory), plan.Host), plan.copyDir)
}

// fixAndPush clears what stops the push, checks again, and pushes if nothing
// else stops it.
func (p *picker) fixAndPush(doing string, fix func(remote) error) {
	panel := p.panel
	host, dir, session, config := panel.plan.Host, panel.plan.RemoteDir, panel.session, p.options.Config
	panel.gen++
	gen := panel.gen
	panel.state, panel.status = panelPushing, []string{doing}
	p.background(func() func() {
		link := p.dial(host)
		if err := fix(link); err != nil {
			return func() { p.pushed(panel, gen, nil, err) }
		}
		plan, err := planPush(session, config, MoveOptions{Host: host, RemoteDir: dir}, link)
		if err != nil || len(plan.Decision.Blockers) > 0 {
			return func() {
				if p.panel != panel || panel.gen != gen {
					return
				}
				if err != nil {
					panel.state, panel.err = panelFailed, err.Error()
					return
				}
				panel.plan, panel.state, panel.status = &plan, panelReady, nil
			}
		}
		done, err := runPush(plan, link)
		return func() { p.pushed(panel, gen, done, err) }
	})
}

func (p *picker) pushed(panel *pushPanel, gen int, done []string, err error) {
	if p.panel != panel || panel.gen != gen {
		return
	}
	panel.status = done
	if err != nil {
		panel.state, panel.err = panelFailed, err.Error()
		return
	}
	panel.state = panelDone
}

func (p *picker) handlePanel(key Key) (*PickResult, error) {
	panel := p.panel
	settled := panel.state != panelPushing && panel.state != panelDone
	switch {
	case key.Ctrl && key.Name == "c":
		return nil, ErrCancelled
	case key.Name == "escape":
		if panel.state != panelPushing {
			p.panel = nil
		}
	case key.Name == "enter":
		switch {
		case panel.state == panelDone, panel.state == panelFailed:
			p.panel = nil
		case panel.state == panelReady && len(panel.plan.Decision.Blockers) == 0:
			p.push()
		case panel.asking():
			p.copyDirAndPush()
		}
	case typed(key, "y") && panel.asking():
		p.copyDirAndPush()
	case typed(key, "n") && panel.asking():
		panel.declined = true
	case (key.Name == "left" || key.Name == "right") && settled && len(panel.hosts) > 1:
		step := 1
		if key.Name == "left" {
			step = len(panel.hosts) - 1
		}
		panel.host = (panel.host + step) % len(panel.hosts)
		panel.dirs, panel.dir = nil, 0
		p.checkPanel(true)
	case key.Ctrl && key.Name == "d" && settled && len(panel.dirs) > 1:
		panel.dir = (panel.dir + 1) % len(panel.dirs)
		p.checkPanel(false)
	case key.Ctrl && key.Name == "k" && panel.state == panelReady && panel.plan.LocalPID > 0:
		p.stopAndPush()
	case key.Ctrl && key.Name == "p" && panel.state == panelReady && panel.plan.offersDirCopy():
		p.copyDirAndPush()
	}
	return nil, nil
}

// Browsing another machine.

// cycleMachine steps home → each host → home.
func (p *picker) cycleMachine() {
	hosts := p.options.Hosts
	switch {
	case len(hosts) == 0:
		return
	case p.view == nil:
		p.home = p.sessions
		p.view = &remoteView{hosts: hosts}
	case p.view.index+1 < len(hosts):
		p.view.index++
	default:
		p.goHome()
		return
	}
	p.loadRemote()
}

func (p *picker) goHome() {
	p.view = nil
	p.sessions, p.home = p.home, nil
	p.active, p.offset = 0, 0
	p.refilter()
}

func (p *picker) loadRemote() {
	view := p.view
	view.gen++
	gen := view.gen
	view.loading, view.err, view.notice, view.copyOffer = true, "", nil, ""
	p.sessions, p.active, p.offset = nil, 0, 0
	p.refilter()
	host, config := view.host(), p.options.Config
	p.background(func() func() {
		out, err := p.dial(host).Probe("agb --print --json\n")
		var sessions []Session
		if err == nil {
			sessions, err = ReadListing([]byte(out), config)
		}
		return func() {
			if p.view != view || view.gen != gen {
				return
			}
			view.loading = false
			if err != nil {
				view.err = err.Error()
				if strings.Contains(view.err, "not found") {
					view.err = "agb is not installed on " + host + ", or not on its PATH for ssh: install it there to browse it"
				}
				return
			}
			p.sessions = sessions
			p.refilter()
		}
	})
}

// pullSelected pulls the selected session from the host being browsed and,
// once it is here, finishes the picker to resume it. With copyDir it first
// copies the project over if that is all that stops it.
func (p *picker) pullSelected(copyDir bool) {
	view, selected := p.view, p.selected()
	if selected == nil || view.loading || view.pulling {
		return
	}
	host := view.host()
	if selected.OpenPID > 0 {
		return // the card says why
	}
	view.pulling, view.notice, view.copyOffer = true, nil, ""
	session, config := *selected, p.options.Config
	p.background(func() func() {
		var out bytes.Buffer
		offered := false
		confirm := func(string) bool { offered = true; return copyDir }
		here, code, err := pull(config, PullOptions{Host: host, ID: session.ID, Confirm: confirm}, p.dial(host), &out)
		return func() {
			view.pulling = false
			if code != 0 || err != nil {
				view.notice = stopLines(out.String(), err)
				if offered && !copyDir {
					view.copyOffer, view.copyDeclined = session.ID, false
				}
				return
			}
			here.Title, here.Prompts, here.AssistantSnippet = session.Title, session.Prompts, session.AssistantSnippet
			result := p.finish(here, here.Account, ModeResume)
			result.PulledFrom = host
			p.done = &result
		}
	})
}

// stopLines picks the Stop: and Note: lines out of a pull's output.
func stopLines(out string, err error) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if text, ok := strings.CutPrefix(line, "Stop: "); ok {
			lines = append(lines, "✗ "+text)
		} else if text, ok := strings.CutPrefix(line, "Note: "); ok {
			lines = append(lines, "! "+text)
		}
	}
	if len(lines) == 0 && err != nil {
		lines = append(lines, "✗ "+err.Error())
	}
	return lines
}

// handleBrowsing takes the keys that mean something else while browsing.
// handled is false for the ones that work as at home (moving, typing).
func (p *picker) handleBrowsing(key Key) (result *PickResult, handled bool) {
	switch {
	case key.Name == "escape":
		p.goHome()
	case key.Name == "enter":
		p.pullSelected(p.view.asking(p.selected()))
		return p.done, true
	// While the card asks, y and n answer it rather than filter the list.
	case typed(key, "y") && p.view.asking(p.selected()):
		p.pullSelected(true)
		return p.done, true
	case typed(key, "n") && p.view.asking(p.selected()):
		p.view.copyDeclined = true
	case key.Ctrl && key.Name == "p":
		if selected := p.selected(); selected != nil && selected.ID == p.view.copyOffer {
			p.pullSelected(true)
		}
		return p.done, true
	// Routes, forks, yolo and pushing are about sessions here.
	case key.Ctrl && (key.Name == "f" || key.Name == "o" || key.Name == "t" || key.Name == "y"),
		key.Name == "tab", key.Name == "backtab":
	default:
		return nil, false
	}
	return nil, true
}

// Drawing.

func (p *picker) renderPanel(width, height int) []string {
	panel := p.panel
	inner := width - 4
	session := panel.session
	var body []string
	add := func(line string) { body = append(body, line) }
	wrapped := func(mark, text string, color func(string) string) {
		for i, line := range wrapText(text, inner-2) {
			prefix := "  "
			if i == 0 {
				prefix = mark + " "
			}
			add(color(prefix + line))
		}
	}

	add(bold(truncatePlain(sessionLabel(&session)+" · "+session.Title, inner)))
	add(dim(truncatePlain(strings.Join([]string{ShortenHome(session.Directory), session.ID, relativeTime(session.UpdatedAtMs, p.now()) + " ago"}, " · "), inner)))
	add("")

	if len(panel.hosts) > 0 {
		var hosts []string
		for i, host := range panel.hosts {
			if i == panel.host {
				hosts = append(hosts, accent("‹ "+host+" ›"))
			} else {
				hosts = append(hosts, dim(host))
			}
		}
		add(dim("to   ") + strings.Join(hosts, "  "))
	}
	if len(panel.dirs) > 0 {
		dir := panel.dirs[panel.dir]
		note := "(same path)"
		if dir != session.Directory {
			note = "(same git origin)"
		}
		if len(panel.dirs) > 1 {
			note += fmt.Sprintf("  %d/%d", panel.dir+1, len(panel.dirs))
		}
		add(dim("dir  ") + ShortenHome(dir) + "  " + dim(note))
	}
	add("")

	host := ""
	if len(panel.hosts) > 0 {
		host = panel.hosts[panel.host]
	}
	switch {
	case panel.state == panelChecking:
		add(accent(p.spinner()) + " checking " + host + "…")
	case panel.state == panelPushing:
		for _, line := range panel.status {
			add(accent(p.spinner()) + " " + line)
		}
	case panel.state == panelDone:
		for _, line := range panel.status {
			wrapped("✓", line, green)
		}
		add("")
		add(dim("^r " + host + " lists it there"))
	default:
		if plan := panel.plan; plan != nil {
			for _, line := range plan.Decision.Passed {
				wrapped("✓", line, green)
			}
			for _, line := range plan.Decision.Warnings {
				wrapped("!", line, yellow)
			}
			for _, line := range plan.Decision.Blockers {
				line = strings.Replace(line, " (--force to push anyway)", "", 1)
				if panel.asking() {
					// The question below offers the copy; the rsync line is noise here.
					line, _, _ = strings.Cut(line, "; clone or create it first")
				}
				wrapped("✗", line, red)
			}
		}
		for _, line := range panel.status {
			wrapped("✓", line, green)
		}
		if panel.err != "" {
			wrapped("✗", panel.err, red)
		}
	}

	if plan := panel.plan; plan != nil && panel.state != panelDone {
		add("")
		add(dim("sends ") + strings.Join(plan.sends(), dim(" · ")))
		if plan.Hook != "" {
			add(dim("then  ") + "arrive hook on " + plan.Host)
			add(dim("      " + truncatePlain(arriveCommand(plan.Hook, plan.There, plan.Resume), inner-6)))
		} else {
			add(dim("then  ") + "resume it there with ssh -t " + plan.Host)
		}
	}
	panel.buttons = nil
	if panel.asking() {
		body = p.askCopy(panel, body, inner, height)
	}
	title := "Push"
	if host != "" {
		title = "Push to " + host
	}
	return boxed(title, body, width, height)
}

// askCopy adds the question whether to copy the project over and push.
func (p *picker) askCopy(panel *pushPanel, body []string, inner, height int) []string {
	plan := panel.plan
	question := fmt.Sprintf("%s is not on %s. Clone it there, or copy it over now with rsync and push?", ShortenHome(plan.RemoteDir), plan.Host)
	body = append(body, "")
	lines, buttons := questionBox(question, "Copy & push", inner, len(body))
	for _, b := range buttons {
		if b.row < height-2 { // drawn, not cut off below the panel
			panel.buttons = append(panel.buttons, b)
		}
	}
	return append(body, lines...)
}

// questionBox draws a yes/no question in a frame of its own, inner cells
// wide, with yes and "Not now" buttons under it. Its first line goes at index
// line of a panel or card body; it returns where the buttons land there.
func questionBox(question, yes string, inner, line int) ([]string, []panelButton) {
	frame := accent
	width := inner - 4 // inside the question's own "│ " and " │"
	lines := []string{frame("╭" + strings.Repeat("─", inner-2) + "╮")}
	row := func(content string) { lines = append(lines, frame("│")+" "+padLine(content, width)+" "+frame("│")) }
	for _, text := range wrapText(question, width) {
		row(text)
	}
	row("")
	choices := []struct {
		label string
		draw  func(string) string
		key   Key
	}{
		{" " + yes + " ", func(v string) string { return style("1;7;36", v) }, Key{Name: "enter"}},
		{" Not now ", func(v string) string { return style("7", v) }, Key{Text: "n"}},
	}
	var buttons []panelButton
	text, used := "", 0
	for _, c := range choices {
		cells := visibleWidth(c.label)
		if used > 0 && used+2+cells > width {
			row(text)
			text, used = "", 0
		}
		if used > 0 {
			text, used = text+"  ", used+2
		}
		start := 2 + used // past the question's own "│ "
		buttons = append(buttons, panelButton{line + len(lines), start, start + cells - 1, c.key})
		text, used = text+c.draw(c.label), used+cells
	}
	row(text)
	return append(lines, frame("╰"+strings.Repeat("─", inner-2)+"╯")), buttons
}

// sends names what a push carries, briefly.
func (plan pushPlan) sends() []string {
	parts := []string{"transcript"}
	if len(plan.Files) > 1 || len(plan.Moves) > 1 {
		parts = append(parts, "tool results")
	}
	if plan.Memory != "" {
		parts = append(parts, "memory")
	}
	if n := len(plan.Referenced); n > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) it refers to", n))
	}
	return parts
}

// boxed draws lines in the card's rounded frame, titled.
func boxed(title string, body []string, width, height int) []string {
	inner := width - 4
	border := func(value string) string { return dim(value) }
	top := border("╭─") + " " + bold(title) + " "
	top += border(strings.Repeat("─", max(0, width-visibleWidth(top)-1)) + "╮")
	lines := []string{top}
	for i := 0; i < height-2; i++ {
		content := ""
		if i < len(body) {
			content = truncatePlain(body[i], inner)
			if visibleWidth(body[i]) <= inner {
				content = body[i]
			}
		}
		lines = append(lines, border("│")+" "+padLine(content, inner)+" "+border("│"))
	}
	return append(lines, border("╰"+strings.Repeat("─", width-2)+"╯"))
}

// remoteCardLines is what the card says about a session on another machine,
// above its prompts, starting at index top of the card's body.
func (p *picker) remoteCardLines(session *Session, inner, top int) []string {
	view := p.view
	host := view.host()
	asking := view.asking(session)
	view.buttons = nil
	var lines []string
	switch {
	case view.pulling:
		lines = append(lines, accent(p.spinner()+" pulling it from "+host+"…"))
	case session.OpenPID > 0:
		lines = append(lines, red(truncatePlain(fmt.Sprintf("● open on %s (pid %d): quit it there first", host, session.OpenPID), inner)))
	default:
		lines = append(lines, accent(truncatePlain("on "+host+" · ↵ pulls it here and resumes it", inner)))
	}
	for _, notice := range view.notice {
		color := red
		if strings.HasPrefix(notice, "!") {
			color = yellow
		}
		if asking {
			// The question below offers the copy; the rsync line is noise here.
			notice, _, _ = strings.Cut(notice, "; clone or create it first")
		}
		for _, line := range wrapText(notice, inner) {
			lines = append(lines, color(line))
		}
	}
	if asking {
		question := fmt.Sprintf("%s is not on this machine. Clone it here, or copy it over from %s now with rsync and pull?", ShortenHome(session.Directory), host)
		lines = append(lines, "")
		box, buttons := questionBox(question, "Copy & pull", inner, top+len(lines))
		lines, view.buttons = append(lines, box...), buttons
	}
	return lines
}

// remoteStatusCard stands in for the card while the list is loading or failed.
func (p *picker) remoteStatusCard(width, height int) []string {
	view := p.view
	var body []string
	switch {
	case view.loading:
		body = []string{accent(p.spinner()) + " fetching sessions from " + view.host() + "…"}
	case view.err != "":
		for _, line := range wrapText(view.err, width-4) {
			body = append(body, red(line))
		}
	default:
		body = []string{dim("no sessions on " + view.host())}
	}
	return boxed(view.host(), body, width, height)
}
