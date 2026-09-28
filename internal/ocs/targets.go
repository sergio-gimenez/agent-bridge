package ocs

import "strings"

// OpencodeAccount is OpenCode's place in the list of targets. OpenCode keeps one
// store, so it needs no configured account, but the picker cycles a single
// list of destinations.
var OpencodeAccount = Account{Tool: SourceOpencode, Name: "oc"}

var fallbackLabel = map[Source]string{
	SourceOpencode: "OC",
	SourceClaude:   "CC",
	SourceCodex:    "CX",
}

func AccountLabel(tool Source, account *Account) string {
	if account == nil {
		return fallbackLabel[tool]
	}
	return strings.ToUpper(account.Name)
}

func ToolName(tool Source) string {
	switch tool {
	case SourceClaude:
		return "Claude Code"
	case SourceCodex:
		return "Codex"
	}
	return "OpenCode"
}

// SameAccount is true when two accounts drive the same tool out of the same
// home. OpenCode has a single home, so the tool alone decides.
func SameAccount(left, right *Account) bool {
	if left == nil || right == nil || left.Tool != right.Tool {
		return false
	}
	if left.Tool == SourceOpencode {
		return true
	}
	return left.Home == right.Home
}

// IsNativeTarget is true when the target is exactly where the session already
// lives, so the tool can resume it by id instead of being seeded with a
// transcript.
func IsNativeTarget(session *Session, target *Account) bool {
	if target == nil || target.Tool != session.Source {
		return false
	}
	own := session.Account
	if own == nil {
		own = &Account{Tool: session.Source}
	}
	return SameAccount(own, target)
}

func NextTarget(count, current, step int) int {
	if count == 0 {
		return current
	}
	return ((current+step)%count + count) % count
}

var toolOrder = []Source{SourceOpencode, SourceClaude, SourceCodex}

// NextToolTarget jumps to the next *tool*, skipping the other accounts of the
// current one: the useful move is "try this over in Codex", not "walk every
// account".
func NextToolTarget(targets []Account, from Source, preferred *Account) *Account {
	start := 0
	for i, tool := range toolOrder {
		if tool == from {
			start = i
		}
	}

	for step := 1; step <= len(toolOrder); step++ {
		tool := toolOrder[(start+step)%len(toolOrder)]
		if tool == from {
			continue
		}
		if preferred != nil && preferred.Tool == tool {
			return preferred
		}
		for i := range targets {
			if targets[i].Tool == tool {
				return &targets[i]
			}
		}
	}
	return nil
}
