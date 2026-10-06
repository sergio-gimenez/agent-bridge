package ocs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

type OpenOptions struct {
	SkipPermissions bool
	Fork            bool
	Account         *Account
}

// envOverride is one variable AgentBridge itself sets. Everything else is inherited,
// but an inherited value is not part of the route and must not be rendered as
// if AgentBridge had chosen it. An empty value means "unset this for the child".
type envOverride struct {
	Name  string
	Value string
}

// dryRunOut is where AGB_DRY_RUN reports go; tests swap it.
var dryRunOut io.Writer = os.Stdout

const seedPromptMinChars = 200

var whitespace = regexp.MustCompile(`\s`)

// A seeded prompt is a whole transcript; printing it verbatim would bury the
// command it belongs to.
func renderArg(arg string) string {
	if utf8.RuneCountInString(arg) > seedPromptMinChars || strings.Contains(arg, "\n") {
		return fmt.Sprintf("<transcript seed, %d chars>", utf8.RuneCountInString(arg))
	}
	if whitespace.MatchString(arg) {
		return strconv.Quote(arg)
	}
	return arg
}

func RenderCommand(command string, args []string, directory string, env []envOverride) string {
	var builder strings.Builder
	for _, variable := range env {
		if variable.Value != "" {
			fmt.Fprintf(&builder, "%s=%s ", variable.Name, ShortenHome(variable.Value))
		}
	}
	builder.WriteString(command)
	for _, arg := range args {
		builder.WriteString(" ")
		builder.WriteString(renderArg(arg))
	}
	fmt.Fprintf(&builder, "\n  in %s", ShortenHome(directory))
	return builder.String()
}

// AssertDirectory fails with the missing path named. A session records the cwd
// it ran in; projects get renamed and moved, so that path can be gone by the
// time we resume, and a bare exec error reads as "the tool is not installed".
func AssertDirectory(directory string) error {
	if info, err := os.Stat(directory); err == nil && info.IsDir() {
		return nil
	}
	return fmt.Errorf("Session directory no longer exists: %s\n"+
		"The project was probably moved or renamed since this session ran.", ShortenHome(directory))
}

func childEnvironment(overrides []envOverride) []string {
	env := os.Environ()
	for _, variable := range overrides {
		prefix := variable.Name + "="
		kept := env[:0]
		for _, entry := range env {
			if !strings.HasPrefix(entry, prefix) {
				kept = append(kept, entry)
			}
		}
		env = kept
		if variable.Value != "" {
			env = append(env, prefix+variable.Value)
		}
	}
	return env
}

// launch replaces this process with the tool, so the tool owns the terminal,
// its signals and its exit code outright. It only returns on failure, or in a
// dry run.
func launch(command string, args []string, directory string, env []envOverride) (int, error) {
	if err := AssertDirectory(directory); err != nil {
		return 1, err
	}

	if dryRunEnabled() {
		fmt.Fprintf(dryRunOut, "\x1b[36mwould run\x1b[0m %s\n", RenderCommand(command, args, directory, env))
		return 0, nil
	}

	binary, err := exec.LookPath(command)
	if err != nil {
		return 127, fmt.Errorf("%s not found on PATH", command)
	}
	if err := os.Chdir(directory); err != nil {
		return 1, err
	}
	err = syscall.Exec(binary, append([]string{command}, args...), childEnvironment(env))
	return 1, fmt.Errorf("could not start %s: %w", command, err)
}

// Each tool names its isolated home with its own variable. Always pass the
// key, so an account on the tool's default home explicitly clears whatever the
// surrounding shell had set.
func claudeEnvironment(account *Account) []envOverride {
	home := ""
	if account != nil {
		home = account.Home
	}
	return []envOverride{{"CLAUDE_CONFIG_DIR", home}}
}

func codexEnvironment(account *Account) []envOverride {
	home := ""
	if account != nil {
		home = account.Home
	}
	return []envOverride{{"CODEX_HOME", home}}
}

// Native resume: the session lives in this tool's own store.

func OpenOpencodeSession(id, directory string, opts OpenOptions) (int, error) {
	args := []string{"--session", id}
	// Native fork: the tool copies the history into a new session id, so the
	// original stays untouched and the copy keeps the full context.
	if opts.Fork {
		args = append(args, "--fork")
	}
	if opts.SkipPermissions {
		args = append(args, "--auto")
	}
	return launch("opencode", args, directory, nil)
}

func OpenClaudeSession(id, directory string, opts OpenOptions) (int, error) {
	args := []string{"--resume", id}
	if opts.Fork {
		args = append(args, "--fork-session")
	}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return launch("claude", args, directory, claudeEnvironment(opts.Account))
}

// Codex spells fork as its own subcommand rather than a flag on resume.
func OpenCodexSession(id, directory string, opts OpenOptions) (int, error) {
	subcommand := "resume"
	if opts.Fork {
		subcommand = "fork"
	}
	args := []string{subcommand, id}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	return launch("codex", args, directory, codexEnvironment(opts.Account))
}

// Cross-tool: session ids are not portable between OpenCode, Claude Code and
// Codex, so open a fresh session in the target tool seeded with the transcript.

func OpenOpencodeFresh(directory, prompt string, opts OpenOptions) (int, error) {
	args := []string{"run"}
	if opts.SkipPermissions {
		args = append(args, "--auto")
	}
	args = append(args, "--dir", directory, prompt)
	return launch("opencode", args, directory, nil)
}

func OpenClaudeFresh(directory, prompt string, opts OpenOptions) (int, error) {
	var args []string
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return launch("claude", append(args, prompt), directory, claudeEnvironment(opts.Account))
}

func OpenCodexFresh(directory, prompt string, opts OpenOptions) (int, error) {
	var args []string
	if opts.SkipPermissions {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	return launch("codex", append(args, prompt), directory, codexEnvironment(opts.Account))
}

// OpenWith follows the route to target: a native resume or fork when the
// session already lives there, a transcript-seeded new session otherwise.
func OpenWith(session Session, target Account, opts OpenOptions) (int, error) {
	opts.Account = &target

	if IsNativeTarget(&session, &target) {
		switch target.Tool {
		case SourceClaude:
			return OpenClaudeSession(session.ID, session.Directory, opts)
		case SourceCodex:
			return OpenCodexSession(session.ID, session.Directory, opts)
		}
		return OpenOpencodeSession(session.ID, session.Directory, opts)
	}

	// Cross-tool and cross-account ids are not portable; seed a fresh session.
	// That is already a fork: the original session is left untouched either
	// way.
	seed, err := BuildSessionSeed(session)
	if err != nil {
		return 1, err
	}
	switch target.Tool {
	case SourceClaude:
		return OpenClaudeFresh(seed.Directory, seed.Prompt, opts)
	case SourceCodex:
		return OpenCodexFresh(seed.Directory, seed.Prompt, opts)
	}
	return OpenOpencodeFresh(seed.Directory, seed.Prompt, opts)
}
