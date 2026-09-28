#!/usr/bin/env node

import { getAllSessions } from "./aggregate.js"
import { parseArgs } from "./cli-options.js"
import { printHelp, printSessions } from "./cli-output.js"
import { configuredTargets, defaultSkipPermissions, loadConfig } from "./config.js"
import type { OcsConfig } from "./config.js"
import {
  openClaudeFresh,
  openClaudeSession,
  openCodexFresh,
  openCodexSession,
  openOpencodeFresh,
  openSession,
} from "./open.js"
import { pickSession } from "./picker.js"
import { buildSessionSeed } from "./seed.js"
import { isNativeTarget } from "./targets.js"
import type { PickMode } from "./picker.js"
import type { Account, SessionPreview } from "./types.js"

async function openWith(
  session: SessionPreview,
  target: Account,
  opts: { skipPermissions: boolean; mode: PickMode },
) {
  const runOpts = { skipPermissions: opts.skipPermissions, fork: opts.mode === "fork" }

  // Native tool and account: resume the real session by id, or let the tool
  // branch it into a new one.
  if (isNativeTarget(session, target)) {
    if (target.tool === "claude") {
      return openClaudeSession(session.id, session.directory, { ...runOpts, account: target })
    }
    if (target.tool === "codex") {
      return openCodexSession(session.id, session.directory, { ...runOpts, account: target })
    }
    return openSession(session.id, session.directory, runOpts)
  }

  // Cross-tool and cross-account ids are not portable; seed a fresh session.
  // That is already a fork: the original session is left untouched either way.
  const seed = await buildSessionSeed(session)

  if (target.tool === "claude") {
    return openClaudeFresh(seed.directory, seed.prompt, { ...runOpts, account: target })
  }
  if (target.tool === "codex") {
    return openCodexFresh(seed.directory, seed.prompt, { ...runOpts, account: target })
  }
  return openOpencodeFresh(seed.directory, seed.prompt, runOpts)
}

// --target accepts any account name, or "oc"/"opencode"; the per-tool flags are
// the same choice said the long way. Without one the picker starts untargeted,
// following whatever is selected.
function resolveTarget(config: OcsConfig, requested?: string): Account | undefined {
  const targets = configuredTargets(config)
  if (!requested) return undefined

  const wanted = requested === "opencode" ? "oc" : requested
  const match = targets.find((target) => target.name === wanted)

  if (!match) {
    throw new Error(
      `Unknown target "${requested}". Configured: ${targets.map((target) => target.name).join(", ")}.`,
    )
  }

  return match
}

async function main() {
  const args = parseArgs(process.argv.slice(2))

  if (args.help) {
    printHelp()
    return
  }

  const config = loadConfig()
  const skipPermissionsFor = (target: Account) =>
    defaultSkipPermissions(config, args.skipPermissions, target)
  const initialTarget = resolveTarget(config, args.target)

  const sessions = getAllSessions({
    search: args.search,
    claudeAccounts: config.claudeAccounts,
    codexAccounts: config.codexAccounts,
  })

  if (args.print) {
    printSessions(sessions.slice(0, 25))
    return
  }

  const picked = await pickSession(sessions, args.query, {
    targets: configuredTargets(config),
    initialTarget,
    skipPermissions: skipPermissionsFor,
  })

  process.exitCode = await openWith(picked.session, picked.target, {
    skipPermissions: picked.skipPermissions ?? skipPermissionsFor(picked.target),
    mode: picked.mode,
  })
}

// `ocs --print | head` closes the pipe early; that is the caller getting what
// they asked for, not a failure worth a stack trace.
process.stdout.on("error", (error: NodeJS.ErrnoException) => {
  if (error.code === "EPIPE") process.exit(0)
  throw error
})

main().catch((error: unknown) => {
  const message = error instanceof Error ? error.message : String(error)
  process.stderr.write(`${message}\n`)
  process.exitCode = 1
})
