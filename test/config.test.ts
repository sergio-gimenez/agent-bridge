import { describe, expect, it } from "vitest"

import { parseArgs } from "../src/cli-options.js"
import { configuredTargets, defaultSkipPermissions, parseConfig } from "../src/config.js"
import { buildContinuationPrompt } from "../src/seed.js"
import type { SessionPreview } from "../src/types.js"

const session: SessionPreview = {
  id: "sid",
  title: "Improve the picker",
  directory: "/home/dev/proj",
  projectId: "",
  source: "claude",
  updatedAtMs: 1,
  updatedAtLabel: "",
  prompts: [],
  assistantSnippets: [],
  searchText: "",
}

describe("skip-permissions flag", () => {
  it("defaults to undefined so config decides", () => {
    expect(parseArgs([]).skipPermissions).toBeUndefined()
  })

  it("enables via --dangerous / --skip-permissions / --yolo", () => {
    expect(parseArgs(["--dangerous"]).skipPermissions).toBe(true)
    expect(parseArgs(["--skip-permissions"]).skipPermissions).toBe(true)
    expect(parseArgs(["--yolo"]).skipPermissions).toBe(true)
  })

  it("forces off via --safe", () => {
    expect(parseArgs(["--safe"]).skipPermissions).toBe(false)
  })

  it("selects an initial target, however the flag is spelled", () => {
    expect(parseArgs(["--claude-account", "CC2"]).target).toBe("cc2")
    expect(parseArgs(["--codex-account", "CX2"]).target).toBe("cx2")
    expect(parseArgs(["--target", "OC"]).target).toBe("oc")
  })
})

describe("parseConfig", () => {
  it("reads skipPermissions", () => {
    expect(parseConfig('{"skipPermissions":true}').skipPermissions).toBe(true)
  })

  it("defaults skipPermissions to false", () => {
    expect(parseConfig("{}").skipPermissions).toBe(false)
  })

  it("reads Claude accounts and validates the default", () => {
    const config = parseConfig(JSON.stringify({
      claudeAccounts: [
        { name: "CC1" },
        { name: "cc2", configDir: "/tmp/cc2" },
      ],
      defaultClaudeAccount: "CC2",
    }))

    expect(config.claudeAccounts).toEqual([
      { tool: "claude", name: "cc1" },
      { tool: "claude", name: "cc2", home: "/tmp/cc2" },
    ])
    expect(config.defaultClaudeAccount).toBe("cc2")
  })

  it("reads Codex accounts from their own home key", () => {
    const config = parseConfig(JSON.stringify({
      codexAccounts: [{ name: "cx1" }, { name: "CX2", codexHome: "/tmp/cx2" }],
      defaultCodexAccount: "cx2",
    }))

    expect(config.codexAccounts).toEqual([
      { tool: "codex", name: "cx1" },
      { tool: "codex", name: "cx2", home: "/tmp/cx2" },
    ])
    expect(config.defaultCodexAccount).toBe("cx2")
  })

  it("gives each tool a default account when none is configured", () => {
    const config = parseConfig("{}")

    expect(config.claudeAccounts).toEqual([{ tool: "claude", name: "cc" }])
    expect(config.codexAccounts).toEqual([{ tool: "codex", name: "cx" }])
  })

  it("lists targets as OpenCode, then Claude, then Codex", () => {
    const config = parseConfig(JSON.stringify({
      claudeAccounts: [{ name: "cc1" }, { name: "cc2", configDir: "/tmp/cc2" }],
      codexAccounts: [{ name: "cx1" }],
    }))

    expect(configuredTargets(config).map((target) => target.name)).toEqual([
      "oc",
      "cc1",
      "cc2",
      "cx1",
    ])
  })

  it("reads per-account and OpenCode skipPermissions", () => {
    const config = parseConfig(JSON.stringify({
      opencode: { skipPermissions: true },
      claudeAccounts: [{ name: "cc1" }, { name: "cc2", skipPermissions: true }],
      codexAccounts: [{ name: "cx1", skipPermissions: "yes" }],
    }))

    expect(config.opencode.skipPermissions).toBe(true)
    expect(config.claudeAccounts[1]).toEqual({ tool: "claude", name: "cc2", skipPermissions: true })
    // Non-booleans are ignored rather than read as truthy.
    expect(config.codexAccounts[0]).toEqual({ tool: "codex", name: "cx1" })
  })

  it("resolves skipPermissions as CLI flag, then target, then global", () => {
    const config = parseConfig(JSON.stringify({
      skipPermissions: true,
      opencode: { skipPermissions: false },
      claudeAccounts: [{ name: "cc1" }, { name: "cc2", skipPermissions: false }],
    }))
    const [oc, cc1, cc2] = configuredTargets(config)

    expect(defaultSkipPermissions(config, undefined, oc)).toBe(false)
    expect(defaultSkipPermissions(config, undefined, cc1)).toBe(true)
    expect(defaultSkipPermissions(config, undefined, cc2)).toBe(false)
    expect(defaultSkipPermissions(config, true, cc2)).toBe(true)
    expect(defaultSkipPermissions(config, false, cc1)).toBe(false)
  })

  // Tab lands on a tool without naming an account, and takes the first one
  // listed for it — which has to be that tool's configured default.
  it("leads each tool's group with its default account", () => {
    const config = parseConfig(JSON.stringify({
      claudeAccounts: [{ name: "cc1" }, { name: "cc2", configDir: "/tmp/cc2" }],
      defaultClaudeAccount: "cc2",
      codexAccounts: [{ name: "cx1" }, { name: "cx2", codexHome: "/tmp/cx2" }],
      defaultCodexAccount: "cx2",
    }))

    expect(configuredTargets(config).map((target) => target.name)).toEqual([
      "oc",
      "cc2",
      "cc1",
      "cx2",
      "cx1",
    ])
  })
})

describe("buildContinuationPrompt", () => {
  it("labels source, seeds latest turn, and includes the full transcript", () => {
    const prompt = buildContinuationPrompt(session, [
      { role: "user", text: "How do I make the picker responsive?" },
      { role: "assistant", text: "Use terminal width." },
      { role: "user", text: "Add badges too." },
    ])

    expect(prompt).toContain("prior Claude Code conversation")
    expect(prompt).toContain("Original session: sid")
    expect(prompt).toContain("Latest user message: Add badges too.")
    expect(prompt).toContain("Reply directly to the latest user message first")
    // full transcript, not just the last few turns
    expect(prompt).toContain("USER: How do I make the picker responsive?")
    expect(prompt).toContain("ASSISTANT: Use terminal width.")
    expect(prompt).toContain("=== TRANSCRIPT ===")
  })

  it("drops oldest turns when over the char budget", () => {
    const big = "x".repeat(5000)
    const turns = Array.from({ length: 40 }, (_, i) => ({
      role: (i % 2 === 0 ? "user" : "assistant") as "user" | "assistant",
      text: `TURN${i}::${big}`,
    }))
    const prompt = buildContinuationPrompt(session, turns)

    expect(prompt).toContain("earlier turns omitted for length")
    // newest turn kept, oldest dropped
    expect(prompt).toContain("TURN39::")
    expect(prompt).not.toContain("TURN0::")
  })
})
