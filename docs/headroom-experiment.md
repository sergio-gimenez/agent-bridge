# Headroom pilot

Prepared 2026-10-10. Status: an explicit Claude pilot runner, local recorder,
JSON report, and outcome command are implemented. Ordinary picker launches
don't enroll automatically. Synthetic checks aren't experiment results.

Test whether Headroom reduces context usage during ordinary AgentBridge work
without adding recovery work or breaking account/session handling.

## Assignment

Start with one account and a fixed model. Randomize new task/session lineages
50/50 between compression and passthrough, using shuffled blocks of four
(two assignments per group). Save each assignment before launching; resumes
and forks of that lineage retain it. Don't switch compression halfway through
a conversation, because prior compressed context and prompt caches carry over.
Existing sessions can serve as smoke checks, but exclude them from the primary
comparison. Record task type before assignment: debugging, implementation,
exploration, or other.

Both groups use the same Go HTTP recorder. The treatment uses Headroom 0.40.0
in cache mode; the control forwards directly to Anthropic. A synthetic wire
check found that `--no-optimize`, even with the explicit bypass header, changes
message cache markers. It isn't a faithful native control. This experiment
therefore measures enabling Headroom as a whole, including cache behavior and
proxy overhead. Keep memory injection, learning, Serena/code-memory setup, output
shaping, effort routing, and response caching disabled in both groups. Preserve
the native model, reasoning settings, account home, and authentication path.
Use a private proxy instance with fixed settings for each treatment launch.
The pilot disables Kompress and its fallback, so it measures structural
compression without downloading ML models. It also disables tool-schema
deferral. Don't change these settings during the experiment.

Check native resume, fork, indexing, and account isolation before expanding
the pilot. Don't replay live tasks twice against the working checkout.

Both recorders request uncompressed HTTP response bodies so provider streaming
usage remains observable. This changes transport encoding equally in both
groups; it doesn't change the model request content.

Review after 5–7 days, aiming for at least 20 independent task lineages per
group. That count is a practical pilot target, not a power calculation. If
traffic is sparse or uncertainty remains large, extend the pilot. Compare
groups within the same account/model; don't pool different models blindly.

## Measurements

| Measure | Collection and interpretation |
| --- | --- |
| Input tokens before/after compression | Headroom counters; label tokenizer estimates separately from provider usage. Report total savings and per-session distribution. |
| Provider input/output and cached tokens | Provider response usage or native session records where available. Count retries and retrieval requests. Keep cache reads/writes separate; missing means unknown. |
| Latency | Per-request compression time, time to first output, and completion time where available. Report median/p95 from individual samples; cumulative sums/counts only give a mean. |
| Recovery overhead | Retrieval calls, misses, retrieved tokens, and extra requests. Attribute to the assigned lineage. |
| Reliability | Launch failures, proxy/upstream errors, 429s, fallbacks, and broken resume/fork/indexing. Include unsuccessful launches in the assigned group. |
| Task outcome | Manual completed/partial/abandoned status, acceptance check result when applicable, and corrections caused by missing or distorted context. Record a brief reason. |
| Subscription usage | Fresh `agb limits --json --refresh` observations before/after, with window/reset metadata. Treat as exploratory; concurrent account activity and rolling resets prevent clean attribution. |

Raw token removal doesn't equal monetary savings. Report cache-aware API cost
only when usage and applicable prices exist; on subscriptions, don't present
list-price estimates as money saved or promise more quota from fewer tokens.
Total tokens per accepted task include output, retrievals, retries, and failed
attempts. Report completion rate alongside that metric so abandoned tasks don't
make either group look efficient.

Headroom's durable savings ledger and `/stats` provide starting points, but
global counters can't identify overlapping sessions. Verify the installed
version's event fields and session/request identifiers before implementation.
Use isolated collection or attributable request events where identifiers are
missing; don't subtract shared counters and call the delta one session's usage.

The runner stores local metadata: experiment ID, assigned group, pseudonymous
lineage and launch IDs, account label, tool/model/version/settings, task type,
timestamps, metric source, counters, and outcome. No prompts or credentials.
Store files with user-only permissions. The repository's `.headroom-pilot/`
directory is gitignored; the command defaults to the user's AgentBridge cache.
Headroom's persistent retrieval cache necessarily contains original tool output.
The runner keeps it private and sets a seven-day TTL so resumes can recover
previously compressed content. Keep it until enrolled tasks finish. Enable local
telemetry, disable the upload beacon, and keep full message logging off.

## Report and decision

Show sample counts, observation coverage, overrides/fallbacks, and a table with
control, treatment, difference, and uncertainty for each metric. Analyze by
original assignment, retaining failures; show actual routing separately.
Bootstrap independent lineages for exploratory confidence intervals, keeping
all launches/requests from each lineage together. With a small pilot, quality
comparisons remain descriptive and cannot prove equal accuracy.

Proposed adoption bar: at least 15% lower total provider tokens per accepted
task, no observed context-related quality regression, and no more than 10%
slower median time to first output. These are product preferences, not known
effects. Review tail latency and cache-aware cost too. Any lost session or
account-isolation failure stops the pilot for repair. If results are mixed,
limit integration to workloads that benefit; if evidence is weak, continue
collecting instead of declaring success.

## Optional MCP setup example

This entry fits an existing AgentBridge profile's `mcps` map after installing
Headroom separately and confirming its executable is on the agent's PATH:

```json
"headroom": {
  "transport": "stdio",
  "command": "headroom",
  "args": ["mcp", "serve"]
}
```

MCP registration provides on-demand tools; it doesn't enable automatic traffic
compression. Don't add this to the control profile during the pilot. A proxy
integration must associate retrieval with that session's intended proxy.

Sources checked 2026-10-10:
[proxy options](https://docs.headroomlabs.ai/docs/proxy),
[metrics](https://docs.headroomlabs.ai/docs/metrics),
[durable savings and cost basis](https://docs.headroomlabs.ai/docs/savings),
[MCP contract](https://docs.headroomlabs.ai/docs/mcp),
[Codex account-home regression](https://docs.headroomlabs.ai/docs/codex-recovery).

## Run the pilot

This workspace also has private convenience scripts configured for `cc2` and
the resolved model `claude-opus-5-5` observed in the native smoke test:

```sh
.headroom-pilot/run issue-123
.headroom-pilot/outcome issue-123 completed
.headroom-pilot/report > /tmp/headroom-report.json
```

Resume with `.headroom-pilot/run issue-123 --resume SESSION-ID`; reuse that
same task ID for forks. These scripts and the installed environment are local
and gitignored. They use `.headroom-pilot/data`; no real task results exist yet.

Build `bin/agb` with `go build -o bin/agb ./cmd/agb`. The local installation in
this workspace lives at `.headroom-pilot/venv/bin/headroom`; on another machine:

```sh
python3 -m venv .headroom-pilot/venv
.headroom-pilot/venv/bin/pip install 'headroom-ai[proxy]==0.40.0'
```

Start a task, replacing the account and model with your chosen values:

```sh
bin/agb experiment run --dir .headroom-pilot/data \
  --headroom .headroom-pilot/venv/bin/headroom \
  --target cc2 --model opus --task issue-123
```

The command prints the randomized group and opens Claude interactively. Keep
the account/model unchanged; the runner rejects changes within a data directory.
`opus` is a native alias; provider responses record the resolved model. Avoid
switching models or reasoning settings during an enrolled task, and review
resolved model IDs before comparing groups. Don't enable on-demand Headroom MCP
tools in the control account. Record task categories separately for this pilot.

Reuse `--task issue-123` when resuming or forking the same task. Append native
Claude arguments after `--`, for example `-- --resume SESSION-ID` or
`-- --resume SESSION-ID --fork-session`. Use a new task ID for independent work.
The runner doesn't automatically infer new session IDs or fork relationships;
assignment stability depends on reusing the task ID. Only launches through
this command contribute metrics.

After a task ends:

```sh
bin/agb experiment outcome --dir .headroom-pilot/data \
  --task issue-123 --status completed
bin/agb experiment report --dir .headroom-pilot/data > /tmp/headroom-report.json
```

Other outcome values are `partial` and `abandoned`. Add `--context-correction`
when missing or distorted context required a correction. Outcome updates apply
to the latest launch for that task. Reports aggregate task counts and keep
every launch, including failures. Each experiment directory allows one active
launch at a time; this prevents ambiguous overlapping counter deltas.

First-byte latency means the first HTTP response-body byte, which can be SSE
metadata rather than visible text. Per-request provider usage only covers what
the client-facing response exposes; Headroom's internal retries/retrieval
rounds may not appear individually. The report also saves allowlisted sidecar
stats and before/after retrieval counters. Retrieval misses, retrieved-token
counts, direct compression timing samples, task correctness, and acceptance
checks aren't fully automatic. Missing observations remain absent. Private
run records mark unfinished launches after crashes, and no automatic verdict
or confidence interval claims run before coverage and outcomes are reviewed.

The pilot uses the chosen account's native OAuth login, clears shell API-key
overrides, and never rewrites the account home or native config. It starts a
sidecar directly rather than invoking `headroom wrap`. Regular AgentBridge
resume/fork paths remain available outside the experiment.
