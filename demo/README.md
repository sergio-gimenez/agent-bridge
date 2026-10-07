# Demo

Everything needed to run and record `agb` against **synthetic** sessions. No
real session history is read, and nothing is ever launched.

## Try it

```bash
make demo
```

That builds the fixture and drops you into the real picker. Type to filter,
`Ctrl+T` / `Shift+Tab` to cycle the target across OpenCode, both Claude accounts
and both Codex accounts, `Enter` or `Tab` to "open", which prints the command it
*would* have run instead of running it.

## A second machine

Entries in `data.json` with `"machine": "desk"` go to another fixture home,
`demo/.fixture/desk`, which stands in for a second machine. `make demo` points
`AGB_DEMO_REMOTES` at it, so `Ctrl+O` pushes to "desk", `Ctrl+R` browses it, and
`Enter` there pulls for real, all inside `demo/.fixture/`. Desk has stub
`claude`, `codex` and `herdr` commands and a link to `bin/agb`, so it answers
the way a real machine does. `AGB_MACHINE_NAME=laptop` keeps the recording
machine's own hostname out of the header.

```bash
make demo-record DEMO=push && make demo-gif DEMO=push   # docs/push.gif
make demo-record DEMO=pull && make demo-gif DEMO=pull   # docs/pull.gif
```

The pull demo starts a stand-in process "resuming" desk's newest session, so
the recording shows a session that is still open there and cannot be pulled.

## What the fixture is

`make demo-fixture` turns [`data.json`](data.json) into a complete fake home
under `demo/.fixture/home`, laid out exactly where `agb` looks by default:

```
.local/share/opencode/opencode.db   OpenCode's SQLite store
.claude-cc1/projects/...            Claude account "cc1"
.claude-cc2/projects/...            Claude account "cc2"
.codex-cx1/sessions/...             Codex account "cx1"
.codex-cx2/sessions/...             Codex account "cx2"
.config/agentbridge/config.json     AgentBridge config naming every account
```

Running the demo is then just `HOME=<that>`, with no path overrides and nothing
pointing back at the real machine. The fixture home path is hardcoded in
[`fixture/main.go`](fixture/main.go) rather than read from `$HOME`, so the builder cannot write
into a real home directory. `demo/.fixture/` is gitignored.

Timestamps are relative to build time, so the picker always shows a plausible
recent history. The session cache lands inside the fixture home too
(`.cache/agentbridge/`), so the demo never touches your real one.

## Adding sessions

Edit [`data.json`](data.json). Nothing in it is real: the projects, prompts
and replies are invented so anyone can regenerate the recording. Each entry is:

```json
{
  "tool": "opencode",
  "ago": 41,
  "dir": "~/code/aurora-web",
  "title": "Auth redirect loops on expired session",
  "turns": [["user", "..."], ["assistant", "..."]]
}
```

`tool` is `opencode` or an account name (`cc1`, `cc2`, `cx1`, `cx2`). `ago` is
minutes before build time. Codex stores no title, so a Codex row is titled by
its opening prompt and `title` there is only a label for whoever edits the
file.

Search matches titles, directories and **user** prompts by default (assistant
text only with `--assistant`), so if you want a query to match in the recording,
put the term in a user turn or the title.

## Re-recording the README GIF

```bash
make demo-record   # -> docs/demo.cast   (needs asciinema)
make demo-gif      # -> docs/demo.gif    (needs agg, or docker)
```

`record.sh` pipes [`keys.sh`](keys.sh) into `asciinema rec`. asciinema
forwards its stdin to the recorded process's pty, so the picker sees a genuine
interactive session; the pauses in `keys.sh` become the typing rhythm in the
recording. To change what the demo does, edit the steps there.

`gif.sh` renders the cast with [agg](https://github.com/asciinema/agg), using a
local binary if present and the official container image otherwise.

Both scripts unset `CLAUDE_CONFIG_DIR`, `CLAUDE_PROJECTS_PATH`, `CODEX_HOME`,
`CODEX_SESSIONS_PATH`, `OPENCODE_DB_PATH`, `AGB_CONFIG_PATH`, `AGB_CACHE_PATH`
and `XDG_CACHE_HOME` so nothing from
the recorder's own environment leaks into the recording.
