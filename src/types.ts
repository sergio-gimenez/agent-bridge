export type SessionSource = "opencode" | "claude" | "codex"

// One configured identity of a tool: a Claude account (its CLAUDE_CONFIG_DIR),
// a Codex account (its CODEX_HOME), or OpenCode, which has only one.
export type Account = {
  tool: SessionSource
  name: string
  // The tool's isolated home. Omit for the tool's own default home.
  home?: string
  // Launch this account with permission checks bypassed by default. Unset
  // defers to the global config setting.
  skipPermissions?: boolean
}

export type SessionPreview = {
  id: string
  title: string
  directory: string
  projectId: string
  source: SessionSource
  updatedAtMs: number
  updatedAtLabel: string
  prompts: string[]
  assistantSnippets: string[]
  searchText: string
  // The account this session belongs to. Unset for OpenCode, which has one.
  account?: Account
  // File-backed sources (Claude JSONL, Codex rollout) record where the
  // transcript lives, so seeding never has to hunt for it again.
  filePath?: string
}

export type SessionRow = {
  id: string
  title: string
  directory: string
  project_id: string
  time_updated: number
}

export type PromptRow = {
  session_id: string
  text: string
}

export type SessionSearchScope = "user" | "all"
