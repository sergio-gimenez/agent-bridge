// Package ocs lists OpenCode, Claude Code and Codex sessions in one picker and
// opens the chosen one in any configured tool and account.
package ocs

import "strings"

type Source string

const (
	SourceOpencode Source = "opencode"
	SourceClaude   Source = "claude"
	SourceCodex    Source = "codex"
)

// SearchScope decides whether assistant replies are part of what a query
// matches, or only the prompts you typed.
type SearchScope string

const (
	ScopeUser SearchScope = "user"
	ScopeAll  SearchScope = "all"
)

// Account is one configured identity of a tool: a Claude account (its
// CLAUDE_CONFIG_DIR), a Codex account (its CODEX_HOME), or OpenCode, which has
// only one.
type Account struct {
	Tool Source
	Name string
	// The tool's isolated home. Empty means the tool's own default home.
	Home string
	// Launch this account with permission checks bypassed by default. Nil
	// defers to the global config setting.
	SkipPermissions *bool
}

type Session struct {
	ID               string
	Title            string
	Directory        string
	ProjectID        string
	Source           Source
	UpdatedAtMs      float64
	UpdatedAtLabel   string
	Prompts          []string
	AssistantSnippet []string
	SearchText       string
	// The account this session belongs to. Nil for OpenCode, which has one.
	Account *Account
	// File-backed sources (Claude JSONL, Codex rollout) record where the
	// transcript lives, so seeding never has to hunt for it again.
	FilePath string

	// SearchText lowercased once, since every keystroke searches it again.
	searchLowered *string
}

func (session *Session) searchLower() string {
	if session.searchLowered == nil {
		lowered := strings.ToLower(session.SearchText)
		session.searchLowered = &lowered
	}
	return *session.searchLowered
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Turn struct {
	Role Role
	Text string
}
