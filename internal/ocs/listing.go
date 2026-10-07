package ocs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// `agb --print --json` lists this machine's sessions for another machine's
// picker to browse and pull from. Only file-backed sessions are listed: OpenCode
// keeps its sessions in a database, so they cannot travel.

type listedSession struct {
	ID          string   `json:"id"`
	Source      Source   `json:"source"`
	Account     string   `json:"account,omitempty"`
	Title       string   `json:"title"`
	Directory   string   `json:"directory"`
	UpdatedAtMs float64  `json:"updatedAtMs"`
	Prompts     []string `json:"prompts,omitempty"`
	Assistant   []string `json:"assistant,omitempty"`
	File        string   `json:"file"`
	OpenPID     int      `json:"openPid,omitempty"`
}

// WriteListing writes sessions as JSON, each with the process that has it open
// here, if any.
func WriteListing(out io.Writer, sessions []Session, openPID func(Session) int) error {
	listed := []listedSession{}
	for _, session := range sessions {
		if session.Source != SourceClaude && session.Source != SourceCodex {
			continue
		}
		entry := listedSession{ID: session.ID, Source: session.Source, Title: session.Title, Directory: session.Directory,
			UpdatedAtMs: session.UpdatedAtMs, Prompts: session.Prompts, Assistant: session.AssistantSnippet,
			File: session.FilePath, OpenPID: openPID(session)}
		if session.Account != nil {
			entry.Account = session.Account.Name
		}
		listed = append(listed, entry)
	}
	encoder := json.NewEncoder(out)
	return encoder.Encode(listed)
}

// ListingOpenPID returns, for WriteListing, the process that has each session
// open on this machine. /proc is read once for all of them.
func ListingOpenPID() func(Session) int {
	procs, ok := snapshotProcs()
	return func(session Session) int {
		home, _, _, err := accountFiles(session)
		if !ok || err != nil {
			return 0
		}
		return procs.open(home, session)
	}
}

// ReadListing reads another machine's listing as Sessions under this machine's
// accounts of the same names, or the tool's default account here.
func ReadListing(raw []byte, config Config) ([]Session, error) {
	var listed []listedSession
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&listed); err != nil {
		return nil, fmt.Errorf("reading the session list: %w", err)
	}
	var sessions []Session
	for _, entry := range listed {
		var accounts []Account
		var preferred string
		switch entry.Source {
		case SourceClaude:
			accounts, preferred = config.ClaudeAccounts, config.DefaultClaudeAccount
		case SourceCodex:
			accounts, preferred = config.CodexAccounts, config.DefaultCodexAccount
		default:
			continue
		}
		account := listingAccount(accounts, entry.Account, preferred)
		sessions = append(sessions, Session{ID: entry.ID, Source: entry.Source, Account: account, Title: entry.Title,
			Directory: entry.Directory, UpdatedAtMs: entry.UpdatedAtMs, UpdatedAtLabel: FormatUpdatedAt(entry.UpdatedAtMs),
			Prompts: entry.Prompts, AssistantSnippet: entry.Assistant, FilePath: entry.File, OpenPID: entry.OpenPID,
			SearchText: joinLines([]string{entry.Title, entry.Directory}, entry.Prompts)})
	}
	return sessions, nil
}

func listingAccount(accounts []Account, name, preferred string) *Account {
	for _, wanted := range []string{name, preferred} {
		for i := range accounts {
			if wanted != "" && accounts[i].Name == wanted {
				return &accounts[i]
			}
		}
	}
	if len(accounts) > 0 {
		return &accounts[0]
	}
	return nil
}

// stopProcess asks a process to end (SIGTERM, which Claude Code and Codex
// handle by saving and exiting) and waits up to timeout for it to be gone. A
// zombie counts as gone: it runs nothing and holds no files.
func stopProcess(pid int, timeout time.Duration) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stopping pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		if processGone(pid) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pid %d is still running %s after SIGTERM; quit it yourself", pid, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func processGone(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	fields := bytes.Fields(raw[bytes.LastIndexByte(raw, ')')+1:])
	return len(fields) > 0 && string(fields[0]) == "Z"
}
