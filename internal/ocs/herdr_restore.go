package ocs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

func herdrBinary() string {
	if binary := os.Getenv("HERDR_BIN_PATH"); binary != "" {
		return binary
	}
	return "herdr"
}

func herdrAccountHome(account Account) string {
	if account.Home != "" {
		return account.Home
	}
	return filepath.Join(homeDir(), "."+string(account.Tool))
}

func herdrNativeHook(account Account) string {
	root := herdrAccountHome(account)
	if account.Tool == SourceClaude {
		root = filepath.Join(root, "hooks")
	}
	return filepath.Join(root, "herdr-agent-state.sh")
}

// Use native argv with an explicit account home, rather than a picker route:
// account renames and changed agb configuration must not turn restore into a
// cross-account fork. env also clears a default account's inherited override.
func herdrResumeArgv(account Account, id, mode string) []string {
	key := "CLAUDE_CONFIG_DIR"
	if account.Tool == SourceCodex {
		key = "CODEX_HOME"
	}
	args := []string{"env"}
	if account.Home == "" {
		args = append(args, "-u", key)
	} else {
		args = append(args, key+"="+account.Home)
	}
	tool := string(account.Tool)
	if mode != tool+":dangerous" {
		mode = tool + ":safe"
	}
	args = append(args, "AGB_HERDR_LAUNCH_MODE="+mode, tool)
	if account.Tool == SourceCodex {
		args = append(args, "resume", id)
	} else {
		args = append(args, "--resume", id)
	}
	if mode == tool+":dangerous" {
		flag := "--dangerously-skip-permissions"
		if account.Tool == SourceCodex {
			flag = "--dangerously-bypass-approvals-and-sandbox"
		}
		args = append(args, flag)
	}
	return args
}

type herdrHookInput struct {
	Event      string `json:"hook_event_name"`
	SessionID  string `json:"session_id"`
	Transcript string `json:"transcript_path"`
	AgentID    string `json:"agent_id"`
	Cursor     string `json:"cursor_version"`
}

func validHerdrHook(account Account, input herdrHookInput) bool {
	if input.Event != "SessionStart" && input.Event != "UserPromptSubmit" {
		return false
	}
	if input.SessionID == "" || len(input.SessionID) > 512 || input.AgentID != "" || input.Cursor != "" || os.Getenv("CURSOR_VERSION") != "" {
		return false
	}
	if input.Event == "SessionStart" && input.Transcript == "" {
		return false
	}
	if input.Transcript != "" {
		root := filepath.Join(herdrAccountHome(account), "projects")
		if account.Tool == SourceCodex {
			root = filepath.Join(herdrAccountHome(account), "sessions")
		}
		// Resolve symlinks too: a child process or a different account must not
		// attach its transcript to this pane's resume command.
		root, rootErr := realSetupDestination(root)
		path, pathErr := realSetupDestination(input.Transcript)
		if rootErr != nil || pathErr != nil {
			return false
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		if account.Tool == SourceClaude && filepath.Base(path) != input.SessionID+".jsonl" {
			return false
		}
	}
	if account.Tool == SourceCodex {
		if inherited := os.Getenv("CODEX_THREAD_ID"); inherited != "" && inherited != input.SessionID {
			return false
		}
	}
	return true
}

func validHerdrResumeArgs(args []string) bool {
	size := 0
	for _, arg := range args {
		size += len(arg)
		if strings.ContainsRune(arg, '\'') || strings.ContainsFunc(arg, unicode.IsControl) {
			return false
		}
	}
	return len(args) <= 64 && size <= 8192
}

// The official hook owns native session identity; agb adds only resume argv.
// Check identity first and carry it in the report as an atomic fence: Herdr
// ignores the argv if the pane has since switched to a different session.
// This never reports lifecycle state or uses Herdr's reserved source prefix.
func reportHerdrResume(ctx context.Context, account Account, input herdrHookInput, argv []string) error {
	paneID := os.Getenv("HERDR_PANE_ID")
	cmd := exec.CommandContext(ctx, herdrBinary(), "pane", "get", paneID)
	data, err := cmd.Output()
	if err != nil {
		return err
	}
	var response struct {
		Result struct {
			Pane struct {
				Agent   string `json:"agent"`
				Session struct {
					Source string `json:"source"`
					Value  string `json:"value"`
				} `json:"agent_session"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	pane := response.Result.Pane
	tool := string(account.Tool)
	if pane.Agent != tool || pane.Session.Source != "herdr:"+tool || pane.Session.Value != input.SessionID {
		return fmt.Errorf("native session has not been detected")
	}
	args := []string{"pane", "report-agent-session", paneID,
		"--source", "agb:" + tool, "--agent", tool,
		"--seq", fmt.Sprint(time.Now().UnixNano()), "--agent-session-id", input.SessionID, "--"}
	args = append(args, argv...)
	return exec.CommandContext(ctx, herdrBinary(), args...).Run()
}

// Hooks are advisory, bounded, silent and always successful to their caller.
// A missing server or unsupported API must never prevent an agent starting.
func runHerdrHook(account Account, in io.Reader) {
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("HERDR_PANE_ID") == "" || os.Getenv("HERDR_SOCKET_PATH") == "" {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(in, 1<<20))
	var input herdrHookInput
	if err != nil || json.Unmarshal(raw, &input) != nil || !validHerdrHook(account, input) {
		return
	}
	argv := herdrResumeArgv(account, input.SessionID, os.Getenv("AGB_HERDR_LAUNCH_MODE"))
	if !validHerdrResumeArgs(argv) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if input.Event == "SessionStart" {
		// Hooks can run in parallel. Invoke the official hook with the original
		// payload so its identity report precedes our account-aware command.
		native := exec.CommandContext(ctx, "bash", herdrNativeHook(account), "session")
		native.Stdin = bytes.NewReader(raw)
		if native.Run() != nil {
			return
		}
	}
	for ctx.Err() == nil {
		if reportHerdrResume(ctx, account, input, argv) == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
