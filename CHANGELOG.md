# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Codex as a third tool.** `ocs` reads Codex rollouts from
  `~/.codex/sessions/**/rollout-*.jsonl`, lists them alongside OpenCode and
  Claude sessions under `[CX*]` badges, resumes them with `codex resume <id>`
  and forks them natively with `codex fork <id>`.
- Multiple Codex accounts, mirroring the Claude ones: `codexAccounts` and
  `defaultCodexAccount` in `~/.config/ocs/config.json`, each account naming its
  own isolated `CODEX_HOME`.
- `--target NAME` selects the initial target by account name, or `oc` for
  OpenCode. `--codex-account` joins `--claude-account` as the long way to say
  the same thing.
- `CODEX_HOME` and `CODEX_SESSIONS_PATH` override where Codex sessions are read
  from, the way `CLAUDE_PROJECTS_PATH` already did for Claude.
- `OCS_DRY_RUN=1` prints the command `ocs` would run (target tool, account,
  working directory) instead of launching it.
- A synthetic demo fixture (`npm run demo`) that builds a complete fake home of
  OpenCode, Claude and Codex sessions, so the picker can be tried, developed
  against and recorded without touching real session history. The fixture now
  creates the project directories too, so opening a demo session no longer trips
  the "session directory no longer exists" guard.
- Scripted asciinema recording of the demo (`npm run demo:record`,
  `npm run demo:gif`) behind the README's animation.
- CI running typecheck, tests and build on Node 20, 22 and 24.
- `CONTRIBUTING.md`, issue templates and a pull request template.
- `npm run install:local` suggests the plugins that share Claude Code memory
  with OpenCode and Codex, for tools that are installed and don't have one set
  up yet.
- `npm run install:local` also suggests `claude-mermaid`, the MCP server that
  renders Mermaid diagrams, for whichever of the three tools don't have it
  configured yet.
- `Ctrl+Y` in the picker toggles yolo (permission checks bypassed) for the
  launch about to be made; a red `YOLO` on the status line says it is on.
- `skipPermissions` can be set per agent: on any entry of `claudeAccounts` or
  `codexAccounts`, or under `"opencode"`. It follows the target, and sits
  between the `--yolo`/`--safe` flags and the global default.

### Changed

- `Ctrl+T` now cycles **every** destination (OpenCode, each Claude account,
  each Codex account) rather than only the Claude accounts, and `Shift+Tab`
  cycles the same list backwards. With three tools there is no single "other"
  one, so `Tab` now opens in the *next tool*, skipping the other accounts of
  the current one.
- Until the target is cycled it follows the selection, so `Enter` resumes what
  you picked in the account it already lives in. Previously the target started
  pinned to the default Claude account, which showed a `[CC2→CC1]` route on a
  session you had only just selected.

### Fixed

- Rows no longer advertise a route they are not going to take. While the target
  follows the selection, an unselected row used to render as `[OC→CX1]` because
  the target happened to sit on the highlighted Codex session; every badge now
  stays native until a target is pinned.
- A `CLAUDE_CONFIG_DIR` inherited from the surrounding shell is no longer
  printed by `OCS_DRY_RUN` as though `ocs` had chosen it.
- `ocs --print | head` no longer dies with an unhandled `EPIPE` stack trace when
  the reader closes the pipe early.
- `--print` now abbreviates the home directory to `~` the way the picker
  already did, so listings are consistent and safer to paste into an issue.

## [1.0.0]

### Added

- Multiple Claude Code accounts: `ocs` scans each configured account's projects
  directory, preserves account ownership when resuming, and shows the target
  account route in the badge (`[CC1]`, `[CC1→CC2]`).
- `Ctrl+T` cycles the target Claude account; `Shift+Tab` opens into it.
- `Enter` follows the displayed route rather than always resuming natively.
- Cross-tool open (`Tab`): because session ids are not portable between
  OpenCode and Claude Code, this forks a new session in the target tool seeded
  with the full transcript of the picked one.
- Permission bypass with `--dangerous` / `--skip-permissions` / `--yolo`, a
  `--safe` override, and a `skipPermissions` default in
  `~/.config/ocs/config.json`.
- Search across titles, directories and all user prompts, with `--assistant` to
  include assistant text.
- Responsive two-column picker that adapts to the terminal size, with match
  highlighting and paging.
- `--print` to list recent sessions without opening the picker.
- `npm run install:local` to install `ocs` into `~/.local/bin` without root.

[Unreleased]: https://github.com/sergio-gimenez/opencode-sessions/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/sergio-gimenez/opencode-sessions/releases/tag/v1.0.0
