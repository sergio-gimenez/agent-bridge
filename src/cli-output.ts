import { shortenHome } from "./format.js"
import { accountLabel } from "./targets.js"
import type { SessionPreview } from "./types.js"

export function printHelp() {
  process.stdout.write([
    "opencode-sessions",
    "",
    "Browse OpenCode, Claude Code and Codex sessions, then open the picked one",
    "with any of them, in any configured account.",
    "",
    "Usage:",
    "  ocs",
    "  ocs --print",
    "  ocs --query \"mesh vpn\"",
    "  ocs --assistant",
    "  ocs --dangerous",
    "  ocs --target cx2",
    "",
    "Options:",
    "  --print               print recent sessions without opening picker",
    "  --query TEXT          start with a search query",
    "  --assistant           include assistant text in search",
    "  --dangerous           bypass permission checks when opening",
    "                        (claude --dangerously-skip-permissions / opencode --auto /",
    "                        codex --dangerously-bypass-approvals-and-sandbox)",
    "  --safe                force permission checks on (overrides config)",
    "  --target NAME         set the initial target: oc, or any account name",
    "  --claude-account NAME same thing, named for Claude accounts",
    "  --codex-account NAME  same thing, named for Codex accounts",
    "  -h, --help            show help",
    "",
    "Keys in the picker:",
    "  type                  filter by title, directory and your prompts",
    "  up/down, PgUp/PgDn    move the selection",
    "  Enter                 follow the route shown in the badge",
    "  Ctrl+F                fork: branch that route into a new session",
    "  Ctrl+T / Shift+Tab    cycle the target forwards / backwards",
    "  Tab                   open in the next tool, as a seeded fork",
    "  Ctrl+Y                toggle yolo (bypass permission checks) for this launch",
    "  Esc                   cancel",
    "",
    "Config: ~/.config/ocs/config.json",
    "  Sets permission and account defaults; CLI flags override per run.",
    "  skipPermissions can be set globally, per account, or under \"opencode\".",
    "",
    "Environment:",
    "  OCS_DRY_RUN=1         print what would be launched instead of launching it",
    "  OCS_CONFIG_PATH       override the config file location",
    "  OPENCODE_DB_PATH      override the OpenCode database location",
    "  CLAUDE_PROJECTS_PATH  override the default Claude projects directory",
    "  CODEX_HOME            override the default Codex home (~/.codex)",
    "  CODEX_SESSIONS_PATH   override the default Codex sessions directory",
    "",
  ].join("\n"))
}

export function printSessions(sessions: SessionPreview[]) {
  for (const session of sessions) {
    process.stdout.write(`[${accountLabel(session.source, session.account)}] ${session.title}\n`)
    process.stdout.write(`  ${shortenHome(session.directory)}\n`)
    process.stdout.write(`  ${session.updatedAtLabel}  ${session.id}\n`)

    for (const prompt of session.prompts) {
      process.stdout.write(`  ${prompt}\n`)
    }

    if (session.assistantSnippets.length > 0) {
      process.stdout.write("  assistant:\n")

      for (const snippet of session.assistantSnippets) {
        process.stdout.write(`    ${snippet}\n`)
      }
    }

    process.stdout.write("\n")
  }
}
