# Contributing

Thanks for taking a look. This is a small, focused tool — issues and pull
requests are welcome.

## Getting set up

You need Go 1.23 or newer.

```bash
git clone https://github.com/sergio-gimenez/opencode-sessions.git
cd opencode-sessions
make test
```

Run the CLI straight from source without building:

```bash
go run ./cmd/agb            # the picker
go run ./cmd/agb --print    # the 25 most recent sessions, no TUI
```

## Working against fake sessions

You do not need real session history — or a second Claude account — to work on
this. `make demo` builds a synthetic home under `demo/.fixture/` and runs the
picker against it with opening stubbed out, so nothing is ever launched:

```bash
make demo
```

Add or edit sessions in [`demo/data.json`](demo/data.json). See
[`demo/README.md`](demo/README.md) for how the fixture is laid out and how the
README recording is regenerated.

## Before opening a pull request

```bash
make vet
make test
make build
```

CI runs the same three on Linux and macOS with Go 1.23 and the latest release,
tests under the race detector, plus a check that the demo fixture still builds
and lists.

## Notes on the code

Everything lives in `internal/ocs`; `cmd/agb` only wires it together.

- `opencode.go` reads OpenCode's SQLite store; `claude.go` and `codex.go` read
  the JSONL transcripts. All three produce the same `Session`, and
  `aggregate.go` reads every store in parallel and merges them.
- `cache.go` remembers what each transcript parsed to. Transcripts are
  append-only, so a file that grew is parsed from where the last run stopped
  (`parseState.Offset`). If you change what a parser extracts, bump
  `cacheVersion`, or users keep the old parse until the file next changes.
- Session ids are **not** portable between tools, or between accounts of one
  tool. Anything that crosses those boundaries goes through `seed.go`, which
  builds a fresh session seeded with the old transcript.
- The picker (`picker.go`) writes plain ANSI with no TUI dependency. Every
  rendered line must fit the terminal width once escape codes are stripped, or
  the layout smears; `TestFrameFitsTheTerminal` guards this.
- Opening a session `exec`s the tool, so `agb` is gone by the time it runs.
- Tests use temporary directories and fixtures rather than a real home
  directory. Keep it that way: nothing in the suite should read `~`.

## Reporting bugs

Include your OS, how you installed `agb`, and whether the session was OpenCode,
Claude Code or Codex. `agb --print` output (with paths redacted as you see fit) is usually
enough to diagnose listing problems. For issues with opening a session, run with
`AGB_DRY_RUN=1` and paste the command it would have run.
