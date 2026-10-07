package ocs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// agb push and agb pull hand a session between two machines of the same user.
// Unlike a cross-tool or cross-account open, the id stays valid there: the
// transcript is copied to the same path under the same account home, so the
// tool resumes the real session rather than a transcript-seeded fork.
//
// Neither is a sync: the copy simply replaces what is on the other side. The
// one guard is that the session is open on neither machine, since carrying on
// in both places forks one id into two conversations. Code is git's to carry;
// agb only notes a checkout that has not been committed and pushed.

// activeWindow is the fallback where no /proc says which processes run: a
// transcript written this recently probably belongs to an open session.
const activeWindow = 2 * time.Minute

// openSession reports the process that has the session open on this machine,
// if any. Claude Code records each running session in <home>/sessions/<pid>.json
// with the process start time, which rules out a reused pid; a leftover record
// from a crash does not count. Any tool also counts as open while a process
// holds the transcript file open, or was started to resume it (`claude --resume
// <id>`, `codex resume <id>`), which covers one that has not got that far yet.
// ok is false where /proc is missing.
func openSession(home string, session Session) (pid int, ok bool) {
	procs, ok := snapshotProcs()
	if !ok {
		return 0, false
	}
	return procs.open(home, session), true
}

// procs is one pass over /proc: which process holds each open file, and which
// was started to resume each session id. Listing many sessions asks it many
// times, so it is read once.
type procs struct {
	byFile   map[string]int
	byResume map[string]int
}

func snapshotProcs() (procs, bool) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return procs{}, false
	}
	p := procs{byFile: map[string]int{}, byResume: map[string]int{}}
	fds, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fd := range fds {
		if target, err := os.Readlink(fd); err == nil && strings.HasSuffix(target, jsonlExt) {
			pid, _ := strconv.Atoi(strings.Split(fd, "/")[2])
			p.byFile[target] = pid
		}
	}
	cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, path := range cmdlines {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		for i := 1; i < len(args); i++ {
			if resumeFlags[args[i-1]] {
				pid, _ := strconv.Atoi(strings.Split(path, "/")[2])
				p.byResume[args[i]] = pid
			}
		}
	}
	return p, true
}

func (p procs) open(home string, session Session) int {
	if session.Source == SourceClaude {
		records, _ := filepath.Glob(filepath.Join(home, "sessions", "*.json"))
		for _, path := range records {
			var record struct {
				PID       int    `json:"pid"`
				SessionID string `json:"sessionId"`
				ProcStart string `json:"procStart"`
			}
			raw, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(raw, &record) != nil || record.SessionID != session.ID {
				continue
			}
			if start, alive := procStart(record.PID); alive && (record.ProcStart == "" || record.ProcStart == start) {
				return record.PID
			}
		}
	}
	if pid := p.byFile[session.FilePath]; pid > 0 {
		return pid
	}
	return p.byResume[session.ID]
}

// resumeFlags are the arguments that put a session id next on a tool's command
// line: claude --resume/-r <id>, codex resume <id>.
var resumeFlags = map[string]bool{"--resume": true, "-r": true, "resume": true}

// procStart returns field 22 of /proc/<pid>/stat, the process start time.
func procStart(pid int) (string, bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}
	// The command name (field 2) may hold spaces; it ends at the last ')'.
	fields := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
	if len(fields) < 20 {
		return "", true
	}
	return fields[19], true
}

type MoveOptions struct {
	ID     string
	Host   string
	DryRun bool
	// Push even if the session looks open here.
	Force bool
	// Skip the host's arrive hook.
	NoArrive bool
	// The session's directory on the other machine. Empty means the same path.
	// For Claude a different path also changes the project folder the
	// transcript goes to, since Claude keys sessions by directory.
	RemoteDir string
}

// remote is the other machine; tests replace it.
type remote interface {
	// Probe runs a shell script there and returns its stdout.
	Probe(script string) (string, error)
	// Copy puts each local absolute path at the same absolute path there.
	Copy(paths []string, extra ...string) error
	// CopyTo puts one local path at another path there, creating its parents.
	CopyTo(src, dst string, extra ...string) error
	// Fetch puts one path there at a local path, creating its parents.
	Fetch(src, dst string, extra ...string) error
	// Run runs one command there, attached to this terminal.
	Run(command string) error
}

type sshRemote struct {
	host string
	out  io.Writer // where rsync and hooks print; nil means stdout
}

func (r sshRemote) writer() io.Writer {
	if r.out == nil {
		return os.Stdout
	}
	return r.out
}

func (r sshRemote) Probe(script string) (string, error) {
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", r.host, "bash -s")
	cmd.Stdin = strings.NewReader(script)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("ssh %s: %v: %s", r.host, err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

func (r sshRemote) Copy(paths []string, extra ...string) error {
	if len(paths) == 0 {
		return nil
	}
	home := homeDir()
	anchored, err := anchorAtHome(home, paths)
	if err != nil {
		return err
	}
	args := append([]string{"-a", "--relative"}, extra...)
	args = append(args, "--")
	args = append(args, anchored...)
	args = append(args, r.host+":"+home+"/")
	cmd := exec.Command("rsync", args...)
	cmd.Stdout, cmd.Stderr = r.writer(), r.writer()
	return cmd.Run()
}

func (r sshRemote) CopyTo(src, dst string, extra ...string) error {
	args := append([]string{"-a", "--mkpath"}, extra...)
	args = append(args, "--", src, r.host+":"+dst)
	cmd := exec.Command("rsync", args...)
	cmd.Stdout, cmd.Stderr = r.writer(), r.writer()
	return cmd.Run()
}

func (r sshRemote) Fetch(src, dst string, extra ...string) error {
	args := append([]string{"-a", "--mkpath"}, extra...)
	args = append(args, "--", r.host+":"+src, dst)
	cmd := exec.Command("rsync", args...)
	cmd.Stdout, cmd.Stderr = r.writer(), r.writer()
	return cmd.Run()
}

func (r sshRemote) Run(command string) error {
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", r.host, command)
	cmd.Stdout, cmd.Stderr = r.writer(), r.writer()
	return cmd.Run()
}

// anchorAtHome marks where --relative starts recreating directories: at home,
// with rsync's /./ marker. From / it would recreate /home itself there, and
// setting its times fails for a normal user (rsync exit 23). Both machines have
// the same home (decideMove checks), so everything moved lives below it.
func anchorAtHome(home string, paths []string) ([]string, error) {
	var anchored []string
	for _, path := range paths {
		rel, ok := strings.CutPrefix(path, home+string(filepath.Separator))
		if home == "" || !ok || rel == "" {
			return nil, fmt.Errorf("%s is not under %s; agb push only copies files below home", path, home)
		}
		anchored = append(anchored, home+string(filepath.Separator)+"."+string(filepath.Separator)+rel)
	}
	return anchored, nil
}

// shellQuote makes any string one word for a POSIX shell.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// referencedFiles finds files the transcript names by absolute path that a
// resumed session would want to read again: agb handoffs (handoffRoot) and other
// Claude transcripts with their sibling directories (transcriptRoots). Anything
// else it mentions is the project's business, not the session's; memory is
// merged separately, never overwritten.
func referencedFiles(transcript []byte, self, handoffRoot string, transcriptRoots []string) []string {
	home := homeDir()
	if home == "" {
		return nil
	}
	pattern := regexp.MustCompile(regexp.QuoteMeta(home) + `/[A-Za-z0-9._/+@-]+`)
	seen := map[string]bool{self: true, strings.TrimSuffix(self, jsonlExt): true}
	var found []string
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			found = append(found, path)
		}
	}
	for _, match := range pattern.FindAll(transcript, -1) {
		path := filepath.Clean(strings.TrimRight(string(match), "."))
		wanted := strings.HasPrefix(path, handoffRoot+string(filepath.Separator))
		for _, root := range transcriptRoots {
			if strings.HasPrefix(path, root+string(filepath.Separator)) && strings.HasSuffix(path, jsonlExt) &&
				!strings.Contains(path, string(filepath.Separator)+"memory"+string(filepath.Separator)) {
				wanted = true
			}
		}
		if !wanted {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		add(path)
		if strings.HasSuffix(path, jsonlExt) {
			if dir := strings.TrimSuffix(path, jsonlExt); isDir(dir) {
				add(dir)
			}
		}
		if len(found) >= 200 {
			break
		}
	}
	return withoutNested(found)
}

// withoutNested drops paths inside another listed directory: copying the
// directory already carries them.
func withoutNested(paths []string) []string {
	sort.Strings(paths)
	var kept []string
	for _, path := range paths {
		nested := false
		for _, parent := range kept {
			if strings.HasPrefix(path, parent+string(filepath.Separator)) {
				nested = true
				break
			}
		}
		if !nested {
			kept = append(kept, path)
		}
	}
	return kept
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// claudeProjectKey is the folder name Claude Code files a directory's
// sessions under: every character that is not an ASCII letter or digit
// becomes '-' (/home/u/i2cat/GÉANT -> -home-u-i2cat-G-ANT).
func claudeProjectKey(dir string) string {
	var key strings.Builder
	for _, r := range dir {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			key.WriteRune(r)
		} else {
			key.WriteByte('-')
		}
	}
	return key.String()
}

// moveFacts is everything the decision needs, gathered first so the decision
// itself is a pure function.
type moveFacts struct {
	Session     Session
	Home        string // the account home: CLAUDE_CONFIG_DIR or CODEX_HOME
	RemoteDir   string // the session directory there
	RemoteFile  string // where the transcript goes there
	Transcript  []byte
	WrittenAgo  time.Duration
	OpenPID     int  // the process that has it open here, 0 if none
	CanSeeProcs bool // false where /proc is missing: WrittenAgo decides
	GitRepo     bool
	GitDirty    bool
	GitUnpushed bool
	GitHead     string
	Remote      map[string]string
}

type moveDecision struct {
	Blockers []string
	Warnings []string
	// What checked out, for the picker's push panel.
	Passed []string
}

func decideMove(f moveFacts, opts MoveOptions) moveDecision {
	var d moveDecision
	r := f.Remote
	host := opts.Host
	block := func(format string, args ...any) { d.Blockers = append(d.Blockers, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { d.Warnings = append(d.Warnings, fmt.Sprintf(format, args...)) }
	pass := func(format string, args ...any) { d.Passed = append(d.Passed, fmt.Sprintf(format, args...)) }
	local := func(format string, args ...any) {
		if opts.Force {
			warn(format+" (--force)", args...)
		} else {
			block(format+" (--force to push anyway)", args...)
		}
	}
	tool, account := ToolName(f.Session.Source), AccountLabel(f.Session.Source, f.Session.Account)

	if home := homeDir(); r["home"] != home {
		block("HOME on %s is %q, here %q: agb push needs the same home path on both machines", host, r["home"], home)
	}
	if r["dir"] != "yes" {
		block("%s does not exist on %s (clone or create it first)", ShortenHome(f.RemoteDir), host)
	} else {
		pass("%s exists on %s", ShortenHome(f.RemoteDir), host)
	}
	switch {
	case r["tool"] != "yes":
		block("%s is not installed on %s", tool, host)
	case r["login"] != "yes":
		warn("the %s account on %s does not look logged in (%s); log in before resuming", account, host, ShortenHome(f.Home))
	default:
		pass("%s on %s, %s logged in", tool, host, account)
	}

	open := false
	switch {
	case f.OpenPID > 0:
		open = true
		local("the session is open here (pid %d); quit it first", f.OpenPID)
	case !f.CanSeeProcs && f.WrittenAgo < activeWindow:
		open = true
		local("the session was written %s ago and is probably still open here; exit it first", f.WrittenAgo.Round(time.Second))
	}
	if pid := r["open_there"]; pid != "" {
		open = true
		block("the session is open on %s (pid %s); quit it there first, or pull it from there", host, pid)
	}
	if !open {
		pass("not open here or on %s", host)
	}

	// Transcripts only grow, so a bigger copy there went on without this one,
	// and replacing it would lose those turns.
	switch size, err := strconv.ParseInt(r["session_size"], 10, 64); {
	case err != nil:
		pass("no copy on %s yet", host)
	case size > int64(len(f.Transcript)):
		local("the copy is bigger on %s (%s vs %s): it went on there; pull it instead", host, humanBytes(size), humanBytes(int64(len(f.Transcript))))
	default:
		pass("the copy on %s is not newer (%s vs %s)", host, humanBytes(size), humanBytes(int64(len(f.Transcript))))
	}

	if f.GitRepo {
		clean := true
		if f.GitDirty {
			clean = false
			warn("%s has uncommitted changes to tracked files; commit and push them so the code goes along", ShortenHome(f.Session.Directory))
		}
		if f.GitUnpushed {
			clean = false
			warn("HEAD of %s is on no remote branch; push it so the code goes along", ShortenHome(f.Session.Directory))
		}
		if head := r["git_head"]; head != "" && head != f.GitHead {
			clean = false
			warn("the checkout on %s is at %.10s, here at %.10s; pull or switch it before resuming", host, head, f.GitHead)
		}
		if clean {
			pass("%s is committed and pushed", ShortenHome(f.Session.Directory))
		}
	}
	return d
}

// humanBytes is a size as people read it: 99 B, 4.1 KB, 8.2 MB.
func humanBytes(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f KB", float64(n)/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
}

func probeScript(f moveFacts, toolBinary, loginFile string) string {
	var s strings.Builder
	s.WriteString(`kv() { printf '%s=%s\n' "$1" "$2"; }
yes_if() { if "$@" >/dev/null 2>&1; then echo yes; else echo no; fi; }
kv home "$HOME"
`)
	fmt.Fprintf(&s, "kv dir \"$(yes_if test -d %s)\"\n", shellQuote(f.RemoteDir))
	fmt.Fprintf(&s, "kv tool \"$(yes_if command -v %s)\"\n", toolBinary)
	fmt.Fprintf(&s, "kv login \"$(yes_if test -s %s)\"\n", shellQuote(loginFile))
	s.WriteString(openThereScript(f.Session, f.Home, f.RemoteFile))
	fmt.Fprintf(&s, "if [ -f %[1]s ]; then kv session_size \"$(stat -c %%s %[1]s)\"; fi\n", shellQuote(f.RemoteFile))
	if f.GitRepo {
		fmt.Fprintf(&s, "kv git_head \"$(git -C %s rev-parse HEAD 2>/dev/null)\"\n", shellQuote(f.RemoteDir))
	}
	return s.String()
}

// openThereScript prints open_there=<pid> when a process there has the session
// open: Claude Code's record of a running session, or any process holding the
// transcript. It needs the kv helper.
func openThereScript(session Session, home, file string) string {
	var s strings.Builder
	if session.Source == SourceClaude {
		fmt.Fprintf(&s, "for rec in %s/sessions/*.json; do [ -f \"$rec\" ] && grep -q %s \"$rec\" && pid=$(basename \"$rec\" .json) && kill -0 \"$pid\" 2>/dev/null && kv open_there \"$pid\"; done\n",
			shellQuote(home), shellQuote(`"sessionId":"`+session.ID+`"`))
	}
	// One find over every process's fd links, matched by target: no process per fd.
	fmt.Fprintf(&s, "pid=$(find /proc/[0-9]*/fd -maxdepth 1 -lname %s 2>/dev/null | head -1 | cut -d/ -f3); if [ -n \"$pid\" ]; then kv open_there \"$pid\"; fi\n", shellQuote(file))
	// A process started to resume it (see resumeFlags); one grep over every
	// command line, whose arguments are NUL-separated.
	pattern := `(^|\x00)(--resume|-r|resume)\x00` + regexp.QuoteMeta(session.ID) + `(\x00|$)`
	fmt.Fprintf(&s, "pid=$(grep -lsaP -- %s /proc/[0-9]*/cmdline 2>/dev/null | head -1 | cut -d/ -f3); if [ -n \"$pid\" ]; then kv open_there \"$pid\"; fi\n", shellQuote(pattern))
	return s.String()
}

func parseProbe(out string) map[string]string {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if key, value, ok := strings.Cut(scanner.Text(), "="); ok {
			values[key] = value
		}
	}
	return values
}

func gitOutput(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

func findSession(sessions []Session, prefix string) (Session, error) {
	var matches []Session
	for _, session := range sessions {
		if strings.HasPrefix(session.ID, prefix) {
			matches = append(matches, session)
		}
	}
	switch len(matches) {
	case 0:
		return Session{}, fmt.Errorf("no session with id %q (agb --print lists them)", prefix)
	case 1:
		return matches[0], nil
	}
	return Session{}, fmt.Errorf("id %q matches %d sessions; give more of it", prefix, len(matches))
}

// parseInterleaved parses flags that may come before, between or after the
// positional arguments, as people type them.
func parseInterleaved(flags *flag.FlagSet, argv []string) ([]string, error) {
	var positional []string
	for len(argv) > 0 {
		if err := flags.Parse(argv); err != nil {
			return nil, err
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		argv = flags.Args()[1:]
	}
	return positional, nil
}

// RunPushCommand parses `agb push [HOST] SESSION-ID` and pushes the session.
func RunPushCommand(argv []string, out io.Writer) (int, error) {
	flags := flag.NewFlagSet("agb push", flag.ContinueOnError)
	flags.SetOutput(out)
	opts := MoveOptions{}
	flags.StringVar(&opts.RemoteDir, "dir", "", "the session's directory there (default: the same path)")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "check and list what would be copied, change nothing")
	flags.BoolVar(&opts.Force, "force", false, "push even if the session looks open here")
	flags.BoolVar(&opts.NoArrive, "no-arrive", false, "do not run the host's arrive hook")
	positional, err := parseInterleaved(flags, argv)
	if err == flag.ErrHelp {
		return 0, nil
	} else if err != nil {
		return 2, err
	}
	switch len(positional) {
	case 1:
		opts.ID = positional[0]
	case 2:
		opts.Host, opts.ID = positional[0], positional[1]
	default:
		return 2, fmt.Errorf("usage: agb push [HOST] SESSION-ID [--dir DIR] [--dry-run] [--force] [--no-arrive]\n" +
			"without HOST it asks for the host and the directory there")
	}
	opts.DryRun = opts.DryRun || dryRunEnabled()
	if opts.RemoteDir != "" {
		opts.RemoteDir = expandHome(opts.RemoteDir)
	}

	config := LoadConfig()
	session, err := findSession(OpenCache(CachePath(), false).AllSessions(config, ScopeUser), opts.ID)
	if err != nil {
		return 1, err
	}
	if opts.Host == "" {
		return RunMoveDialog(session, config, os.Stdin, out)
	}
	return moveSession(session, config, opts, dialRemote(opts.Host, nil), out)
}

// accountFiles is where a session's account keeps its state, the tool that
// resumes it, and the file a logged-in account has.
func accountFiles(session Session) (home, toolBinary, loginFile string, err error) {
	switch session.Source {
	case SourceClaude:
		home = filepath.Dir(claudeProjectsPath(session.Account))
		return home, "claude", filepath.Join(home, ".credentials.json"), nil
	case SourceCodex:
		home = codexHome(session.Account)
		return home, "codex", filepath.Join(home, "auth.json"), nil
	}
	return "", "", "", fmt.Errorf("%s sessions live in a database, not a file; agb push and pull handle Claude Code and Codex", ToolName(session.Source))
}

// relocatedTranscript is where a transcript goes for another directory: for
// Claude, which files sessions by directory, that directory's project folder.
// Only a different directory relocates it, so a project folder whose name does
// not follow claudeProjectKey stays as it is.
func relocatedTranscript(session Session, dir string) string {
	if session.Source != SourceClaude || dir == session.Directory {
		return session.FilePath
	}
	return filepath.Join(filepath.Dir(filepath.Dir(session.FilePath)), claudeProjectKey(dir), filepath.Base(session.FilePath))
}

// pushPlan is everything a push will do, worked out before anything is
// copied: the checks, what goes along, and how the session resumes there.
type pushPlan struct {
	Session   Session
	Host      string
	RemoteDir string
	Decision  moveDecision
	// The process that has the session open here, 0 if none.
	LocalPID int
	// Paths that change project folder there go by CopyTo; Files keep their
	// path. Referenced files and memory keep a newer copy there.
	Moves       []relocation
	Files       []string
	Referenced  []string
	Memory      string
	MemoryThere string
	Relocated   bool
	// The session as it will sit there, the command that resumes it, and the
	// host's arrive hook ("" for none).
	There  Session
	Resume string
	Hook   string
}

type relocation struct{ from, to string }

func moveSession(session Session, config Config, opts MoveOptions, link remote, out io.Writer) (int, error) {
	plan, err := planPush(session, config, opts, link)
	if err != nil {
		return 1, err
	}
	plan.print(out)
	if len(plan.Decision.Blockers) > 0 {
		return 2, fmt.Errorf("not pushed")
	}
	if opts.DryRun {
		fmt.Fprintf(out, "Dry run: nothing copied. There, it would resume with:\n  %s\n", plan.Resume)
		if plan.Hook != "" && !opts.NoArrive {
			fmt.Fprintf(out, "and the arrive hook would run:\n  %s\n", arriveCommand(plan.Hook, plan.There, plan.Resume))
		}
		return 0, nil
	}
	if err := plan.copy(link); err != nil {
		return 1, err
	}
	if plan.Hook == "" || opts.NoArrive {
		fmt.Fprintf(out, "Pushed. Exit it here if it is still open; it now continues there:\n  ssh -t %s %s\n", opts.Host, shellQuote(plan.Resume))
		return 0, nil
	}
	fmt.Fprintf(out, "Pushed. Exit it here if it is still open. Running the arrive hook on %s.\n", opts.Host)
	if err := plan.arrive(link); err != nil {
		return 1, err
	}
	return 0, nil
}

func planPush(session Session, config Config, opts MoveOptions, link remote) (pushPlan, error) {
	home, toolBinary, loginFile, err := accountFiles(session)
	if err != nil {
		return pushPlan{}, err
	}
	if session.FilePath == "" {
		return pushPlan{}, fmt.Errorf("no transcript file recorded for %s", session.ID)
	}
	transcript, err := os.ReadFile(session.FilePath)
	if err != nil {
		return pushPlan{}, err
	}
	info, err := os.Stat(session.FilePath)
	if err != nil {
		return pushPlan{}, err
	}
	remoteDir := opts.RemoteDir
	if remoteDir == "" {
		remoteDir = session.Directory
	}
	remoteFile := relocatedTranscript(session, remoteDir)
	facts := moveFacts{Session: session, Home: home, Transcript: transcript, WrittenAgo: time.Since(info.ModTime()),
		RemoteDir: remoteDir, RemoteFile: remoteFile}
	facts.OpenPID, facts.CanSeeProcs = openSession(home, session)
	if inside, err := gitOutput(session.Directory, "rev-parse", "--is-inside-work-tree"); err == nil && inside == "true" {
		facts.GitRepo = true
		dirty, _ := gitOutput(session.Directory, "status", "--porcelain", "--untracked-files=no")
		facts.GitDirty = dirty != ""
		branches, _ := gitOutput(session.Directory, "branch", "-r", "--contains", "HEAD")
		facts.GitUnpushed = branches == ""
		facts.GitHead, _ = gitOutput(session.Directory, "rev-parse", "HEAD")
	}
	probe, err := link.Probe(probeScript(facts, toolBinary, loginFile))
	if err != nil {
		return pushPlan{}, err
	}
	facts.Remote = parseProbe(probe)

	plan := pushPlan{Session: session, Host: opts.Host, RemoteDir: remoteDir, Decision: decideMove(facts, opts),
		LocalPID: facts.OpenPID, Relocated: remoteFile != session.FilePath}
	if !opts.NoArrive {
		plan.Hook = config.Arrive[opts.Host]
	}

	// What goes along.
	var transcriptRoots []string
	for _, account := range config.ClaudeAccounts {
		account := account
		transcriptRoots = append(transcriptRoots, claudeProjectsPath(&account))
	}
	transcriptRoots = append(transcriptRoots, claudeProjectsPath(nil))
	sibling := strings.TrimSuffix(session.FilePath, jsonlExt)
	if plan.Relocated {
		plan.Moves = append(plan.Moves, relocation{session.FilePath, remoteFile})
		if isDir(sibling) {
			plan.Moves = append(plan.Moves, relocation{sibling + "/", strings.TrimSuffix(remoteFile, jsonlExt) + "/"})
		}
	} else {
		plan.Files = append(plan.Files, session.FilePath)
		if session.Source == SourceClaude && isDir(sibling) {
			plan.Files = append(plan.Files, sibling)
		}
	}
	plan.Files = withoutNested(plan.Files)
	plan.Referenced = referencedFiles(transcript, session.FilePath, filepath.Join(filepath.Dir(CachePath()), "handoffs"), transcriptRoots)
	if session.Source == SourceClaude {
		if dir := filepath.Join(filepath.Dir(session.FilePath), "memory"); isDir(dir) {
			plan.Memory, plan.MemoryThere = dir, dir
			if plan.Relocated {
				plan.MemoryThere = filepath.Join(filepath.Dir(remoteFile), "memory")
			}
		}
	}
	plan.There = session
	plan.There.Directory = remoteDir
	plan.Resume = resumeCommand(plan.There, home)
	return plan, nil
}

func (plan pushPlan) print(out io.Writer) {
	session := plan.Session
	fmt.Fprintf(out, "Push %s %s to %s\n  %s\n  in %s", AccountLabel(session.Source, session.Account), session.ID, plan.Host, session.Title, ShortenHome(session.Directory))
	if plan.RemoteDir != session.Directory {
		fmt.Fprintf(out, ", there in %s", ShortenHome(plan.RemoteDir))
	}
	fmt.Fprintln(out)
	for _, m := range plan.Moves {
		fmt.Fprintf(out, "copy     %s -> %s\n", ShortenHome(strings.TrimSuffix(m.from, "/")), ShortenHome(strings.TrimSuffix(m.to, "/")))
	}
	for _, path := range plan.Files {
		fmt.Fprintf(out, "copy     %s\n", ShortenHome(path))
	}
	for _, path := range plan.Referenced {
		fmt.Fprintf(out, "copy     %s (unless newer there)\n", ShortenHome(path))
	}
	if plan.Memory != "" {
		fmt.Fprintf(out, "merge    %s (newer files there are kept)\n", ShortenHome(plan.MemoryThere))
	}
	for _, warning := range plan.Decision.Warnings {
		fmt.Fprintf(out, "Note: %s\n", warning)
	}
	for _, blocker := range plan.Decision.Blockers {
		fmt.Fprintf(out, "Stop: %s\n", blocker)
	}
}

// copy does the push: the session's own files replace what is there; what it
// only refers to, and memory, keep a newer copy there, since another session
// may be carrying on with them on that machine.
func (plan pushPlan) copy(link remote) error {
	for _, m := range plan.Moves {
		if err := link.CopyTo(m.from, m.to); err != nil {
			return fmt.Errorf("copying the session: %w", err)
		}
	}
	if err := link.Copy(plan.Files); err != nil {
		return fmt.Errorf("copying the session: %w", err)
	}
	if len(plan.Referenced) > 0 {
		if err := link.Copy(plan.Referenced, "--update"); err != nil {
			return fmt.Errorf("copying what the session refers to: %w", err)
		}
	}
	if plan.Memory != "" {
		var err error
		if plan.Relocated {
			err = link.CopyTo(plan.Memory+"/", plan.MemoryThere+"/", "--update")
		} else {
			err = link.Copy([]string{plan.Memory}, "--update")
		}
		if err != nil {
			return fmt.Errorf("copying memory: %w", err)
		}
	}
	return nil
}

// arrive runs the host's arrive hook there.
func (plan pushPlan) arrive(link remote) error {
	if err := link.Run(arriveCommand(plan.Hook, plan.There, plan.Resume)); err != nil {
		return fmt.Errorf("the session is there, but the arrive hook failed: %w; resume it with:\n  ssh -t %s %s", err, plan.Host, shellQuote(plan.Resume))
	}
	return nil
}

// arriveCommand fills a host's arrive hook. {resume}, {dir}, {title} and {id}
// become one shell word each, so a hook uses them unquoted:
//
//	p=$(herdr tab create --label {title} --cwd {dir} | jq -r .result.root_pane.pane_id) && herdr pane run "$p" {resume}
func arriveCommand(template string, session Session, resume string) string {
	return strings.NewReplacer(
		"{resume}", shellQuote(resume),
		"{dir}", shellQuote(session.Directory),
		"{title}", shellQuote(session.Title),
		"{id}", shellQuote(session.ID),
	).Replace(template)
}

// resumeCommand names the account home only for an isolated account: on the
// tool's default home the variable must stay unset, as agb's own open does.
func resumeCommand(session Session, home string) string {
	env, command := "CLAUDE_CONFIG_DIR", "claude --resume "+session.ID
	if session.Source == SourceCodex {
		env, command = "CODEX_HOME", "codex resume "+session.ID
	}
	if session.Account == nil || session.Account.Home == "" {
		return fmt.Sprintf("cd %s && %s", shellQuote(session.Directory), command)
	}
	return fmt.Sprintf("cd %s && %s=%s %s", shellQuote(session.Directory), env, shellQuote(home), command)
}
