import fs from "node:fs"
import os from "node:os"
import path from "node:path"

import { OPENCODE_ACCOUNT } from "./targets.js"
import type { Account, SessionSource } from "./types.js"

export type OcsConfig = {
  // When true, launch the target tool with permission checks bypassed
  // (claude: --dangerously-skip-permissions, opencode: --auto,
  // codex: --dangerously-bypass-approvals-and-sandbox).
  skipPermissions: boolean
  // OpenCode has no accounts, so its per-tool settings live in their own block.
  opencode: Account
  claudeAccounts: Account[]
  defaultClaudeAccount: string
  codexAccounts: Account[]
  defaultCodexAccount: string
}

// Each tool names its isolated home with its own environment variable, so each
// keeps its own config key rather than sharing one generic name.
const HOME_KEY: Partial<Record<SessionSource, string>> = {
  claude: "configDir",
  codex: "codexHome",
}

const DEFAULTS: OcsConfig = {
  skipPermissions: false,
  opencode: OPENCODE_ACCOUNT,
  claudeAccounts: [{ tool: "claude", name: "cc" }],
  defaultClaudeAccount: "cc",
  codexAccounts: [{ tool: "codex", name: "cx" }],
  defaultCodexAccount: "cx",
}

function expandHome(value: string) {
  if (value === "~") return os.homedir()
  if (value.startsWith("~/")) return path.join(os.homedir(), value.slice(2))
  return value
}

export function configPath() {
  return (
    process.env.OCS_CONFIG_PATH ??
    path.join(os.homedir(), ".config", "ocs", "config.json")
  )
}

// Only a real boolean sets a per-account default; anything else leaves the key
// out, so the account falls back to the global setting.
function skipPermissionsOf(entry: unknown): Pick<Account, "skipPermissions"> {
  const value = (entry as Record<string, unknown> | undefined)?.skipPermissions
  return typeof value === "boolean" ? { skipPermissions: value } : {}
}

function parseAccounts(raw: unknown, tool: SessionSource, fallback: Account[]): Account[] {
  const homeKey = HOME_KEY[tool] as string
  const seen = new Set<string>()

  const accounts = Array.isArray(raw)
    ? raw.flatMap((entry: Record<string, unknown>) => {
        const home = entry?.[homeKey]
        if (!entry || typeof entry.name !== "string") return []
        if (home !== undefined && typeof home !== "string") return []

        const name = entry.name.trim().toLowerCase()
        if (!name || seen.has(name)) return []
        seen.add(name)
        return [{
          tool,
          name,
          ...(home ? { home: expandHome(home) } : {}),
          ...skipPermissionsOf(entry),
        }]
      })
    : []

  return accounts.length > 0 ? accounts : fallback.map((account) => ({ ...account }))
}

function pickDefault(accounts: Account[], requested: unknown) {
  const name = typeof requested === "string" ? requested.trim().toLowerCase() : ""
  return accounts.some((account) => account.name === name) ? name : accounts[0].name
}

export function parseConfig(raw: string): OcsConfig {
  const parsed = JSON.parse(raw) as Record<string, unknown>
  const claudeAccounts = parseAccounts(parsed.claudeAccounts, "claude", DEFAULTS.claudeAccounts)
  const codexAccounts = parseAccounts(parsed.codexAccounts, "codex", DEFAULTS.codexAccounts)

  return {
    skipPermissions:
      typeof parsed.skipPermissions === "boolean"
        ? parsed.skipPermissions
        : DEFAULTS.skipPermissions,
    opencode: { ...OPENCODE_ACCOUNT, ...skipPermissionsOf(parsed.opencode) },
    claudeAccounts,
    defaultClaudeAccount: pickDefault(claudeAccounts, parsed.defaultClaudeAccount),
    codexAccounts,
    defaultCodexAccount: pickDefault(codexAccounts, parsed.defaultCodexAccount),
  }
}

export function loadConfig(): OcsConfig {
  try {
    return parseConfig(fs.readFileSync(configPath(), "utf8"))
  } catch {
    return parseConfig("{}")
  }
}

// A tool's default account leads its group, so "the first account of this
// tool" is the configured default wherever a route lands on a tool without
// naming an account.
function defaultFirst(accounts: Account[], defaultName: string): Account[] {
  return [
    ...accounts.filter((account) => account.name === defaultName),
    ...accounts.filter((account) => account.name !== defaultName),
  ]
}

// The picker cycles one ordered list of destinations: OpenCode, then every
// Claude account, then every Codex account.
export function configuredTargets(config: OcsConfig): Account[] {
  return [
    config.opencode,
    ...defaultFirst(config.claudeAccounts, config.defaultClaudeAccount),
    ...defaultFirst(config.codexAccounts, config.defaultCodexAccount),
  ]
}

// The CLI flag decides for this run; without one, the target's own setting
// beats the global default. Ctrl+Y in the picker overrides all of these.
export function defaultSkipPermissions(
  config: OcsConfig,
  cliFlag: boolean | undefined,
  target: Account,
) {
  const configured = configuredTargets(config).find(
    (candidate) => candidate.tool === target.tool && candidate.name === target.name,
  )
  return cliFlag ?? configured?.skipPermissions ?? config.skipPermissions
}
