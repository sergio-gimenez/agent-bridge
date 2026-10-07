package ocs

import (
	"fmt"
	"io"
	"strings"
)

type Options struct {
	Print bool
	// With Print: JSON for another machine's picker (see WriteListing).
	JSON   bool
	Help   bool
	Query  string
	Search SearchScope
	// Nil defers to the config file; set means an explicit CLI override.
	SkipPermissions *bool
	// Name of the account (or "oc") the picker should start targeting.
	Target string
	// Ignore the cache for this run and rebuild it from scratch.
	Rescan bool
}

// --claude-account and --codex-account read better at the call site, but a
// target is a target: all three flags name one entry of the same list.
var targetFlags = map[string]bool{"--target": true, "--claude-account": true, "--codex-account": true}

func ParseArgs(argv []string) Options {
	options := Options{Search: ScopeUser}
	on, off := true, false

	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		next := func() string {
			if i+1 < len(argv) {
				i++
				return argv[i]
			}
			i++
			return ""
		}

		switch {
		case arg == "--print":
			options.Print = true
		case arg == "--json":
			options.JSON = true
		case arg == "--help" || arg == "-h":
			options.Help = true
		case arg == "--assistant":
			options.Search = ScopeAll
		case arg == "--rescan":
			options.Rescan = true
		case arg == "--dangerous" || arg == "--skip-permissions" || arg == "--yolo":
			options.SkipPermissions = &on
		case arg == "--safe" || arg == "--no-skip-permissions":
			options.SkipPermissions = &off
		case arg == "--query":
			options.Query = next()
		case targetFlags[arg]:
			options.Target = strings.ToLower(strings.TrimSpace(next()))
		}
	}
	return options
}

func PrintHelp(out io.Writer) {
	fmt.Fprint(out, strings.Join([]string{
		"AgentBridge (agb)",
		"",
		"Browse OpenCode, Claude Code and Codex sessions, then open the picked one",
		"with any of them, in any configured account.",
		"",
		"Usage:",
		"  agb",
		"  agb --print",
		"  agb --query \"mesh vpn\"",
		"  agb --assistant",
		"  agb --dangerous",
		"  agb --target cx2",
		"  agb setup --example",
		"  agb plan --profile development",
		"  agb sync --profile development",
		"  agb sync --check --json",
		"  agb push [HOST] SESSION-ID [--dir DIR] [--dry-run] [--force] [--no-arrive]",
		"  agb pull HOST SESSION-ID [--dir DIR] [--dry-run] [--force]",
		"",
		"Options:",
		"  --print               print recent sessions without opening picker",
		"  --json                with --print: list sessions as JSON (what pull browses)",
		"  --query TEXT          start with a search query",
		"  --assistant           include assistant text in search",
		"  --dangerous           bypass permission checks when opening",
		"                        (claude --dangerously-skip-permissions / opencode --auto /",
		"                        codex --dangerously-bypass-approvals-and-sandbox)",
		"  --safe                force permission checks on (overrides config)",
		"  --target NAME         set the initial target: oc, or any account name",
		"  --claude-account NAME same thing, named for Claude accounts",
		"  --codex-account NAME  same thing, named for Codex accounts",
		"  --rescan              ignore the session cache and rebuild it",
		"  -h, --help            show help",
		"",
		"Keys in the picker:",
		"  type                  filter by title, directory and your prompts",
		"  up/down, PgUp/PgDn    move the selection",
		"  Enter                 follow the route shown in the badge",
		"  Ctrl+F                fork: branch that route into a new session",
		"  Ctrl+O                push the session to another machine (Claude Code, Codex)",
		"  Ctrl+R                browse another machine's sessions; Enter pulls one here",
		"  Ctrl+T / Shift+Tab    cycle the target forwards / backwards",
		"  Tab                   open in the next tool, as a seeded fork",
		"  Ctrl+Y                toggle yolo (bypass permission checks) for this launch",
		"  Ctrl+Left/Right       resize the list and the card (remembered)",
		"  Esc                   cancel",
		"",
		"Config: ~/.config/agentbridge/config.json",
		"  Sets permission and account defaults; CLI flags override per run.",
		"  skipPermissions can be set globally, per account, or under \"opencode\".",
		"",
		"Environment:",
		"  AGB_DRY_RUN=1         print what would be launched instead of launching it",
		"  AGB_CONFIG_PATH       override the config file location",
		"  AGB_CACHE_PATH        override the session cache (~/.cache/agentbridge/index.gob)",
		"  OPENCODE_DB_PATH      override the OpenCode database location",
		"  CLAUDE_PROJECTS_PATH  override the default Claude projects directory",
		"  CODEX_HOME            override the default Codex home (~/.codex)",
		"  CODEX_SESSIONS_PATH   override the default Codex sessions directory",
		"",
		"Legacy ocs config/cache paths and OCS_* variables remain supported.",
		"",
	}, "\n"))
}

func PrintSessions(out io.Writer, sessions []Session) {
	for _, session := range sessions {
		fmt.Fprintf(out, "[%s] %s\n", AccountLabel(session.Source, session.Account), session.Title)
		fmt.Fprintf(out, "  %s\n", ShortenHome(session.Directory))
		fmt.Fprintf(out, "  %s  %s\n", session.UpdatedAtLabel, session.ID)
		for _, prompt := range session.Prompts {
			fmt.Fprintf(out, "  %s\n", prompt)
		}
		if len(session.AssistantSnippet) > 0 {
			fmt.Fprint(out, "  assistant:\n")
			for _, snippet := range session.AssistantSnippet {
				fmt.Fprintf(out, "    %s\n", snippet)
			}
		}
		fmt.Fprint(out, "\n")
	}
}
