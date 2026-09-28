package ocs

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const jsonlExt = ".jsonl"

var claudeTextTypes = map[string]bool{"text": true}

func claudeProjectsPath(account *Account) string {
	if account != nil && account.Home != "" {
		return filepath.Join(account.Home, "projects")
	}
	if path := os.Getenv("CLAUDE_PROJECTS_PATH"); path != "" {
		return path
	}
	return filepath.Join(homeDir(), ".claude", "projects")
}

type claudeLine struct {
	Type    string          `json:"type"`
	AITitle json.RawMessage `json:"aiTitle"`
	Cwd     json.RawMessage `json:"cwd"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func isToolResultOnly(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	var parts []*contentPart
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part == nil || part.Type != "tool_result" {
			return false
		}
	}
	return true
}

var slashCommand = regexp.MustCompile(`^/[a-z][a-z-]*$`)

func isClaudeNoise(text string) bool {
	return strings.HasPrefix(text, "<command-") ||
		strings.HasPrefix(text, "<local-command-") ||
		strings.HasPrefix(text, "<system-reminder") ||
		strings.HasPrefix(text, "Caveat:") ||
		strings.Contains(text, "<local-command-stdout>") ||
		slashCommand.MatchString(text)
}

func feedClaude(state *parseState, line []byte) {
	var entry claudeLine
	if json.Unmarshal(line, &entry) != nil {
		return
	}

	if entry.Type == "ai-title" {
		if title, ok := rawString(entry.AITitle); ok {
			state.Title = title
			return
		}
	}

	if !state.HaveDirectory {
		if cwd, ok := rawString(entry.Cwd); ok {
			state.Directory, state.HaveDirectory = cwd, true
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
		if text := extractText(entry.Message.Content, claudeTextTypes); text != "" && !isClaudeNoise(text) {
			state.User = append(state.User, text)
		}
	case "assistant":
		if text := extractText(entry.Message.Content, claudeTextTypes); text != "" {
			state.Assistant = append(state.Assistant, text)
		}
	}
}

// claudeRecord is nil when the transcript holds no session worth listing: no
// title and nothing you typed.
func claudeRecord(state *parseState) *record {
	if len(state.User) == 0 && state.Title == "" {
		return nil
	}
	return &record{
		Title:     state.Title,
		Directory: state.Directory,
		User:      state.User,
		Assistant: state.Assistant,
	}
}

var claudeFormat = transcriptFormat{feed: feedClaude, record: claudeRecord}

func parseClaude(reader io.Reader) (*record, error) {
	state, err := parseAll(reader, claudeFormat)
	if err != nil {
		return nil, err
	}
	return claudeRecord(state), nil
}

func claudeSession(rec *record, file scannedFile, scope SearchScope, account *Account) Session {
	title := rec.Title
	if title == "" {
		first := "Untitled Claude session"
		if len(rec.User) > 0 {
			first = rec.User[0]
		}
		title = Truncate(first, 80)
	}
	directory := rec.Directory
	if directory == "" {
		directory = "unknown"
	}

	search := [][]string{{title, directory, accountName(account)}, rec.User}
	if scope == ScopeAll {
		search = append(search, rec.Assistant)
	}

	return Session{
		ID:               strings.TrimSuffix(filepath.Base(file.Path), jsonlExt),
		Title:            title,
		Directory:        directory,
		Source:           SourceClaude,
		UpdatedAtMs:      file.ModMs(),
		UpdatedAtLabel:   FormatUpdatedAt(file.ModMs()),
		Prompts:          lastN(rec.User, promptLimit),
		AssistantSnippet: lastN(rec.Assistant, promptLimit),
		SearchText:       joinLines(search...),
		Account:          account,
		FilePath:         file.Path,
	}
}

// listClaudeFiles finds <projects>/<project>/<session>.jsonl, one level deep.
func listClaudeFiles(root string) []scannedFile {
	projects, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var files []scannedFile
	for _, project := range projects {
		projectPath := filepath.Join(root, project.Name())
		entries, err := os.ReadDir(projectPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), jsonlExt) {
				continue
			}
			if file, ok := statFile(filepath.Join(projectPath, entry.Name()), entry); ok {
				files = append(files, file)
			}
		}
	}
	return files
}
