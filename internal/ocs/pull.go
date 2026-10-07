package ocs

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// agb pull is agb push run backwards, from the machine the session comes to:
// the one you are sitting at, since the other may be a laptop that is asleep.
// The session need not be known here, so the other side is searched for it.

type PullOptions struct {
	ID     string
	Host   string
	DryRun bool
	// Pull even if a copy here looks open. A session open there always stops it.
	Force bool
	// The session's directory here. Empty means the same path as there.
	LocalDir string
}

// lookPath finds the tool that resumes a pulled session; tests replace it.
var lookPath = exec.LookPath

// sessionIDPattern keeps an id prefix safe to put in a remote glob.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// pullRoot is one account's session folder, searched there for the id.
type pullRoot struct {
	Source  Source
	Account *Account
	Root    string
}

func pullRoots(config Config) []pullRoot {
	var roots []pullRoot
	for i := range config.ClaudeAccounts {
		account := &config.ClaudeAccounts[i]
		roots = append(roots, pullRoot{SourceClaude, account, claudeProjectsPath(account)})
	}
	for i := range config.CodexAccounts {
		account := &config.CodexAccounts[i]
		roots = append(roots, pullRoot{SourceCodex, account, codexSessionsPath(account)})
	}
	return roots
}

// findScript prints match=<root index>:<path> for every transcript there whose
// id starts with prefix.
func findScript(roots []pullRoot, prefix string) string {
	var s strings.Builder
	for i, root := range roots {
		if root.Source == SourceClaude {
			fmt.Fprintf(&s, "for f in %s/*/%s*.jsonl; do if [ -f \"$f\" ]; then echo \"match=%d:$f\"; fi; done\n", shellQuote(root.Root), prefix, i)
		} else {
			fmt.Fprintf(&s, "find %s -type f -name %s 2>/dev/null | sed 's/^/match=%d:/'\n", shellQuote(root.Root), shellQuote("*"+prefix+"*.jsonl"), i)
		}
	}
	s.WriteString("true\n")
	return s.String()
}

// findRemoteSession turns the find script's output into the one session it
// names, as it sits there.
func findRemoteSession(roots []pullRoot, out, prefix, host string) (Session, error) {
	var matches []Session
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "match=")
		if !ok {
			continue
		}
		index, path, ok := strings.Cut(rest, ":")
		i, err := strconv.Atoi(index)
		if !ok || err != nil || i < 0 || i >= len(roots) || seen[path] {
			continue
		}
		seen[path] = true
		root := roots[i]
		id := strings.TrimSuffix(filepath.Base(path), jsonlExt)
		if root.Source == SourceCodex {
			id = codexIDFromName(filepath.Base(path))
		}
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		matches = append(matches, Session{ID: id, Source: root.Source, Account: root.Account, FilePath: path})
	}
	switch len(matches) {
	case 0:
		return Session{}, fmt.Errorf("no session with id %q on %s", prefix, host)
	case 1:
		return matches[0], nil
	}
	return Session{}, fmt.Errorf("id %q matches %d sessions on %s; give more of it", prefix, len(matches), host)
}

// pullProbeScript reports, for the session there: its directory (from the
// transcript), what lies beside it, whether it is open, and its checkout.
func pullProbeScript(session Session, home string) string {
	var s strings.Builder
	s.WriteString(`kv() { printf '%s=%s\n' "$1" "$2"; }
yes_if() { if "$@" >/dev/null 2>&1; then echo yes; else echo no; fi; }
kv home "$HOME"
`)
	fmt.Fprintf(&s, "kv size \"$(stat -c %%s %s)\"\n", shellQuote(session.FilePath))
	fmt.Fprintf(&s, "cwd=$(grep -m1 -o '\"cwd\":\"[^\"]*\"' %s | cut -d'\"' -f4); kv cwd \"$cwd\"\n", shellQuote(session.FilePath))
	if session.Source == SourceClaude {
		fmt.Fprintf(&s, "kv sibling \"$(yes_if test -d %s)\"\n", shellQuote(strings.TrimSuffix(session.FilePath, jsonlExt)))
		fmt.Fprintf(&s, "kv memory \"$(yes_if test -d %s)\"\n", shellQuote(filepath.Join(filepath.Dir(session.FilePath), "memory")))
	}
	s.WriteString(openThereScript(session, home, session.FilePath))
	s.WriteString(`if [ -n "$cwd" ] && git -C "$cwd" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  if [ -n "$(git -C "$cwd" status --porcelain --untracked-files=no)" ]; then kv git_dirty yes; fi
  if [ -z "$(git -C "$cwd" branch -r --contains HEAD 2>/dev/null)" ]; then kv git_unpushed yes; fi
  kv git_head "$(git -C "$cwd" rev-parse HEAD)"
fi
true
`)
	return s.String()
}

// RunPullCommand parses `agb pull HOST SESSION-ID` and pulls the session.
func RunPullCommand(argv []string, out io.Writer) (int, error) {
	flags := flag.NewFlagSet("agb pull", flag.ContinueOnError)
	flags.SetOutput(out)
	opts := PullOptions{}
	flags.StringVar(&opts.LocalDir, "dir", "", "the session's directory here (default: the same path as there)")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "check and list what would be fetched, change nothing")
	flags.BoolVar(&opts.Force, "force", false, "pull even if a copy here looks open")
	positional, err := parseInterleaved(flags, argv)
	if err == flag.ErrHelp {
		return 0, nil
	} else if err != nil {
		return 2, err
	}
	if len(positional) != 2 {
		return 2, fmt.Errorf("usage: agb pull HOST SESSION-ID [--dir DIR] [--dry-run] [--force]")
	}
	opts.Host, opts.ID = positional[0], positional[1]
	opts.DryRun = opts.DryRun || dryRunEnabled()
	if opts.LocalDir != "" {
		opts.LocalDir = expandHome(opts.LocalDir)
	}
	return pullSession(LoadConfig(), opts, dialRemote(opts.Host, nil), out)
}

func pullSession(config Config, opts PullOptions, link remote, out io.Writer) (int, error) {
	_, code, err := pull(config, opts, link, out)
	return code, err
}

// pull does the work of agb pull and returns the session as it now sits here,
// ready to resume.
func pull(config Config, opts PullOptions, link remote, out io.Writer) (Session, int, error) {
	if !sessionIDPattern.MatchString(opts.ID) {
		return Session{}, 2, fmt.Errorf("%q is not a session id", opts.ID)
	}
	roots := pullRoots(config)
	found, err := link.Probe(findScript(roots, opts.ID))
	if err != nil {
		return Session{}, 1, err
	}
	session, err := findRemoteSession(roots, found, opts.ID, opts.Host)
	if err != nil {
		return Session{}, 1, err
	}
	home, toolBinary, _, err := accountFiles(session)
	if err != nil {
		return Session{}, 1, err
	}
	probe, err := link.Probe(pullProbeScript(session, home))
	if err != nil {
		return Session{}, 1, err
	}
	r := parseProbe(probe)
	session.Directory = r["cwd"]

	var blockers, warnings []string
	block := func(format string, args ...any) { blockers = append(blockers, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	if r["home"] != homeDir() {
		block("HOME there is %q, here %q: agb pull needs the same home path on both machines", r["home"], homeDir())
	}
	localDir := opts.LocalDir
	if localDir == "" {
		localDir = session.Directory
	}
	if localDir == "" {
		block("the transcript there names no directory; pass --dir")
	} else if !isDir(localDir) {
		block("%s does not exist here (clone or create it first, or pass --dir)", ShortenHome(localDir))
	}
	if _, err := lookPath(toolBinary); err != nil {
		block("%s is not installed here", ToolName(session.Source))
	}
	if pid := r["open_there"]; pid != "" {
		block("the session is open on %s (pid %s); quit it there first", opts.Host, pid)
	}
	localFile := relocatedTranscript(session, localDir)
	here := session
	here.FilePath = localFile
	if pid, _ := openSession(home, here); pid > 0 {
		if opts.Force {
			warn("a copy here is open (pid %d) (--force)", pid)
		} else {
			block("a copy here is open (pid %d); quit it first (--force to pull anyway)", pid)
		}
	}
	// Transcripts only grow, so a bigger copy here went on without the one
	// there, and replacing it would lose those turns.
	if info, err := os.Stat(localFile); err == nil {
		if size, err := strconv.ParseInt(r["size"], 10, 64); err == nil && info.Size() > size {
			message := fmt.Sprintf("the copy is bigger here (%s vs %s on %s): it went on here; push it instead", humanBytes(info.Size()), humanBytes(size), opts.Host)
			if opts.Force {
				warn("%s (--force)", message)
			} else {
				block("%s (--force to pull anyway)", message)
			}
		}
	}
	dir := ShortenHome(session.Directory)
	if r["git_dirty"] == "yes" {
		warn("%s has uncommitted changes on %s; commit and push them there so the code comes along", dir, opts.Host)
	}
	if r["git_unpushed"] == "yes" {
		warn("HEAD of %s on %s is on no remote branch; push it there so the code comes along", dir, opts.Host)
	}
	if head := r["git_head"]; head != "" && localDir != "" {
		if local, err := gitOutput(localDir, "rev-parse", "HEAD"); err == nil && local != head {
			warn("the checkout here is at %.10s, there at %.10s; pull or switch it before resuming", local, head)
		}
	}

	type fetch struct{ from, to string }
	fetches := []fetch{{session.FilePath, localFile}}
	if r["sibling"] == "yes" {
		fetches = append(fetches, fetch{strings.TrimSuffix(session.FilePath, jsonlExt) + "/", strings.TrimSuffix(localFile, jsonlExt) + "/"})
	}
	var memory *fetch
	if r["memory"] == "yes" {
		memory = &fetch{filepath.Join(filepath.Dir(session.FilePath), "memory") + "/", filepath.Join(filepath.Dir(localFile), "memory") + "/"}
	}

	label := AccountLabel(session.Source, session.Account)
	fmt.Fprintf(out, "Pull %s %s from %s\n  in %s", label, session.ID, opts.Host, dir)
	if localDir != session.Directory {
		fmt.Fprintf(out, ", here in %s", ShortenHome(localDir))
	}
	fmt.Fprintln(out)
	for _, f := range fetches {
		if f.from == f.to {
			fmt.Fprintf(out, "fetch    %s\n", ShortenHome(strings.TrimSuffix(f.from, "/")))
		} else {
			fmt.Fprintf(out, "fetch    %s -> %s\n", ShortenHome(strings.TrimSuffix(f.from, "/")), ShortenHome(strings.TrimSuffix(f.to, "/")))
		}
	}
	if memory != nil {
		fmt.Fprintf(out, "merge    %s (newer files here are kept)\n", ShortenHome(strings.TrimSuffix(memory.to, "/")))
	}
	for _, warning := range warnings {
		fmt.Fprintf(out, "Note: %s\n", warning)
	}
	for _, blocker := range blockers {
		fmt.Fprintf(out, "Stop: %s\n", blocker)
	}
	if len(blockers) > 0 {
		return Session{}, 2, fmt.Errorf("not pulled")
	}

	here.Directory = localDir
	resume := resumeCommand(here, home)
	if opts.DryRun {
		fmt.Fprintf(out, "Dry run: nothing fetched. Here, it would resume with:\n  %s\n", resume)
		return here, 0, nil
	}
	for _, f := range fetches {
		if err := link.Fetch(f.from, f.to); err != nil {
			return Session{}, 1, fmt.Errorf("fetching the session: %w", err)
		}
	}
	if memory != nil {
		if err := link.Fetch(memory.from, memory.to, "--update"); err != nil {
			return Session{}, 1, fmt.Errorf("fetching memory: %w", err)
		}
	}
	fmt.Fprintf(out, "Pulled %s %s from %s. Quit it there if it is still open; it now continues here:\n  %s\n", label, session.ID, opts.Host, resume)
	return here, 0, nil
}
