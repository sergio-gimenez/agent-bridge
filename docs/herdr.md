# Switching agents in Herdr

Use the `sergio.agb` Herdr plugin to open the session picker in a new tab,
then continue a conversation in another agent or account. The shortcuts below
assume the plugin is installed and bound to `prefix+a`; Herdr's default prefix
is `Ctrl+B`.

## Restore conversations after a restart

Install agb's resume hooks on each machine that runs your agents:

```sh
agb herdr install --dry-run
agb herdr install
```

This needs Herdr 0.9.3 or later, `bash`, `python3`, and an installed agb binary.
Run `make install` before installing the hooks; they call that binary by its
absolute path. The installer covers the Claude and Codex accounts in your agb
configuration. Each account's config directory must already exist. To install
for just one account, use `agb herdr install --target cc2`. `--config PATH`
selects a different agb configuration file.

The installer installs Herdr's official integration in each selected account,
then adds agb handlers for `SessionStart` and `UserPromptSubmit`. It preserves
other hooks and settings, follows config-file symlinks, and saves private copies
of the original files in `.agb-herdr-backup-*` inside each account directory.
Repeated installs replace agb's handlers rather than adding duplicates. Run
the installer again after moving the agb binary or adding an account.

Start agents again to load the hooks. Existing conversations keep their history:
quit an agent normally, open agb in its pane, and resume the conversation in
its original account. Keep the Herdr server running while doing this. Codex
may ask you to review and trust the new hook definitions before it runs them;
review them in Codex's hooks settings. See the [Codex hooks documentation](https://learn.chatgpt.com/docs/hooks).

### What the hooks save

Herdr saves the space, tabs and pane directories independently of conversation
history. To resume a conversation after a server restart, it also needs the
native session ID and a command that selects its original account. A bare
`claude --resume ID` can look in the wrong store for a conversation opened
with `CLAUDE_CONFIG_DIR=~/.claude-cc3`.

Herdr's official integration reports the native ID. agb checks that the ID
belongs to the agent Herdr sees in that pane, then adds a native resume command
with the account directory included. For example:

```sh
env CLAUDE_CONFIG_DIR=/home/you/.claude-cc3 claude --resume SESSION-ID
env CODEX_HOME=/home/you/.codex-cx2 codex resume SESSION-ID
```

The saved command uses the account's absolute path. Default accounts clear
their tool's inherited account override instead. Restoring runs the native
agent directly, so an account renamed in agb doesn't redirect the saved
conversation. agb refreshes the command after a new session, native fork,
`/clear`, `/resume`, or compaction; it uses the current native ID rather than
the source conversation's ID. The prompt hook retries if Herdr hadn't detected
the agent when its startup hook ran.

For agents launched through agb, restore also keeps agb's safe/YOLO choice.
Agents started directly with `cc2`, `cc3`, or a Codex wrapper get the account
and session repair too, but use their native default permission options on
restore. Other ad hoc CLI arguments, model overrides and environment variables
aren't captured. Keep persistent preferences in the account's config.

agb adds a resume command without reporting agent lifecycle state. Herdr keeps
its normal detection and notifications. Hooks do nothing outside Herdr and
give up silently after two seconds if the server or API is unavailable.
Herdr rejects resume arguments containing apostrophes or control characters,
so account paths containing those characters can't use this integration.
OpenCode accounts continue to use their own Herdr integration.

### Verify or recover

Run `herdr agent list` and check that your pane has `agent_session`. Herdr's
`session.json` also needs an `agent_resume` entry with `source: "agb:claude"`
or `"agb:codex"` and an `argv` array containing the correct account directory.
For a named Herdr session, inspect its own `sessions/NAME/session.json`; use
`herdr --session NAME agent list` for its runtime state. Session files contain
private data, so inspect them locally.

If the ID is missing, check the official integration for that account:

```sh
CLAUDE_CONFIG_DIR="$HOME/.claude-cc2" herdr integration status
CODEX_HOME="$HOME/.codex-cx2" herdr integration status
```

If the ID exists but `agent_resume` is missing, rerun `agb herdr install`,
check that the recorded agb executable still exists, then resume the agent
through agb. Confirm Codex trusts the hooks and that Herdr has native agent
restore enabled (`[session] resume_agents_on_restore = true`, the default).

Installation doesn't fill in IDs for previously closed panes or alter running
agents. To recover an old conversation, select it in agb and resume it in its
own account. The next hook saves the account-aware restore command. Detach
with `Ctrl+B`, then `Q` when you want to leave agents running; after a host
reboot or server restart, Herdr starts new processes using the saved commands.
See [Herdr session restore](https://herdr.dev/docs/session-state/) for the
difference between reattaching and restarting.

To remove only agb's handlers:

```sh
agb herdr uninstall --dry-run
agb herdr uninstall
```

Use `--target NAME` to limit removal. Herdr's official hooks and your unrelated
settings remain. Restart agents to load the removal; an already saved restore
command stays in Herdr until that agent exits. If an installation fails, the
error names its backup directory. Earlier accounts may already be installed;
correct the error and rerun. The backups contain original settings, the
official script, and Codex's `config.toml` where applicable. An `.absent` file
records a file that didn't exist before installation.

## Connect to a remote session

Run this on your laptop, using your SSH host alias:

```sh
herdr --remote desk --remote-keybindings server
```

`--remote-keybindings server` uses the remote host's keybindings, including
its plugin actions. Without it, `herdr --remote` defaults to local keybindings
and excludes custom command bindings, so `Ctrl+B`, then `A` won't open `agb`.

If you're already attached, detach with `Ctrl+B`, then `Q`, and reconnect
with the command above. Detaching leaves your agents running.

## Continue the current conversation in another agent

1. From the agent pane, press `Ctrl+B`, then `A` to open `agb` in a new tab.
2. Select the conversation you're currently working on; type part of its title to filter.
3. Press `Ctrl+T` and choose the destination agent and account, such as `CX1` or `CC2`, by its limits.
4. Optionally press `Ctrl+Y` to enable YOLO for this launch; the dialog says so.
5. Press `Enter` to launch the destination.

A hop between tools or accounts starts a new conversation with the source
transcript as context. Your original agent stays open in its pane. See
[Migration is a fork, not a move](forking.md#migration-is-a-fork-not-a-move)
for the limits of transcript handoffs.

## Start a new conversation

![Conversation actions and the new-conversation chooser](actions.gif)

Open `agb` in a herdr pane and click `^n new` or press `Ctrl+N`. Select an
agent/account with `Tab` or `Ctrl+T`, enter the project directory, then click
`enter start`. The agent starts in that pane with a blank conversation.
You can also run `agb new --target cx1 --dir ~/my-project` in a pane.

Right-click an existing conversation to see its actions, including resuming,
forking, choosing another agent/account, pushing, and starting a new
conversation in the same project. `Ctrl+E` opens the menu from the keyboard.

## Move a conversation to herdr on desk

Open `agb` inside your local herdr. Select a Claude Code or Codex conversation
and click `^o push` in the footer (or press `Ctrl+O`). Review the destination
and checks, then click `enter push` to copy it. If the conversation is still
running locally, the panel offers `^k stop it here & push`; that action ends
the local agent before transferring its session.

With an `arrive.desk` hook configured, the push opens the conversation in a
new herdr tab on desk. The hook goes in your local
`~/.config/agentbridge/config.json` alongside `moveHosts`:

```json
{
  "moveHosts": ["desk"],
  "arrive": {
    "desk": "p=$(herdr tab create --label {title} --cwd {dir} --no-focus | jq -r .result.root_pane.pane_id) && herdr pane run \"$p\" {resume}"
  }
}
```

Merge these fields into your existing config so you keep your account settings.
Connect to desk with the command above to use the new tab. The transfer keeps
the session ID and history; it doesn't transfer a running process or your
working tree. Commit and push your code, and update the checkout on desk.

To bring it back, quit the agent on desk, open `agb` in your local herdr,
and click `^r desk` (or press `Ctrl+R`). Select the conversation and click
`enter pull & resume here`. See [Push and pull](push-pull.md) for prerequisites
and the checks that can stop a transfer. OpenCode sessions can't transfer
between machines yet.

Mouse controls require your terminal and herdr connection to forward mouse
reports to the pane. Herdr normally keeps right-clicks for its own menu; agb
automatically enables `herdr pane input --pane "$HERDR_PANE_ID" --right-click pane`
when its picker opens inside a managed pane. Right-click the pane frame to
open Herdr's menu instead. `Ctrl+E` opens agb's menu from the keyboard.

## If the shortcut doesn't work

If `Ctrl+B`, then `A` does nothing remotely, check that you connected with
`--remote-keybindings server` and that the remote host has the `sergio.agb`
plugin and its `prefix+a` binding configured.

As a fallback, open a new tab with `Ctrl+B`, then `C`, and run `agb` in its
shell. Once the picker opens, `Ctrl+T`, `Ctrl+Y` and `Enter` are `agb` controls;
they don't depend on the Herdr plugin shortcut.
