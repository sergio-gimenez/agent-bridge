package ocs

import (
	"bufio"
	"bytes"
	"crypto/sha256"
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
	"syscall"
	"time"
)

// agb move hands a session to another machine of the same user. Unlike a
// cross-tool or cross-account open, the id stays valid there: the transcript is
// copied to the same path under the same account home, so the tool resumes the
// real session rather than a transcript-seeded fork.
//
// It is a move, not a sync: one machine owns a session at a time. The local
// copy is left in place, and a later move in either direction refuses to
// overwrite a copy that grew on its own (see checkPrefix).

// activeWindow is the fallback where no /proc says which processes run: a
// transcript written this recently probably belongs to an open session.
const activeWindow = 2 * time.Minute

// openSession reports the process that has the session open on this machine,
// if any. Claude Code records each running session in <home>/sessions/<pid>.json
// with the process start time, which rules out a reused pid; a leftover record
// from a crash does not count. Any tool also counts as open while a process
// holds the transcript file open. ok is false where /proc is missing.
func openSession(home string, session Session) (pid int, ok bool) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return 0, false
	}
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
				return record.PID, true
			}
		}
	}
	fds, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fd := range fds {
		if target, err := os.Readlink(fd); err == nil && target == session.FilePath {
			pid, _ := strconv.Atoi(strings.Split(fd, "/")[2])
			return pid, true
		}
	}
	return 0, true
}

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
	// Skip the local safety checks: active session, uncommitted or unpushed work.
	Force bool
	// Make the remote setup (agb config and skill sources) equal to this one, then
	// run agb sync there. Without it, setup drift stops the move.
	SyncSetup   bool
	IgnoreDrift bool
	Launch      bool
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
	// Run runs one command there, attached to this terminal.
	Run(command string) error
}

type sshRemote struct{ host string }

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
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (r sshRemote) CopyTo(src, dst string, extra ...string) error {
	args := append([]string{"-a", "--mkpath"}, extra...)
	args = append(args, "--", src, r.host+":"+dst)
	cmd := exec.Command("rsync", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (r sshRemote) Run(command string) error {
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", r.host, command)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
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
			return nil, fmt.Errorf("%s is not under %s; agb move only copies files below home", path, home)
		}
		anchored = append(anchored, home+string(filepath.Separator)+"."+string(filepath.Separator)+rel)
	}
	return anchored, nil
}

// shellQuote makes any string one word for a POSIX shell.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// treeHashScript prints one hash for a directory's file contents and names,
// following symlinks, the same way treeHash computes it locally.
const treeHashScript = `treehash() { ( cd "$1" 2>/dev/null && find -L . -type f ! -path '*/__pycache__/*' ! -name '*.pyc' -print0 | LC_ALL=C sort -z | xargs -0r sha256sum | sha256sum | cut -c1-64 ) || echo missing; }
`

// treeHash mirrors treeHashScript: sha256 over sha256sum's listing of every
// file (bytewise path order, ./-prefixed), skipping Python byte-code caches.
func treeHash(dir string) (string, error) {
	var paths []string
	var walk func(abs, rel string) error
	walk = func(abs, rel string) error {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			childAbs, childRel := filepath.Join(abs, name), rel+"/"+name
			info, err := os.Stat(childAbs) // follows symlinks, as find -L does
			if err != nil {
				continue
			}
			if info.IsDir() {
				if name == "__pycache__" {
					continue
				}
				if err := walk(childAbs, childRel); err != nil {
					return err
				}
			} else if info.Mode().IsRegular() && !strings.HasSuffix(name, ".pyc") {
				paths = append(paths, childRel)
			}
		}
		return nil
	}
	if err := walk(dir, "."); err != nil {
		return "", err
	}
	sort.Strings(paths)
	var listing bytes.Buffer
	for _, rel := range paths {
		raw, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&listing, "%x  %s\n", sha256.Sum256(raw), rel)
	}
	return fmt.Sprintf("%x", sha256.Sum256(listing.Bytes())), nil
}

func fileHash(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// checkPrefix decides whether the copy already on the other machine may be
// replaced. Transcripts only grow, so a copy that is a prefix of ours is an
// older state of the same session; anything else continued there on its own.
func checkPrefix(local []byte, remoteSize int64, remoteSHA string) error {
	switch {
	case remoteSize < 0:
		return nil // no copy there
	case remoteSize > int64(len(local)):
		return fmt.Errorf("the copy there is longer than this one: the session continued there. Move it back from there instead")
	case fmt.Sprintf("%x", sha256.Sum256(local[:remoteSize])) != remoteSHA:
		return fmt.Errorf("the copy there has diverged from this one (not an older state of it); both continued separately")
	}
	return nil
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
	Session       Session
	Home          string // the account home: CLAUDE_CONFIG_DIR or CODEX_HOME
	RemoteDir     string // the session directory there
	RemoteFile    string // where the transcript goes there
	Transcript    []byte
	WrittenAgo    time.Duration
	OpenPID       int  // the process that has it open here, 0 if none
	CanSeeProcs   bool // false where /proc is missing: WrittenAgo decides
	GitRepo       bool
	GitDirty      bool
	GitUnpushed   bool
	GitHead       string
	Remote        map[string]string
	LocalSettings string            // hash of the account's settings.json
	LocalSetup    map[string]string // "config" and "skill:<name>" -> hash
}

type moveDecision struct {
	Blockers []string
	Warnings []string
	// The account's settings.json goes along when the other machine has none.
	CopySettings bool
	SetupDrift   []string
}

func decideMove(f moveFacts, opts MoveOptions) moveDecision {
	var d moveDecision
	r := f.Remote
	block := func(format string, args ...any) { d.Blockers = append(d.Blockers, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { d.Warnings = append(d.Warnings, fmt.Sprintf(format, args...)) }

	if home := homeDir(); r["home"] != home {
		block("HOME there is %q, here %q: agb move needs the same home path on both machines", r["home"], home)
	}
	if r["dir"] != "yes" {
		block("the session directory does not exist there: %s (clone or create it first)", ShortenHome(f.RemoteDir))
	}
	if r["tool"] != "yes" {
		block("%s is not installed there", ToolName(f.Session.Source))
	}
	if r["login"] != "yes" {
		warn("the %s account there does not look logged in (%s); log in before resuming", AccountLabel(f.Session.Source, f.Session.Account), ShortenHome(f.Home))
	}
	size := int64(-1)
	if value, ok := r["session_size"]; ok {
		size, _ = strconv.ParseInt(value, 10, 64)
	}
	if err := checkPrefix(f.Transcript, size, r["session_sha"]); err != nil {
		block("%v", err)
	}

	local := func(format string, args ...any) {
		if opts.Force {
			warn(format+" (--force)", args...)
		} else {
			block(format+" (--force to move anyway)", args...)
		}
	}
	switch {
	case f.OpenPID > 0:
		local("the session is open here (pid %d); quit it first", f.OpenPID)
	case !f.CanSeeProcs && f.WrittenAgo < activeWindow:
		local("the session was written %s ago and is probably still open here; exit it first", f.WrittenAgo.Round(time.Second))
	}
	if pid := r["open_there"]; pid != "" {
		block("the session is open there (pid %s); quit it there first, or move it back from there", pid)
	}
	if f.GitRepo {
		if f.GitDirty {
			local("%s has uncommitted changes to tracked files; commit and push them first", ShortenHome(f.Session.Directory))
		}
		if f.GitUnpushed {
			local("HEAD of %s is on no remote branch; push it first", ShortenHome(f.Session.Directory))
		}
		if head := r["git_head"]; head != "" && head != f.GitHead {
			warn("the checkout there is at %.10s, here at %.10s; pull or switch it before resuming", head, f.GitHead)
		}
	}

	switch remoteSettings := r["settings"]; {
	case f.LocalSettings == "missing":
	case remoteSettings == "missing" || remoteSettings == "":
		d.CopySettings = true
	case remoteSettings != f.LocalSettings:
		warn("%s/settings.json differs there; it is left as it is", ShortenHome(f.Home))
	}

	for _, key := range sortedKeys(f.LocalSetup) {
		if r["setup:"+key] != f.LocalSetup[key] {
			d.SetupDrift = append(d.SetupDrift, key)
		}
	}
	if len(d.SetupDrift) > 0 {
		switch {
		case opts.SyncSetup:
			if r["agb"] != "yes" {
				block("--sync-setup needs agb installed there")
			}
		case opts.IgnoreDrift:
			warn("setup differs there (%s); ignored", strings.Join(d.SetupDrift, ", "))
		default:
			block("setup differs there: %s. --sync-setup makes it equal to this machine's, --ignore-drift moves anyway", strings.Join(d.SetupDrift, ", "))
		}
	}
	return d
}

// setupSources lists the agb config and every skill source its profiles name,
// as "config" / "skill:<name>" -> local path.
func setupSources(config Config, configPath string) map[string]string {
	sources := map[string]string{}
	if _, err := os.Stat(configPath); err != nil {
		return sources
	}
	sources["config"] = configPath
	if config.Setup == nil {
		return sources
	}
	base := filepath.Dir(configPath)
	addSkills := func(skills map[string]SetupSkill) {
		for name, skill := range skills {
			if skill.Path == "" || !setupEnabled(skill.Enabled) {
				continue
			}
			if path, err := setupPath(skill.Path, base); err == nil {
				sources["skill:"+name] = path
			}
		}
	}
	for _, profile := range config.Setup.Profiles {
		addSkills(profile.Skills)
		for _, override := range profile.Overrides {
			addSkills(override.Skills)
		}
	}
	return sources
}

func localSetupHashes(sources map[string]string) map[string]string {
	hashes := map[string]string{}
	for key, path := range sources {
		if key == "config" {
			hashes[key] = fileHash(path)
		} else if hash, err := treeHash(path); err == nil {
			hashes[key] = hash
		} else {
			hashes[key] = "missing"
		}
	}
	return hashes
}

func probeScript(f moveFacts, sources map[string]string, toolBinary, loginFile string) string {
	var s strings.Builder
	s.WriteString(treeHashScript)
	s.WriteString(`kv() { printf '%s=%s\n' "$1" "$2"; }
yes_if() { if "$@" >/dev/null 2>&1; then echo yes; else echo no; fi; }
kv home "$HOME"
`)
	fmt.Fprintf(&s, "kv dir \"$(yes_if test -d %s)\"\n", shellQuote(f.RemoteDir))
	fmt.Fprintf(&s, "kv tool \"$(yes_if command -v %s)\"\n", toolBinary)
	fmt.Fprintf(&s, "kv agb \"$(yes_if command -v agb)\"\n")
	fmt.Fprintf(&s, "kv login \"$(yes_if test -s %s)\"\n", shellQuote(loginFile))
	fmt.Fprintf(&s, "if [ -f %[1]s ]; then kv session_size \"$(stat -c %%s %[1]s)\"; kv session_sha \"$(sha256sum < %[1]s | cut -c1-64)\"; fi\n", shellQuote(f.RemoteFile))
	if f.Session.Source == SourceClaude {
		fmt.Fprintf(&s, "for rec in %s/sessions/*.json; do [ -f \"$rec\" ] && grep -q %s \"$rec\" && pid=$(basename \"$rec\" .json) && kill -0 \"$pid\" 2>/dev/null && kv open_there \"$pid\"; done\n",
			shellQuote(f.Home), shellQuote(`"sessionId":"`+f.Session.ID+`"`))
	}
	// One find over every process's fd links, matched by target: no process per fd.
	fmt.Fprintf(&s, "pid=$(find /proc/[0-9]*/fd -maxdepth 1 -lname %s 2>/dev/null | head -1 | cut -d/ -f3); [ -n \"$pid\" ] && kv open_there \"$pid\"\n", shellQuote(f.RemoteFile))
	settings := filepath.Join(f.Home, "settings.json")
	fmt.Fprintf(&s, "if [ -f %[1]s ]; then kv settings \"$(sha256sum < %[1]s | cut -c1-64)\"; else kv settings missing; fi\n", shellQuote(settings))
	if f.GitRepo {
		fmt.Fprintf(&s, "kv git_head \"$(git -C %s rev-parse HEAD 2>/dev/null)\"\n", shellQuote(f.RemoteDir))
	}
	for _, key := range sortedKeys(sources) {
		path := shellQuote(sources[key])
		if key == "config" {
			fmt.Fprintf(&s, "if [ -f %[1]s ]; then kv setup:config \"$(sha256sum < %[1]s | cut -c1-64)\"; else kv setup:config missing; fi\n", path)
		} else {
			fmt.Fprintf(&s, "kv %s \"$(treehash %s)\"\n", shellQuote("setup:"+key), path)
		}
	}
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

// RunMoveCommand parses `agb move` arguments and runs the move.
func RunMoveCommand(argv []string, out io.Writer) (int, error) {
	flags := flag.NewFlagSet("agb move", flag.ContinueOnError)
	flags.SetOutput(out)
	opts := MoveOptions{}
	flags.StringVar(&opts.Host, "to", "", "ssh host to move the session to (an alias from ~/.ssh/config)")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "check and list what would be copied, change nothing")
	flags.BoolVar(&opts.Force, "force", false, "move even if the session looks open or the project has unpushed work")
	flags.BoolVar(&opts.SyncSetup, "sync-setup", false, "make the agb config and skill sources there equal to this machine's, then agb sync there")
	flags.BoolVar(&opts.IgnoreDrift, "ignore-drift", false, "move even if the setup differs there")
	flags.BoolVar(&opts.Launch, "launch", false, "resume the session there right away (ssh -t)")
	// Flags may come after the id, as people type them.
	var positional []string
	for len(argv) > 0 {
		if err := flags.Parse(argv); err != nil {
			if err == flag.ErrHelp {
				return 0, nil
			}
			return 2, err
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		argv = flags.Args()[1:]
	}
	if len(positional) != 1 {
		return 2, fmt.Errorf("usage: agb move SESSION-ID [--to HOST] [--dry-run] [--force] [--sync-setup|--ignore-drift] [--launch]\n" +
			"without --to it asks for the host and the directory there")
	}
	opts.ID = positional[0]
	opts.DryRun = opts.DryRun || dryRunEnabled()

	config := LoadConfig()
	session, err := findSession(OpenCache(CachePath(), false).AllSessions(config, ScopeUser), opts.ID)
	if err != nil {
		return 1, err
	}
	if opts.Host == "" {
		return RunMoveDialog(session, config, os.Stdin, out)
	}
	return moveSession(session, config, ConfigPath(), opts, sshRemote{opts.Host}, out)
}

func moveSession(session Session, config Config, configPath string, opts MoveOptions, link remote, out io.Writer) (int, error) {
	var home, toolBinary, loginFile string
	switch session.Source {
	case SourceClaude:
		home = filepath.Dir(claudeProjectsPath(session.Account))
		toolBinary, loginFile = "claude", filepath.Join(home, ".credentials.json")
	case SourceCodex:
		home = codexHome(session.Account)
		toolBinary, loginFile = "codex", filepath.Join(home, "auth.json")
	default:
		return 1, fmt.Errorf("%s sessions live in a database, not a file; agb move handles Claude Code and Codex", ToolName(session.Source))
	}
	if session.FilePath == "" {
		return 1, fmt.Errorf("no transcript file recorded for %s", session.ID)
	}

	transcript, err := os.ReadFile(session.FilePath)
	if err != nil {
		return 1, err
	}
	info, err := os.Stat(session.FilePath)
	if err != nil {
		return 1, err
	}
	remoteDir := opts.RemoteDir
	if remoteDir == "" {
		remoteDir = session.Directory
	}
	// Only a directory chosen for the other side relocates the transcript; a
	// project folder whose name does not follow claudeProjectKey stays as it is.
	remoteFile := session.FilePath
	if session.Source == SourceClaude && remoteDir != session.Directory {
		remoteFile = filepath.Join(filepath.Dir(filepath.Dir(session.FilePath)), claudeProjectKey(remoteDir), filepath.Base(session.FilePath))
	}
	relocated := remoteFile != session.FilePath
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
	facts.LocalSettings = fileHash(filepath.Join(home, "settings.json"))
	sources := setupSources(config, configPath)
	facts.LocalSetup = localSetupHashes(sources)

	probe, err := link.Probe(probeScript(facts, sources, toolBinary, loginFile))
	if err != nil {
		return 1, err
	}
	facts.Remote = parseProbe(probe)
	decision := decideMove(facts, opts)

	// What goes along.
	var transcriptRoots []string
	for _, account := range config.ClaudeAccounts {
		account := account
		transcriptRoots = append(transcriptRoots, claudeProjectsPath(&account))
	}
	transcriptRoots = append(transcriptRoots, claudeProjectsPath(nil))
	// What changes project folder there goes by CopyTo; the rest keeps its path.
	type relocation struct{ from, to string }
	var moved []relocation
	var files []string
	sibling := strings.TrimSuffix(session.FilePath, jsonlExt)
	if relocated {
		moved = append(moved, relocation{session.FilePath, remoteFile})
		if isDir(sibling) {
			moved = append(moved, relocation{sibling + "/", strings.TrimSuffix(remoteFile, jsonlExt) + "/"})
		}
	} else {
		files = append(files, session.FilePath)
		if session.Source == SourceClaude && isDir(sibling) {
			files = append(files, sibling)
		}
	}
	files = append(files, referencedFiles(transcript, session.FilePath, filepath.Join(filepath.Dir(CachePath()), "handoffs"), transcriptRoots)...)
	if decision.CopySettings {
		files = append(files, filepath.Join(home, "settings.json"))
	}
	files = withoutNested(files)
	var memory string
	if session.Source == SourceClaude {
		if dir := filepath.Join(filepath.Dir(session.FilePath), "memory"); isDir(dir) {
			memory = dir
		}
	}

	memoryThere := memory
	if relocated && memory != "" {
		memoryThere = filepath.Join(filepath.Dir(remoteFile), "memory")
	}
	fmt.Fprintf(out, "Move %s %s to %s\n  %s\n  in %s", AccountLabel(session.Source, session.Account), session.ID, opts.Host, session.Title, ShortenHome(session.Directory))
	if remoteDir != session.Directory {
		fmt.Fprintf(out, ", there in %s", ShortenHome(remoteDir))
	}
	fmt.Fprintln(out)
	for _, m := range moved {
		fmt.Fprintf(out, "copy     %s -> %s\n", ShortenHome(strings.TrimSuffix(m.from, "/")), ShortenHome(strings.TrimSuffix(m.to, "/")))
	}
	for _, path := range files {
		fmt.Fprintf(out, "copy     %s\n", ShortenHome(path))
	}
	if memory != "" {
		fmt.Fprintf(out, "merge    %s (newer files there are kept)\n", ShortenHome(memoryThere))
	}
	if opts.SyncSetup {
		for _, key := range decision.SetupDrift {
			fmt.Fprintf(out, "setup    %s -> %s\n", key, ShortenHome(sources[key]))
		}
	}
	for _, warning := range decision.Warnings {
		fmt.Fprintf(out, "Note: %s\n", warning)
	}
	for _, blocker := range decision.Blockers {
		fmt.Fprintf(out, "Stop: %s\n", blocker)
	}
	if len(decision.Blockers) > 0 {
		return 2, fmt.Errorf("not moved")
	}

	thereSession := session
	thereSession.Directory = remoteDir
	resume := resumeCommand(thereSession, home)
	if opts.DryRun {
		fmt.Fprintf(out, "Dry run: nothing copied. There, it would resume with:\n  %s\n", resume)
		return 0, nil
	}

	if opts.SyncSetup && len(decision.SetupDrift) > 0 {
		for _, key := range decision.SetupDrift {
			path := sources[key]
			if key == "config" {
				err = link.Copy([]string{path})
			} else {
				// A skill directory is replaced as a whole, so files removed here go there too.
				err = link.Copy([]string{path + "/"}, "--delete")
			}
			if err != nil {
				return 1, fmt.Errorf("copying %s: %w", key, err)
			}
		}
		if err := link.Run("agb sync && agb sync --check >/dev/null"); err != nil {
			return 1, fmt.Errorf("agb sync there: %w", err)
		}
	}
	for _, m := range moved {
		if err := link.CopyTo(m.from, m.to); err != nil {
			return 1, fmt.Errorf("copying the session: %w", err)
		}
	}
	if err := link.Copy(files); err != nil {
		return 1, fmt.Errorf("copying the session: %w", err)
	}
	if memory != "" {
		var err error
		if relocated {
			err = link.CopyTo(memory+"/", memoryThere+"/", "--update")
		} else {
			err = link.Copy([]string{memory}, "--update")
		}
		if err != nil {
			return 1, fmt.Errorf("copying memory: %w", err)
		}
	}
	fmt.Fprintf(out, "Moved. Exit it here if it is still open; it now continues there:\n  ssh -t %s %s\n", opts.Host, shellQuote(resume))
	if opts.Launch {
		ssh, err := exec.LookPath("ssh")
		if err != nil {
			return 1, err
		}
		return 1, syscall.Exec(ssh, []string{"ssh", "-t", opts.Host, resume}, os.Environ())
	}
	return 0, nil
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
