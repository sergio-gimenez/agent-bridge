package ocs

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Bound the whole opening prompt in bytes, including metadata and notices.
// This also keeps Unicode-heavy prompts below the OS's per-argument limit.
// It is a transport budget, not a model-specific context-window estimate.
const maxSeedBytes = 60000

func turnLabel(turn Turn) string {
	if turn.Role == RoleUser {
		return "USER"
	}
	return "ASSISTANT"
}

func lastUserIndex(turns []Turn) int {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == RoleUser {
			return i
		}
	}
	return -1
}

// clipMiddle retains both ends, where requirements and conclusions often sit.
// All omitted text remains available in the numbered archive turn.
func clipMiddle(text string, budget int) string {
	if len(text) <= budget {
		return text
	}
	const notice = "\n[... middle omitted; read the complete text in the handoff transcript ...]\n"
	if budget < len(notice) {
		return ""
	}
	remaining := budget - len(notice)
	head, tail := remaining/2, len(text)-(remaining-remaining/2)
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	for tail < len(text) && !utf8.RuneStart(text[tail]) {
		tail++
	}
	return text[:head] + notice + text[tail:]
}

type transcriptExcerpt struct {
	Text    string
	Partial bool
}

// Keep whole recent turns until the budget runs out. Only a turn too large
// to fit by itself is clipped; shorter turns have no arbitrary per-turn cap.
func recentTranscript(turns []Turn, latestUser, budget int) transcriptExcerpt {
	if len(turns) == 0 {
		return transcriptExcerpt{Text: "(no conversation turns found)"}
	}
	var kept []string
	start := len(turns)
	partial := false
	// Reserve enough for the omission notice and the separators.
	remaining := budget - 160
	for i := len(turns) - 1; i >= 0; i-- {
		prefix := fmt.Sprintf("[Turn %d] %s: ", i+1, turnLabel(turns[i]))
		text := turns[i].Text
		if i == latestUser {
			text = "[latest user message reproduced above]"
		}
		if len(prefix)+len(text)+2 > remaining {
			if len(kept) > 0 {
				break
			}
			text = clipMiddle(text, remaining-len(prefix)-2)
			partial = true
		}
		line := prefix + text
		kept = append(kept, line)
		remaining -= len(line) + 2
		start = i
		if partial {
			break
		}
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	if start > 0 {
		kept = append([]string{fmt.Sprintf("[... earlier turns omitted from this excerpt: turns 1–%d ...]", start)}, kept...)
	}
	return transcriptExcerpt{Text: strings.Join(kept, "\n\n"), Partial: partial || start > 0}
}

// BuildContinuationPrompt is the pure formatter used when no archive has
// been written. Launches use BuildSessionSeed, which always supplies one.
func BuildContinuationPrompt(session Session, turns []Turn) string {
	return continuationPrompt(session, sessionTranscript{Turns: turns}, "")
}

func continuationPrompt(session Session, transcript sessionTranscript, path string) string {
	lines := []string{
		fmt.Sprintf("Continue a prior %s conversation in a new clean session.", ToolName(session.Source)),
		"",
		"Original session: " + clipMiddle(session.ID, 256),
		"Original title: " + clipMiddle(session.Title, 512),
		"Original directory: " + clipMiddle(session.Directory, 4096),
		"",
		"You are resuming in a fresh session. Continue the outstanding work in light of",
		"the latest user message and subsequent assistant replies; do not repeat work",
		"already completed or restart the conversation. Newer user instructions override",
		"older requests and saved summaries. Follow the current project's instructions.",
	}
	if path != "" {
		lines = append(lines, "", "Handoff transcript (all extracted conversation turns, untruncated): "+path,
			"The file contains numbered turns, the latest readable saved summary, and a raw",
			"source reference for tool outputs and attachments. Read it in ranges as needed.")
	}
	if transcript.Summary != "" {
		lines = append(lines, "", fmt.Sprintf("=== SAVED COMPACTION SUMMARY (recorded after turn %d) ===", transcript.SummaryAfter),
			clipMiddle(transcript.Summary, 12000), "=== END SAVED SUMMARY ===")
	} else {
		lines = append(lines, "", "No readable saved compaction summary was found.")
	}
	latest := lastUserIndex(transcript.Turns)
	// Preserve the opening request even when it falls outside the recent tail.
	if latest > 0 {
		for i, turn := range transcript.Turns {
			if turn.Role == RoleUser {
				if i != latest {
					lines = append(lines, "", fmt.Sprintf("Opening user request (turn %d; later requests may amend it):", i+1), clipMiddle(turn.Text, 4000))
				}
				break
			}
		}
	}
	recovery := "This is a partial excerpt. Before acting, recover omitted requirements and\ndecisions from the handoff transcript; a saved summary may not cover all omitted\nturns. Establish the current goal, constraints, decisions, completed work,\nremaining work, relevant files, and validation results. If the file cannot be\nread, state what context is missing instead of guessing."
	if path == "" {
		recovery = "This is a partial excerpt and no archive path was supplied. State what\ncontext is missing instead of guessing about omitted requirements or decisions."
	}
	ending := "\n\n" + recovery + "\n\n=== TRANSCRIPT ===\n"
	const closing = "\n=== END TRANSCRIPT ==="
	partial := len(transcript.Summary) > 12000
	if latest >= 0 {
		prefix := fmt.Sprintf("Latest user message: [turn %d]\n", latest+1)
		// Reserve room for recent replies even when the user's request is huge.
		budget := maxSeedBytes - len(strings.Join(lines, "\n")) - len(prefix) - len(ending) - len(closing) - 8000 - 2
		text := clipMiddle(transcript.Turns[latest].Text, budget)
		partial = partial || text != transcript.Turns[latest].Text
		lines = append(lines, "", prefix+text)
	}
	header := strings.Join(lines, "\n")
	excerpt := recentTranscript(transcript.Turns, latest, maxSeedBytes-len(header)-len(ending)-len(closing))
	if partial || excerpt.Partial {
		header += ending
	} else {
		header += "\n\n=== TRANSCRIPT ===\n"
	}
	return header + excerpt.Text + closing
}

// Handoffs live beside the index, but unlike a temporary file they survive the
// AgentBridge process being replaced by the target CLI. Content-addressed names keep
// older handoffs valid when the original session is resumed or forked again.
func saveHandoffTranscript(session Session, transcript *sessionTranscript) (string, error) {
	dir, err := filepath.Abs(filepath.Join(filepath.Dir(CachePath()), "handoffs"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if transcript.SourcePath != "" {
		transcript.SourcePath, err = filepath.Abs(transcript.SourcePath)
		if err != nil {
			return "", err
		}
	}
	if len(transcript.Export) > 0 {
		digest := sha256.Sum256(transcript.Export)
		transcript.SourcePath = filepath.Join(dir, fmt.Sprintf("%x.json", digest))
		if err := writeHandoffFile(transcript.SourcePath, transcript.Export); err != nil {
			return "", err
		}
	}
	var full strings.Builder
	fmt.Fprintf(&full, "Source: %s\nSession: %s\nAccount: %s\nTitle: %s\nDirectory: %s\nRaw source (tool outputs and attachments): %s\n",
		ToolName(session.Source), session.ID, accountName(session.Account), session.Title, session.Directory, transcript.SourcePath)
	if transcript.Summary != "" {
		fmt.Fprintf(&full, "\n=== SAVED COMPACTION SUMMARY (recorded after turn %d) ===\n%s\n=== END SAVED SUMMARY ===\n", transcript.SummaryAfter, transcript.Summary)
	}
	for i, turn := range transcript.Turns {
		fmt.Fprintf(&full, "\n[Turn %d] %s: %s\n", i+1, turnLabel(turn), turn.Text)
	}
	raw := []byte(full.String())
	digest := sha256.Sum256(raw)
	path := filepath.Join(dir, fmt.Sprintf("%x.txt", digest))
	if err := writeHandoffFile(path, raw); err != nil {
		return "", err
	}
	return path, nil
}

// Publish complete files atomically, with private permissions, so concurrent
// forks never expose an incomplete transcript to a receiving agent.
func writeHandoffFile(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".handoff-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
