package ocs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Antigravity CLI (`agy`) keeps one store per machine. Each conversation has a
// plaintext step log under brain/<id>/.system_generated/logs/transcript.jsonl;
// its title and workspace live in conversation_summaries.db beside it. The
// per-conversation databases under conversations/ hold protobuf blobs and are
// never read.
//
// agy has no variable that relocates its home (credentials sit in the OS
// keyring), so there is a single Antigravity account.

var AntigravityAccount = Account{Tool: SourceAntigravity, Name: "ag"}

func antigravityHome(account *Account) string {
	if account != nil && account.Home != "" {
		return account.Home
	}
	return filepath.Join(homeDir(), ".gemini", "antigravity-cli")
}

func antigravityBrainPath(account *Account) string {
	return filepath.Join(antigravityHome(account), "brain")
}

func antigravityTranscriptPath(account *Account, id string) string {
	return filepath.Join(antigravityBrainPath(account), id, ".system_generated", "logs", "transcript.jsonl")
}

// antigravityInstalled is true once agy has run on this machine at least once,
// which is when it creates its home. Until then it is left out of the targets.
var antigravityInstalled = func() bool {
	info, err := os.Stat(antigravityHome(nil))
	return err == nil && info.IsDir()
}

type antigravityStep struct {
	Source  string          `json:"source"`
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
}

// agy wraps what you typed in <USER_REQUEST> and appends its own metadata
// (local time, settings changes) after it. Only the request is yours.
func antigravityRequest(content string) string {
	const open, close = "<USER_REQUEST>", "</USER_REQUEST>"
	start := strings.Index(content, open)
	if start < 0 {
		return content
	}
	request := content[start+len(open):]
	if end := strings.Index(request, close); end >= 0 {
		request = request[:end]
	}
	return strings.TrimSpace(request)
}

// antigravityTurn maps one step to a turn. Tool calls, tool output (GENERIC
// steps) and steps with no text are not conversation.
func antigravityTurn(line []byte) (Turn, bool) {
	var step antigravityStep
	if json.Unmarshal(line, &step) != nil {
		return Turn{}, false
	}
	text, ok := rawString(step.Content)
	if !ok {
		return Turn{}, false
	}
	switch {
	case step.Type == "USER_INPUT" && step.Source == "USER_EXPLICIT":
		text = antigravityRequest(text)
		if strings.TrimSpace(text) == "" {
			return Turn{}, false
		}
		return Turn{Role: RoleUser, Text: text}, true
	case step.Type == "PLANNER_RESPONSE" && step.Source == "MODEL":
		if strings.TrimSpace(text) == "" {
			return Turn{}, false
		}
		return Turn{Role: RoleAssistant, Text: text}, true
	}
	return Turn{}, false
}

func feedAntigravity(state *parseState, line []byte) {
	turn, ok := antigravityTurn(line)
	if !ok {
		return
	}
	text := CollapseWhitespace(turn.Text)
	if turn.Role == RoleUser {
		state.User = append(state.User, text)
	} else {
		state.Assistant = append(state.Assistant, text)
	}
}

func antigravityRecord(state *parseState) *record {
	// A conversation with no prompt was opened and abandoned.
	if len(state.User) == 0 {
		return nil
	}
	return &record{User: state.User, Assistant: state.Assistant}
}

var antigravityFormat = transcriptFormat{feed: feedAntigravity, record: antigravityRecord}

func parseAntigravity(reader io.Reader) (*record, error) {
	state, err := parseAll(reader, antigravityFormat)
	if err != nil {
		return nil, err
	}
	return antigravityRecord(state), nil
}

// The transcript sits four levels under brain/, in a directory named by the
// conversation id.
func antigravityIDFromPath(path string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
}

func listAntigravityFiles(brain string) []scannedFile {
	entries, err := os.ReadDir(brain)
	if err != nil {
		return nil
	}
	var files []scannedFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(brain, entry.Name(), ".system_generated", "logs", "transcript.jsonl")
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if file, ok := statFile(path, statEntry{info}); ok {
			files = append(files, file)
		}
	}
	return files
}

type antigravitySummary struct {
	Title     string
	Directory string
}

func openAntigravityDB(path string) (*sql.DB, error) {
	// Opening a missing file would quietly create an empty database.
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"}).String()
	return sql.Open("sqlite", dsn)
}

// antigravitySummaries reads each conversation's title and first workspace.
// A missing or unreadable index only costs the titles and directories; the
// transcripts still list.
func antigravitySummaries(account *Account) map[string]antigravitySummary {
	summaries := map[string]antigravitySummary{}
	db, err := openAntigravityDB(filepath.Join(antigravityHome(account), "conversation_summaries.db"))
	if err != nil {
		return summaries
	}
	defer db.Close()

	rows, err := db.Query(`select conversation_id, title, workspace_uris from conversation_summaries`)
	if err != nil {
		return summaries
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, workspaces string
		if rows.Scan(&id, &title, &workspaces) != nil {
			continue
		}
		summaries[id] = antigravitySummary{Title: strings.TrimSpace(title), Directory: antigravityWorkspace(workspaces)}
	}
	return summaries
}

// workspace_uris is a JSON list of file:// URIs; the first is where agy ran.
func antigravityWorkspace(raw string) string {
	var uris []string
	if json.Unmarshal([]byte(raw), &uris) != nil {
		return ""
	}
	for _, uri := range uris {
		parsed, err := url.Parse(uri)
		if err == nil && parsed.Scheme == "file" && parsed.Path != "" {
			return parsed.Path
		}
	}
	return ""
}

func antigravitySession(rec *record, file scannedFile, summary antigravitySummary, scope SearchScope, account *Account) Session {
	directory := summary.Directory
	if directory == "" {
		directory = "unknown"
	}
	title := summary.Title
	if title == "" {
		title = Truncate(rec.User[0], 80)
	}

	search := [][]string{{summary.Title, summary.Directory, accountName(account)}, rec.User}
	if scope == ScopeAll {
		search = append(search, rec.Assistant)
	}

	return Session{
		ID:               antigravityIDFromPath(file.Path),
		Title:            title,
		Directory:        directory,
		Source:           SourceAntigravity,
		UpdatedAtMs:      file.ModMs(),
		UpdatedAtLabel:   FormatUpdatedAt(file.ModMs()),
		Prompts:          lastN(rec.User, promptLimit),
		AssistantSnippet: lastN(rec.Assistant, promptLimit),
		SearchText:       joinLines(search...),
		Account:          account,
		FilePath:         file.Path,
	}
}

func (cache *Cache) antigravitySessions(account *Account, scope SearchScope) []Session {
	files := listAntigravityFiles(antigravityBrainPath(account))
	if len(files) == 0 {
		return nil
	}
	records := cache.records(files, antigravityFormat)
	summaries := antigravitySummaries(account)

	var sessions []Session
	for i, rec := range records {
		if rec != nil {
			id := antigravityIDFromPath(files[i].Path)
			sessions = append(sessions, antigravitySession(rec, files[i], summaries[id], scope, account))
		}
	}
	return sessions
}

// antigravityTranscript keeps prose formatting for a handoff, where the
// listing collapses whitespace.
func antigravityTranscript(session Session) (sessionTranscript, error) {
	path := session.FilePath
	if path == "" {
		path = antigravityTranscriptPath(session.Account, session.ID)
	}
	file, err := os.Open(path)
	if err != nil {
		return sessionTranscript{}, fmt.Errorf("Could not find Antigravity conversation %s.", session.ID)
	}
	defer file.Close()

	transcript := sessionTranscript{SourcePath: path}
	if session.Directory != "unknown" {
		transcript.Directory = session.Directory
	}
	err = eachLine(file, func(line []byte) {
		if turn, ok := antigravityTurn(line); ok {
			transcript.Turns = append(transcript.Turns, turn)
		}
	})
	return transcript, err
}

// agy resumes by id but has no fork flag (only /fork inside the TUI), so a
// fork takes the seeded route, like OpenCode 2.
func OpenAntigravitySession(id, directory string, opts OpenOptions) (int, error) {
	args := []string{"--conversation", id}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return launch("agy", args, directory, nil)
}

// -i runs the seed as the first turn and stays interactive; -p would print
// one reply and exit.
func OpenAntigravityFresh(directory, prompt string, opts OpenOptions) (int, error) {
	var args []string
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return launch("agy", append(args, "-i", prompt), directory, nil)
}

// statEntry adapts an os.FileInfo to the fs.DirEntry statFile takes, for a
// file found by path rather than by reading its directory.
type statEntry struct{ info os.FileInfo }

func (entry statEntry) Name() string               { return entry.info.Name() }
func (entry statEntry) IsDir() bool                { return entry.info.IsDir() }
func (entry statEntry) Type() os.FileMode          { return entry.info.Mode().Type() }
func (entry statEntry) Info() (os.FileInfo, error) { return entry.info, nil }
