package ocs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Full transcript, with guards so a huge session doesn't blow the prompt:
// truncate each turn, then keep the most recent turns within a char budget.
const (
	maxTurnChars       = 4000
	maxTranscriptChars = 60000
)

type SessionSeed struct {
	Directory string
	Prompt    string
}

func lastTurn(turns []Turn, role Role) string {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == role {
			return turns[i].Text
		}
	}
	return ""
}

func renderTranscript(turns []Turn) string {
	rendered := make([]string, len(turns))
	for i, turn := range turns {
		label := "ASSISTANT"
		if turn.Role == RoleUser {
			label = "USER"
		}
		rendered[i] = label + ": " + Truncate(turn.Text, maxTurnChars)
	}

	// Keep the most recent turns that fit the budget; drop the oldest if over.
	var kept []string
	total := 0
	dropped := false
	for i := len(rendered) - 1; i >= 0; i-- {
		line := rendered[i]
		size := len([]rune(line))
		if len(kept) > 0 && total+size > maxTranscriptChars {
			dropped = true
			break
		}
		kept = append([]string{line}, kept...)
		total += size
	}

	if dropped {
		kept = append([]string{"[... earlier turns omitted for length ...]"}, kept...)
	}
	if len(kept) == 0 {
		return "(no transcript found)"
	}
	return strings.Join(kept, "\n\n")
}

func BuildContinuationPrompt(session Session, turns []Turn) string {
	lines := []string{
		fmt.Sprintf("Continue a prior %s conversation in a new clean session.", ToolName(session.Source)),
		"",
		"Original session: " + session.ID,
		"Original title: " + session.Title,
		"Original directory: " + session.Directory,
		"",
		"You are resuming this conversation in a fresh session. The full transcript",
		"is below. Reply directly to the latest user message first; do not restart the",
		"conversation from scratch. If some context looks incomplete, say so briefly and",
		"then continue with the most recent thread.",
	}
	if last := lastTurn(turns, RoleUser); last != "" {
		lines = append(lines, "", "Latest user message: "+Truncate(last, 320))
	}
	lines = append(lines, "", "=== TRANSCRIPT ===", renderTranscript(turns), "=== END TRANSCRIPT ===")
	return strings.Join(lines, "\n")
}

// --- OpenCode transcript (via `opencode export`) -------------------------

type exportedSession struct {
	Info *struct {
		Directory string `json:"directory"`
	} `json:"info"`
	Messages []struct {
		Info *struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"messages"`
}

var exportBanner = regexp.MustCompile(`^Exporting session:.*\n`)

func opencodeTranscript(id string) (string, []Turn, error) {
	var stdout bytes.Buffer
	command := exec.Command("opencode", "export", id)
	command.Stdout = &stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return "", nil, fmt.Errorf("Failed to export OpenCode session %s.", id)
	}

	var exported exportedSession
	if err := json.Unmarshal(exportBanner.ReplaceAll(stdout.Bytes(), nil), &exported); err != nil {
		return "", nil, fmt.Errorf("Could not read the export of OpenCode session %s: %w", id, err)
	}
	if exported.Info == nil || exported.Info.Directory == "" {
		return "", nil, fmt.Errorf("Exported OpenCode session has no directory.")
	}

	var turns []Turn
	for _, message := range exported.Messages {
		if message.Info == nil || (message.Info.Role != "user" && message.Info.Role != "assistant") {
			continue
		}
		var texts []string
		for _, part := range message.Parts {
			if part.Type == "text" {
				texts = append(texts, part.Text)
			}
		}
		if text := CollapseWhitespace(joinSpace(texts)); text != "" {
			turns = append(turns, Turn{Role: Role(message.Info.Role), Text: text})
		}
	}
	return exported.Info.Directory, turns, nil
}

// --- Claude Code transcript (parse the session JSONL) --------------------

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func findClaudeFile(session Session) string {
	// The reader records where it found the transcript; the scan below is the
	// fallback for sessions built without one.
	if session.FilePath != "" && fileExists(session.FilePath) {
		return session.FilePath
	}
	root := claudeProjectsPath(session.Account)
	projects, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, project := range projects {
		candidate := filepath.Join(root, project.Name(), session.ID+jsonlExt)
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func claudeTranscript(session Session) (string, []Turn, error) {
	path := findClaudeFile(session)
	if path == "" {
		return "", nil, fmt.Errorf("Could not find Claude session %s.", session.ID)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()

	directory := ""
	var turns []Turn
	err = eachLine(file, func(line []byte) {
		var entry claudeLine
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		if directory == "" {
			if cwd, ok := rawString(entry.Cwd); ok {
				directory = cwd
			}
		}
		if entry.Message == nil {
			return
		}
		switch entry.Type {
		case "user":
			if isToolResultOnly(entry.Message.Content) {
				return
			}
			text := extractText(entry.Message.Content, claudeTextTypes)
			if text != "" && !strings.HasPrefix(text, "<") && !strings.HasPrefix(text, "Caveat:") {
				turns = append(turns, Turn{Role: RoleUser, Text: text})
			}
		case "assistant":
			if text := extractText(entry.Message.Content, claudeTextTypes); text != "" {
				turns = append(turns, Turn{Role: RoleAssistant, Text: text})
			}
		}
	})
	if err != nil {
		return "", nil, err
	}
	if directory == "" {
		directory, _ = os.Getwd()
	}
	return directory, turns, nil
}

// --- Codex transcript (parse the session rollout JSONL) ------------------

func findCodexFile(session Session) string {
	if session.FilePath != "" && fileExists(session.FilePath) {
		return session.FilePath
	}
	// Rollouts are filed by date, so without a recorded path the only way back
	// to one is to walk the tree looking for the id in the file name.
	for _, file := range listCodexFiles(codexSessionsPath(session.Account)) {
		if strings.Contains(filepath.Base(file.Path), session.ID) {
			return file.Path
		}
	}
	return ""
}

func codexTranscript(session Session) (string, []Turn, error) {
	path := findCodexFile(session)
	if path == "" {
		return "", nil, fmt.Errorf("Could not find Codex session %s.", session.ID)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()

	parsed, err := parseRollout(file)
	if err != nil {
		return "", nil, err
	}
	directory := parsed.Directory
	if directory == "" {
		directory, _ = os.Getwd()
	}
	return directory, parsed.Turns, nil
}

// BuildSessionSeed turns a session into a fresh session's opening prompt, for
// routes that cross a tool or an account.
func BuildSessionSeed(session Session) (SessionSeed, error) {
	var (
		directory string
		turns     []Turn
		err       error
	)
	switch session.Source {
	case SourceClaude:
		directory, turns, err = claudeTranscript(session)
	case SourceCodex:
		directory, turns, err = codexTranscript(session)
	default:
		directory, turns, err = opencodeTranscript(session.ID)
	}
	if err != nil {
		return SessionSeed{}, err
	}

	session.Directory = directory
	return SessionSeed{Directory: directory, Prompt: BuildContinuationPrompt(session, turns)}, nil
}
