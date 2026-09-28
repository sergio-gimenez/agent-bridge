package ocs

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var codexTextTypes = map[string]bool{"input_text": true, "output_text": true, "text": true}

var uuidPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func codexHome(account *Account) string {
	if account != nil && account.Home != "" {
		return account.Home
	}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home
	}
	return filepath.Join(homeDir(), ".codex")
}

// Codex splits rollouts by state: active sessions live under `sessions/`,
// archived ones under `archived_sessions/`. Only the active ones are listed,
// which is how OpenCode's archived sessions are treated too.
func codexSessionsPath(account *Account) string {
	if account != nil && account.Home != "" {
		return filepath.Join(account.Home, "sessions")
	}
	if path := os.Getenv("CODEX_SESSIONS_PATH"); path != "" {
		return path
	}
	return filepath.Join(codexHome(nil), "sessions")
}

type rolloutItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Message json.RawMessage `json:"message"`
	// Pre-envelope rollouts put the session metadata on the first bare line.
	ID  json.RawMessage `json:"id"`
	Cwd json.RawMessage `json:"cwd"`
}

type rolloutLine struct {
	rolloutItem
	Payload *rolloutItem `json:"payload"`
}

// firstPresent mirrors `payload.x ?? entry.x`: the payload's value wins
// whenever it is there at all, even if it then turns out not to be a string.
func firstPresent(preferred, fallback json.RawMessage) json.RawMessage {
	if len(preferred) > 0 && string(preferred) != "null" {
		return preferred
	}
	return fallback
}

// Codex opens a session by feeding itself context (the AGENTS.md files, the
// environment block, the tool preamble) as user turns. They are markup, never
// something the person typed, so they must not become the title or match a
// search for what you actually asked.
func isCodexNoise(text string) bool {
	return strings.HasPrefix(text, "<") || strings.HasPrefix(text, "## My request for Codex:")
}

type rollout struct {
	ID        string
	Directory string
	Turns     []Turn
}

func pushTurn(into *[]Turn, role string, text string) {
	if role != string(RoleUser) && role != string(RoleAssistant) {
		return
	}
	if text == "" || (role == string(RoleUser) && isCodexNoise(text)) {
		return
	}
	*into = append(*into, Turn{Role: Role(role), Text: text})
}

func feedCodex(state *parseState, line []byte) {
	var entry rolloutLine
	if json.Unmarshal(line, &entry) != nil {
		return
	}
	payload := entry.Payload
	if payload == nil {
		payload = &rolloutItem{}
	}

	if !state.HaveID {
		if id, ok := rawString(firstPresent(payload.ID, entry.ID)); ok {
			state.ID, state.HaveID = id, true
		}
	}
	if !state.HaveDirectory {
		if cwd, ok := rawString(firstPresent(payload.Cwd, entry.Cwd)); ok {
			state.Directory, state.HaveDirectory = cwd, true
		}
	}

	if entry.Type == "event_msg" {
		// Once a structured item has turned up, events are never used again.
		if len(state.Items) > 0 {
			return
		}
		role := ""
		switch payload.Type {
		case "user_message":
			role = string(RoleUser)
		case "agent_message":
			role = string(RoleAssistant)
		}
		pushTurn(&state.Events, role, extractText(payload.Message, codexTextTypes))
		return
	}

	// `response_item` wraps the item; the oldest rollouts wrote it bare.
	item := &entry.rolloutItem
	if entry.Type == "response_item" {
		item = payload
	}
	if item.Type == "message" {
		pushTurn(&state.Items, item.Role, extractText(item.Content, codexTextTypes))
		if len(state.Items) > 0 {
			state.Events = nil
		}
	}
}

// codexTurns: both the structured `response_item` messages and the UI-level
// `event_msg` events describe the same turn, so counting both would duplicate
// every line. Response items are the richer record; events are the fallback
// for rollouts that only carry them.
func codexTurns(state *parseState) []Turn {
	if len(state.Items) > 0 {
		return state.Items
	}
	return state.Events
}

func codexRecord(state *parseState) *record {
	rec := &record{ID: state.ID, Directory: state.Directory}
	for _, turn := range codexTurns(state) {
		if turn.Role == RoleUser {
			rec.User = append(rec.User, turn.Text)
		} else {
			rec.Assistant = append(rec.Assistant, turn.Text)
		}
	}

	// A rollout with no prompt is a session that was opened and abandoned.
	if len(rec.User) == 0 {
		return nil
	}
	return rec
}

var codexFormat = transcriptFormat{feed: feedCodex, record: codexRecord}

func parseRollout(reader io.Reader) (rollout, error) {
	state, err := parseAll(reader, codexFormat)
	if err != nil {
		return rollout{}, err
	}
	return rollout{ID: state.ID, Directory: state.Directory, Turns: codexTurns(state)}, nil
}

func parseCodex(reader io.Reader) (*record, error) {
	state, err := parseAll(reader, codexFormat)
	if err != nil {
		return nil, err
	}
	return codexRecord(state), nil
}

// rollout-2025-06-01T09-30-00-<uuid>.jsonl: the id is the trailing UUID, used
// only until the file's own session metadata supplies it.
func codexIDFromName(name string) string {
	if id := uuidPattern.FindString(name); id != "" {
		return id
	}
	return strings.TrimSuffix(name, jsonlExt)
}

func codexSession(rec *record, file scannedFile, scope SearchScope, account *Account) Session {
	id := rec.ID
	if id == "" {
		id = codexIDFromName(filepath.Base(file.Path))
	}
	directory := rec.Directory
	if directory == "" {
		directory = "unknown"
	}

	search := [][]string{{rec.Directory, accountName(account)}, rec.User}
	if scope == ScopeAll {
		search = append(search, rec.Assistant)
	}

	return Session{
		ID: id,
		// Codex records no title of its own, so the opening prompt names the
		// session, the same fallback the Claude reader uses.
		Title:            Truncate(rec.User[0], 80),
		Directory:        directory,
		Source:           SourceCodex,
		UpdatedAtMs:      file.ModMs(),
		UpdatedAtLabel:   FormatUpdatedAt(file.ModMs()),
		Prompts:          lastN(rec.User, promptLimit),
		AssistantSnippet: lastN(rec.Assistant, promptLimit),
		SearchText:       joinLines(search...),
		Account:          account,
		FilePath:         file.Path,
	}
}

// Rollouts are filed under sessions/YYYY/MM/DD/, so the walk has to recurse
// rather than read one flat directory of projects.
func listCodexFiles(root string) []scannedFile {
	var files []scannedFile
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			switch {
			case entry.IsDir():
				walk(path)
			case entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), jsonlExt):
				if file, ok := statFile(path, entry); ok {
					files = append(files, file)
				}
			}
		}
	}
	walk(root)
	return files
}
