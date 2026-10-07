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
}

type remoteView struct {
	hosts   []string
	index   int
	loading bool
	pulling bool
	err     string
	// Why the last pull did not happen.
	notice []string
	gen    int
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
	panel.state, panel.plan, panel.err, panel.status = panelChecking, nil, "", nil
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
	panel := p.panel
	pid, host, dir, session, config := panel.plan.LocalPID, panel.plan.Host, panel.plan.RemoteDir, panel.session, p.options.Config
	panel.gen++
	gen := panel.gen
	panel.state, panel.status = panelPushing, []string{fmt.Sprintf("stopping it here (pid %d)…", pid)}
	stop := p.stop
	if stop == nil {
		stop = func(pid int) error { return stopProcess(pid, 10*time.Second) }
	}
	p.background(func() func() {
		if err := stop(pid); err != nil {
			return func() { p.pushed(panel, gen, nil, err) }
		}
		link := p.dial(host)
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
		}
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
	view.loading, view.err, view.notice = true, "", nil
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
// once it is here, finishes the picker to resume it.
func (p *picker) pullSelected() {
	view, selected := p.view, p.selected()
	if selected == nil || view.loading || view.pulling {
		return
	}
	host := view.host()
	if selected.OpenPID > 0 {
		return // the card says why
	}
	view.pulling, view.notice = true, nil
	session, config := *selected, p.options.Config
	p.background(func() func() {
		var out bytes.Buffer
		here, code, err := pull(config, PullOptions{Host: host, ID: session.ID}, p.dial(host), &out)
		return func() {
			view.pulling = false
			if code != 0 || err != nil {
				view.notice = stopLines(out.String(), err)
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
		p.pullSelected()
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
				wrapped("✗", strings.Replace(line, " (--force to push anyway)", "", 1), red)
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
	title := "Push"
	if host != "" {
		title = "Push to " + host
	}
	return boxed(title, body, width, height)
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
// above its prompts.
func (p *picker) remoteCardLines(session *Session, inner int) []string {
	view := p.view
	host := view.host()
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
		for _, line := range wrapText(notice, inner) {
			lines = append(lines, color(line))
		}
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
