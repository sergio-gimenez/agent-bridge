# Forks and handoffs

`Enter` continues the picked session, so new turns land in the original
conversation. `Ctrl+F` follows the same route but branches: you get a new session
to take in a different direction, and the original stays exactly as you left it.

## Inside one tool and account

The fork is native, so the copy carries real session state rather than a
retelling of it:

```
claude --resume <id> --fork-session
opencode --session <id> --fork
codex fork <id>
```

`opencode --session <id> --fork` is OpenCode 1; OpenCode 2's TUI has no fork
flag, so there a fork takes the transcript-seeded path below.

Ids don't survive a hop between tools or between accounts, so across one of those
both `Ctrl+F` and `Enter` fall back to the transcript-seeded path below. That
path is already a fork; the original never changes either way.

## Migration is a fork, not a move

Cross-tool and cross-account opens do **not** migrate a session. Ids are not
portable between OpenCode, Claude Code and Codex, or between two accounts of the
same tool, so `agb` starts a **new** session in the target and pastes the old
transcript in as context. Consequences worth knowing:

- The original session still sits in its own tool, untouched.
- Ping-ponging (CC → CX → OC …) leaves a **chain of partial copies**, since every
  hop rebuilds state from a transcript instead of resuming the real thing.
- Fork from the **most recent** node, at the top of the list, or you carry a
  stale branch forward.

Do it deliberately, at a boundary that makes sense: a task is finished, you want
to keep the general context, and you'd rather continue in the other tool. `Tab`
is not a live round trip, so treat it as "start fresh over there, with this
history".

## What the handoff carries

For long conversations, the handoff includes the opening request, the latest
user message, recent turns, and the latest readable compaction summary saved by
the source tool, when one exists. Code blocks and line breaks are preserved.
The opening prompt is limited to 60,000 bytes, including its metadata; this is
a CLI transport limit, not a model-specific token budget. Oversized messages
retain their beginning and end, with an explicit omission notice.

Every seeded fork saves an untruncated copy of the extracted user and assistant
text in `~/.cache/agentbridge/handoffs/`, beside the index cache, and gives the target
agent its absolute path. Turns are numbered so omitted details can be read in
ranges. The file also points to the source JSONL for tool outputs and attachments;
OpenCode's complete JSON export is saved alongside the readable transcript.
Files are private to your user, and later forks keep earlier snapshots valid.

`agb` reuses saved summaries rather than making a new model call. When history
is omitted from the prompt, it instructs the target agent to recover requirements
and decisions from the saved transcript before acting. Encrypted compaction
checkpoints cannot be reused as readable summaries across tools. Handoff files
remain until you remove them; deleting them removes that recovery path from
sessions that reference them. Dry runs also prepare these files.

## Memory travels separately

A fork carries the conversation, not what the agent has saved to memory. See
[memory and diagrams across tools](integrations.md) for plugins that let every
tool read the same memory files.
