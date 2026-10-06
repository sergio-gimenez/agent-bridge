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

type SessionSeed struct {
	Directory      string
	Prompt         string
	TranscriptPath string
}

// Seeding keeps prose formatting intact; the search readers deliberately
// collapse whitespace instead. Tool calls and attachments remain in the raw
// source, referenced from the readable handoff transcript.
func transcriptText(raw json.RawMessage, textTypes map[string]bool) string {
	if value, ok := rawString(bytes.TrimSpace(raw)); ok {
		return value
	}
	var parts []*contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, part := range parts {
		if part != nil && textTypes[part.Type] {
			if value, ok := rawString(part.Text); ok {
				texts = append(texts, value)
			}
		}
	}
	return strings.Join(texts, "\n\n")
}

type sessionTranscript struct {
	Directory    string
	Turns        []Turn
	Summary      string
	SummaryAfter int // number of conversation turns preceding the saved summary
	SourcePath   string
	Export       []byte // OpenCode has no JSONL file; preserve its complete export.
}

func (transcript *sessionTranscript) saveSummary(text string) {
	if strings.TrimSpace(text) != "" {
		transcript.Summary = text
		transcript.SummaryAfter = len(transcript.Turns)
	}
}

// --- OpenCode transcript (via `opencode export`) -------------------------

type exportedSession struct {
	Info *struct {
		Directory string `json:"directory"`
	} `json:"info"`
	Messages []struct {
		Info *struct {
			Role    string          `json:"role"`
			Summary bool            `json:"summary"`
			Error   json.RawMessage `json:"error"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"messages"`
}

var exportBanner = regexp.MustCompile(`^Exporting session:.*\n`)

func parseOpencodeTranscript(raw []byte) (sessionTranscript, error) {
	var exported exportedSession
	if err := json.Unmarshal(raw, &exported); err != nil {
		return sessionTranscript{}, err
	}
	if exported.Info == nil || exported.Info.Directory == "" {
		return sessionTranscript{}, fmt.Errorf("Exported OpenCode session has no directory.")
	}
	transcript := sessionTranscript{Directory: exported.Info.Directory, Export: raw}
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
		text := strings.Join(texts, "\n\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if message.Info.Summary {
			if len(message.Info.Error) == 0 || string(message.Info.Error) == "null" {
				transcript.saveSummary(text)
			}
			continue
		}
		transcript.Turns = append(transcript.Turns, Turn{Role: Role(message.Info.Role), Text: text})
	}
	return transcript, nil
}

func opencodeTranscript(id string) (sessionTranscript, error) {
	var stdout bytes.Buffer
	command := exec.Command("opencode", "export", id)
	command.Stdout = &stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return sessionTranscript{}, fmt.Errorf("Failed to export OpenCode session %s.", id)
	}
	transcript, err := parseOpencodeTranscript(exportBanner.ReplaceAll(stdout.Bytes(), nil))
	if err != nil {
		return sessionTranscript{}, fmt.Errorf("Could not read the export of OpenCode session %s: %w", id, err)
	}
	return transcript, nil
}

// --- Claude Code transcript (parse the session JSONL) --------------------

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func findClaudeFile(session Session) string {
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

func claudeTranscript(session Session) (sessionTranscript, error) {
	path := findClaudeFile(session)
	if path == "" {
		return sessionTranscript{}, fmt.Errorf("Could not find Claude session %s.", session.ID)
	}
	file, err := os.Open(path)
	if err != nil {
		return sessionTranscript{}, err
	}
	defer file.Close()
	transcript := sessionTranscript{SourcePath: path}
	err = eachLine(file, func(line []byte) {
		var entry struct {
			claudeLine
			IsCompactSummary bool `json:"isCompactSummary"`
		}
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		if transcript.Directory == "" {
			if cwd, ok := rawString(entry.Cwd); ok {
				transcript.Directory = cwd
			}
		}
		if entry.Message == nil {
			return
		}
		text := transcriptText(entry.Message.Content, claudeTextTypes)
		if entry.IsCompactSummary {
			transcript.saveSummary(text)
			return
		}
		switch entry.Type {
		case "user":
			if !isToolResultOnly(entry.Message.Content) && strings.TrimSpace(text) != "" && !isClaudeNoise(strings.TrimSpace(text)) {
				transcript.Turns = append(transcript.Turns, Turn{RoleUser, text})
			}
		case "assistant":
			if strings.TrimSpace(text) != "" {
				transcript.Turns = append(transcript.Turns, Turn{RoleAssistant, text})
			}
		}
	})
	return transcript, err
}

// --- Codex transcript (parse the session rollout JSONL) ------------------

func findCodexFile(session Session) string {
	if session.FilePath != "" && fileExists(session.FilePath) {
		return session.FilePath
	}
	for _, file := range listCodexFiles(codexSessionsPath(session.Account)) {
		if strings.Contains(filepath.Base(file.Path), session.ID) {
			return file.Path
		}
	}
	return ""
}

func codexTranscript(session Session) (sessionTranscript, error) {
	path := findCodexFile(session)
	if path == "" {
		return sessionTranscript{}, fmt.Errorf("Could not find Codex session %s.", session.ID)
	}
	file, err := os.Open(path)
	if err != nil {
		return sessionTranscript{}, err
	}
	defer file.Close()
	transcript := sessionTranscript{SourcePath: path}
	var items, events []Turn
	var summaryAfterItems, summaryAfterEvents int
	appendTurn := func(into *[]Turn, role, text string) {
		if (role == "user" || role == "assistant") && strings.TrimSpace(text) != "" &&
			(role != "user" || !isCodexNoise(strings.TrimSpace(text))) {
			*into = append(*into, Turn{Role(role), text})
		}
	}
	err = eachLine(file, func(line []byte) {
		var entry rolloutLine
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		payload := entry.Payload
		if payload == nil {
			payload = &rolloutItem{}
		}
		if transcript.Directory == "" {
			if cwd, ok := rawString(firstPresent(payload.Cwd, entry.Cwd)); ok {
				transcript.Directory = cwd
			}
		}
		if entry.Type == "compacted" {
			if text, ok := rawString(firstPresent(payload.Message, entry.Message)); ok && strings.TrimSpace(text) != "" {
				transcript.Summary = text
				summaryAfterItems, summaryAfterEvents = len(items), len(events)
			}
			return
		}
		if entry.Type == "event_msg" {
			if len(items) == 0 {
				role := ""
				if payload.Type == "user_message" {
					role = "user"
				} else if payload.Type == "agent_message" {
					role = "assistant"
				}
				appendTurn(&events, role, transcriptText(payload.Message, codexTextTypes))
			}
			return
		}
		item := &entry.rolloutItem
		if entry.Type == "response_item" {
			item = payload
		}
		if item.Type == "message" {
			appendTurn(&items, item.Role, transcriptText(item.Content, codexTextTypes))
			if len(items) > 0 {
				events = nil
			}
		}
	})
	transcript.Turns, transcript.SummaryAfter = items, summaryAfterItems
	if len(items) == 0 {
		transcript.Turns, transcript.SummaryAfter = events, summaryAfterEvents
	}
	return transcript, err
}

// BuildSessionSeed writes a recoverable handoff before launching the target.
func BuildSessionSeed(session Session) (SessionSeed, error) {
	var transcript sessionTranscript
	var err error
	switch session.Source {
	case SourceClaude:
		transcript, err = claudeTranscript(session)
	case SourceCodex:
		transcript, err = codexTranscript(session)
	default:
		transcript, err = opencodeTranscript(session.ID)
	}
	if err != nil {
		return SessionSeed{}, err
	}
	if transcript.Directory == "" {
		transcript.Directory = session.Directory
		if transcript.Directory == "" || transcript.Directory == "unknown" {
			transcript.Directory, err = os.Getwd()
			if err != nil {
				return SessionSeed{}, err
			}
		}
	}
	session.Directory = transcript.Directory
	path, err := saveHandoffTranscript(session, &transcript)
	if err != nil {
		return SessionSeed{}, fmt.Errorf("Could not save the handoff transcript: %w", err)
	}
	return SessionSeed{
		Directory:      transcript.Directory,
		Prompt:         continuationPrompt(session, transcript, path),
		TranscriptPath: path,
	}, nil
}
