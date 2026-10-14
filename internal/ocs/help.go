package ocs

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

const pickerGuide = `Getting started

Run agb to browse saved conversations across OpenCode, Claude Code and Codex accounts. Type to filter by title, project directory and your prompts; words match without regard to case. Run agb --assistant to search assistant replies too.

Up/Down selects a conversation; PgUp/PgDn moves ten rows, and Home/End jumps to the first/last result. Backspace edits your search. Click a row to select it, double-click to open it, or use the mouse wheel to move the selection. Click footer shortcuts to run them.

Resuming, routing and forks

Enter resumes the selected conversation in its own account until you pin a target. Ctrl+T cycles accounts forward; Shift+Tab cycles backward. The header shows the pinned target and an arrow on a row means Enter will carry that conversation there. agb --target cx2 starts with that target pinned.

Ctrl+F branches the displayed route into a new conversation. Within the same tool and account this uses a native fork where supported; OpenCode 2 uses a transcript seed instead. Opening in another tool or account always starts a new conversation with the old transcript as context. The original stays available. Continue from the newest branch when switching again.

Tab immediately opens the selection in the next tool as a seeded fork. It launches the conversation; use Ctrl+T to choose a destination before opening instead.

The seed preserves the opening request, latest user message, recent turns and a readable saved summary when available. Long histories also save a full extracted transcript under ~/.cache/agentbridge/handoffs/ so the destination agent can recover omitted details. Keep those files while conversations still refer to them.

Starting a blank conversation

Ctrl+N opens the new conversation dialog, even with an empty session list. Tab or Ctrl+T chooses the agent/account; Shift+Tab goes backward. Type to edit the project directory, Ctrl+U clears it, and Enter starts. Esc returns to the picker. The default directory is where you ran agb.

Ctrl+E or right-click opens actions for the selected conversation, including starting a blank conversation in its project. Use Up/Down and Enter to choose an action, or click it. Esc closes the menu.

Accounts, limits and permissions

OC means OpenCode, CC means Claude Code, and CX means Codex. Configured account names appear in their labels, such as CC1 or CX2. Configure claudeAccounts with name/configDir and codexAccounts with name/codexHome in ~/.config/agentbridge/config.json. Omit the home path to use the tool's default account. Authenticate accounts with their own agent CLI before using them here.

For an isolated OpenCode wrapper, add an opencodeAccounts entry with name, command and dbPath. The wrapper sets its environment and dbPath identifies its session database. Optional configHome sets its XDG configuration root for setup plans. The default oc target remains available. Choose a wrapper with Ctrl+T or agb --target NAME; crossing stores creates a seeded fork.

The header suggests an account with available subscription headroom. Ctrl+G pins that suggestion; it doesn't switch accounts until you open a conversation. The detail card shows the selected target's limits. Unknown usage doesn't mean an account has free capacity. agb limits --refresh requests a fresh report; agb limits --json provides it for scripts.

Ctrl+Y toggles permission bypass for this launch; the red YOLO badge shows when it's enabled. agb --safe forces permission checks on; --dangerous (also --yolo or --skip-permissions) bypasses them. Defaults use the target account's skipPermissions setting, then the global setting; CLI flags override defaults and Ctrl+Y overrides flags. The toggle isn't saved.

Pushing and pulling between machines

Configure moveHosts in config.json with SSH hosts that have agb and the destination tool installed. Ctrl+O opens the push checks for a Claude Code or Codex session. Left/Right chooses the host; Ctrl+D cycles destination directories when the project exists in more than one place. Enter confirms the push after the checks pass; Esc returns. Ctrl+K stops a locally open session with SIGTERM, checks again and pushes. When the project is missing on the other machine, the panel asks whether to copy it over with rsync: Enter, y or the Copy & push button copies it, checks again and pushes; n or Not now puts the question away, and Ctrl+P still does it. OpenCode sessions don't support machine transfers.

Ctrl+R browses the next configured remote machine; Enter pulls the selection and resumes it locally. Ctrl+R cycles machines and Esc returns to local conversations. Remote actions differ from local actions: follow the footer. A session marked open on the remote machine cannot be pulled until it stops. When a pull stops because the project is missing here, the card asks the same way: Enter, y or Copy & pull copies it over and pulls; n or Not now puts the question away, and Ctrl+P still does it.

Transfers copy the session with its identity and replace the destination copy; they aren't continuous synchronization. Project code travels separately through git. Use agb push HOST SESSION-ID --dry-run or agb pull HOST SESSION-ID --dry-run to inspect checks first. --dir changes the destination project path. --force bypasses the local open-session check; it doesn't bypass every transfer check. Push --no-arrive skips the configured arrival hook.

Shared skills, MCPs and memory

agb setup --example prints a starter setup block to merge into your config. Edit its profiles, targets, skills and MCPs, then use agb plan --profile development to inspect changes. agb sync --profile development applies them. --target NAME limits a plan/sync to one target; --config PATH selects a configuration file.

agb sync --check reports drift without writing (exit 1 means changes exist). --dry-run previews changes and --json prints a machine-readable plan. Conflicts return exit 2. Conversation forks carry history; configure shared memory separately when you need tools to share saved project knowledge.

Layout and troubleshooting

Ctrl+Left/Right resizes the list and detail card; agb remembers the split. The card hides below 90 terminal columns. If your desktop intercepts those keys, change its keyboard shortcuts or click the resize hint when visible.

Missing conversations: check account paths and run agb --rescan to rebuild the index. Missing stores are skipped. Archived conversations don't appear. agb --print lists the 25 newest sessions; --print --json lists up to 200 with their IDs and open status. agb open accepts a full ID or a unique prefix.

Set AGB_DRY_RUN=1 to inspect an agent launch without starting it. Dry runs of seeded forks still prepare handoff files. For command-specific options run agb COMMAND --help.

F1 opens this guide from any picker dialog. ? opens it when the search is empty. Up/Down, PgUp/PgDn, Home/End and the wheel scroll help; Esc, F1 or ? closes it and restores your previous view. Ctrl+C quits the picker.
`

// PrintGuide shares the same guide between the terminal UI and shell users.
func PrintGuide(out io.Writer) {
	fmt.Fprint(out, "AgentBridge guide\n\n", pickerGuide, "\n")
	PrintHelp(out)
}

func (p *picker) helpFrame(cols, rows int) string {
	width := max(1, cols-1)
	p.helpPage = max(1, rows-4)
	var content bytes.Buffer
	PrintGuide(&content)
	var body []string
	for _, paragraph := range strings.Split(strings.TrimSpace(content.String()), "\n") {
		if paragraph == "" {
			body = append(body, "")
		} else {
			body = append(body, wrapText(paragraph, width)...)
		}
	}
	p.helpMax = max(0, len(body)-p.helpPage)
	p.helpOffset = clamp(p.helpOffset, 0, p.helpMax)
	p.mouseFooterRow = p.helpPage + 4
	lines := []string{accent(truncatePlain(fmt.Sprintf("AgentBridge help · %d–%d of %d", p.helpOffset+1, min(p.helpOffset+p.helpPage, len(body)), len(body)), width)), ""}
	for i := 0; i < p.helpPage; i++ {
		line := ""
		if index := p.helpOffset + i; index < len(body) {
			line = body[index]
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", p.clickableHints([]hint{{"esc", "back"}, {"pageup", "up"}, {"pagedown", "down"}}, width))
	return "\x1b[H" + strings.Join(lines, "\x1b[K\r\n") + "\x1b[K\x1b[J"
}

func (p *picker) handleHelp(key Key) (*PickResult, error) {
	if key.Mouse != nil {
		mouse := key.Mouse
		if mouse.Release {
			return nil, nil
		}
		switch mouse.Button &^ (4 | 8 | 16) {
		case 64:
			p.helpOffset = clamp(p.helpOffset-3, 0, p.helpMax)
		case 65:
			p.helpOffset = clamp(p.helpOffset+3, 0, p.helpMax)
		case 0:
			if mouse.Y == p.mouseFooterRow {
				for _, h := range p.mouseHints {
					if mouse.X >= h.start && mouse.X <= h.end {
						return p.handleHelp(h.key)
					}
				}
			}
		}
		return nil, nil
	}
	switch {
	case key.Ctrl && key.Name == "c":
		return nil, ErrCancelled
	case key.Name == "escape", key.Name == "f1", key.Text == "?":
		p.helpOpen = false
	case key.Name == "up":
		p.helpOffset = clamp(p.helpOffset-1, 0, p.helpMax)
	case key.Name == "down":
		p.helpOffset = clamp(p.helpOffset+1, 0, p.helpMax)
	case key.Name == "pageup":
		p.helpOffset = clamp(p.helpOffset-p.helpPage, 0, p.helpMax)
	case key.Name == "pagedown":
		p.helpOffset = clamp(p.helpOffset+p.helpPage, 0, p.helpMax)
	case key.Name == "home":
		p.helpOffset = 0
	case key.Name == "end":
		p.helpOffset = p.helpMax
	}
	return nil, nil
}
