package ocs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const pilotHeadroomVersion = "0.40.0"

type pilotState struct {
	Version     int               `json:"version"`
	Target      string            `json:"target"`
	Model       string            `json:"model"`
	Home        string            `json:"accountHome"`
	Assignments map[string]string `json:"assignments"`
	Pending     []string          `json:"pending"`
}

type pilotRun struct {
	ID                string         `json:"id"`
	Task              string         `json:"task"`
	Group             string         `json:"group"`
	Target            string         `json:"target"`
	Model             string         `json:"model"`
	HeadroomVersion   string         `json:"headroomVersion"`
	Started           time.Time      `json:"started"`
	Finished          *time.Time     `json:"finished,omitempty"`
	ExitCode          *int           `json:"exitCode,omitempty"`
	Status            string         `json:"status"`
	Outcome           string         `json:"outcome,omitempty"`
	ContextCorrection bool           `json:"contextCorrection"`
	Before            *AccountUsage  `json:"limitsBefore,omitempty"`
	After             *AccountUsage  `json:"limitsAfter,omitempty"`
	Stats             map[string]any `json:"headroomStats,omitempty"`
	StatsBefore       map[string]any `json:"headroomStatsBefore,omitempty"`
	Requests          []pilotRequest `json:"requests,omitempty"`
}

func pilotHash(task string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(task))) }

func pilotWrite(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pilot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// The lock covers the entire launch: exclusive proxies and task outcomes never
// share counters, and simultaneous invocations can't consume the same block.
func pilotLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another experiment command is running")
	}
	return f, nil
}

func pilotAssign(state *pilotState, key string) (string, error) {
	if group := state.Assignments[key]; group != "" {
		return group, nil
	}
	if len(state.Pending) == 0 {
		state.Pending = []string{"control", "control", "compression", "compression"}
		for i := len(state.Pending) - 1; i > 0; i-- {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
			if err != nil {
				return "", err
			}
			j := int(n.Int64())
			state.Pending[i], state.Pending[j] = state.Pending[j], state.Pending[i]
		}
	}
	group := state.Pending[0]
	state.Pending = state.Pending[1:]
	state.Assignments[key] = group
	return group, nil
}

func pilotLimits(account Account) *AccountUsage {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, err := probeClaudeUsage(ctx, account)
	if err != nil {
		return nil
	}
	u.Target, u.Source = account.Name, account.Tool
	return &u
}

func pilotEnvironment(account Account, base string) []envOverride {
	env := claudeEnvironment(&account)
	return append(env, envOverride{"ANTHROPIC_BASE_URL", base},
		envOverride{"CLAUDE_CODE_USE_BEDROCK", ""}, envOverride{"CLAUDE_CODE_USE_VERTEX", ""},
		envOverride{"CLAUDE_CODE_USE_FOUNDRY", ""}, envOverride{"ANTHROPIC_API_KEY", ""},
		envOverride{"ANTHROPIC_AUTH_TOKEN", ""})
}

func pilotProxyEnv(dir string) []string {
	// Explicit settings override Headroom's file-backed defaults. Avoid wrapper
	// setup entirely: it can register MCPs or rewrite native account configs.
	env := []envOverride{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "HEADROOM_") {
			env = append(env, envOverride{name, ""})
		}
	}
	for name, value := range map[string]string{
		"HEADROOM_CONFIG_DIR": filepath.Join(dir, "config"), "HEADROOM_WORKSPACE_DIR": filepath.Join(dir, "state"),
		"HEADROOM_CCR_BACKEND": "sqlite", "HEADROOM_CCR_SQLITE_PATH": filepath.Join(filepath.Dir(dir), "ccr.db"), "HEADROOM_CCR_TTL_SECONDS": "604800",
		"HEADROOM_BEACON": "off", "HEADROOM_TELEMETRY": "on", "HEADROOM_OUTPUT_SHAPER": "0",
		"HEADROOM_UPDATE_CHECK":           "off",
		"HEADROOM_TOOL_SEARCH":            "0",
		"HEADROOM_COMPRESS_USER_MESSAGES": "0", "HEADROOM_COMPRESS_SYSTEM_MESSAGES": "0",
		"HEADROOM_NO_CCR": "0", "HEADROOM_FORCE_KOMPRESS_ALL": "0",
		"HEADROOM_SAVINGS_PATH":        filepath.Join(dir, "stats.json"),
		"HEADROOM_SAVINGS_EVENTS_PATH": filepath.Join(dir, "savings.jsonl"),
		"DO_NOT_TRACK":                 "1",
	} {
		env = append(env, envOverride{name, value})
	}
	return childEnvironment(env)
}

func pilotProxy(binary, dir string) (string, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	args := []string{"proxy", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--mode", "cache",
		"--no-cache", "--no-rate-limit", "--no-memory-tools", "--no-memory-context", "--no-learn", "--no-code-aware",
		"--disable-kompress", "--disable-kompress-fallback"}
	p := exec.Command(binary, args...)
	p.Env = pilotProxyEnv(dir)
	p.Dir = dir
	p.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = p.Start(); err != nil {
		return "", nil, err
	}
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	stop := func() {
		_ = syscall.Kill(-p.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-p.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return "", nil, fmt.Errorf("Headroom proxy exited during startup; run the pinned headroom proxy --help to check dependencies")
		default:
		}
		res, e := client.Get(base + "/health")
		if e == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return base, stop, nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	stop()
	return "", nil, fmt.Errorf("Headroom proxy did not become healthy in 45 seconds")
}

// Only allowlisted numerical stats enter the experiment record. Provider
// payloads, prompts, tool text, headers, and error bodies never do.
func pilotStats(base string) map[string]any {
	client := &http.Client{Timeout: 3 * time.Second}
	r, err := client.Get(base + "/stats")
	if err != nil {
		return nil
	}
	defer r.Body.Close()
	var raw map[string]any
	if r.StatusCode != 200 || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&raw) != nil {
		return nil
	}
	result := map[string]any{}
	for _, section := range []string{"tokens", "requests", "cache", "compression", "overhead", "telemetry"} {
		if values, ok := raw[section].(map[string]any); ok {
			clean := map[string]float64{}
			for _, key := range []string{"input", "output", "saved", "proxy_compression_saved", "proxy_total_before_compression", "total_before_compression", "savings_percent", "total", "cached", "failed", "rate_limited", "total_hits", "ccr_retrievals", "average_ms", "min_ms", "max_ms", "total_compressions", "total_retrievals"} {
				if v, ok := values[key].(float64); ok {
					clean[key] = v
				}
			}
			result[section] = clean
		}
	}
	return result
}

func RunExperimentCommand(argv []string, out io.Writer) (int, error) {
	if len(argv) == 0 {
		return 2, fmt.Errorf("usage: agb experiment run|report|outcome [flags]")
	}
	mode := argv[0]
	flags := flag.NewFlagSet("agb experiment "+mode, flag.ContinueOnError)
	flags.SetOutput(out)
	dirFlag := flags.String("dir", filepath.Join(homeDir(), ".cache", "agentbridge", "headroom-pilot"), "private experiment storage")
	task := flags.String("task", "", "stable task ID (reuse on resume and fork)")
	target := flags.String("target", "", "one configured Claude account")
	model := flags.String("model", "", "fixed native model ID")
	binary := flags.String("headroom", "headroom", "pinned Headroom executable")
	outcome := flags.String("status", "", "completed, partial, or abandoned")
	correction := flags.Bool("context-correction", false, "missing context required a correction")
	if err := flags.Parse(argv[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, err
	}
	if mode != "run" && mode != "report" && mode != "outcome" {
		return 2, fmt.Errorf("unknown experiment command %q", mode)
	}
	dir, err := filepath.Abs(*dirFlag)
	if err != nil {
		return 2, err
	}
	if mode == "run" && dryRunEnabled() {
		return 2, fmt.Errorf("experiment run does not support AGB_DRY_RUN; no assignment or launch recorded")
	}
	lock, err := pilotLock(dir)
	if err != nil {
		return 2, err
	}
	defer lock.Close()
	if mode == "report" {
		if flags.NArg() != 0 {
			return 2, fmt.Errorf("report takes no positional arguments")
		}
		return pilotReport(dir, out)
	}
	if strings.TrimSpace(*task) == "" {
		return 2, fmt.Errorf("--task is required; reuse it for the same task's resumes and forks")
	}
	if mode == "outcome" {
		if flags.NArg() != 0 {
			return 2, fmt.Errorf("outcome takes no positional arguments")
		}
		return pilotOutcome(dir, pilotHash(*task), *outcome, *correction)
	}
	if *target == "" || *model == "" {
		return 2, fmt.Errorf("run requires --target and --model")
	}
	// CLI flags that change model/provider/account routing would invalidate the
	// fixed experiment settings. Treat ordinary task text as opaque.
	for _, arg := range flags.Args() {
		for _, forbidden := range []string{"--model", "--settings", "--setting-sources", "--betas", "--fallback-model", "--agents", "--agent"} {
			if arg == forbidden || strings.HasPrefix(arg, forbidden+"=") {
				return 2, fmt.Errorf("%s cannot override pilot settings", forbidden)
			}
		}
	}
	var account *Account
	config := LoadConfig()
	for _, candidate := range config.ClaudeAccounts {
		if candidate.Name == *target {
			copy := candidate
			account = &copy
		}
	}
	if account == nil {
		return 2, fmt.Errorf("--target must name a configured Claude account")
	}
	bin, err := exec.LookPath(*binary)
	if err != nil {
		return 2, fmt.Errorf("install headroom-ai[proxy]==%s and pass --headroom /absolute/path/headroom", pilotHeadroomVersion)
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return 2, err
	}
	versionCmd := exec.Command(bin, "--version")
	versionCmd.Env = pilotProxyEnv(dir)
	version, err := versionCmd.Output()
	if err != nil || strings.TrimSpace(string(version)) != "headroom, version "+pilotHeadroomVersion {
		return 2, fmt.Errorf("pilot requires Headroom %s", pilotHeadroomVersion)
	}
	state := pilotState{Version: 1, Target: *target, Model: *model, Home: claudeHome(*account), Assignments: map[string]string{}}
	statePath := filepath.Join(dir, "assignments.json")
	if raw, e := os.ReadFile(statePath); e == nil {
		if err = json.Unmarshal(raw, &state); err != nil {
			return 2, err
		}
		if state.Version != 1 || state.Target != *target || state.Model != *model || state.Home != claudeHome(*account) || state.Assignments == nil {
			return 2, fmt.Errorf("account/model settings changed; use a separate --dir for a new experiment")
		}
	} else if !os.IsNotExist(e) {
		return 2, e
	}
	group, err := pilotAssign(&state, pilotHash(*task))
	if err != nil {
		return 2, err
	}
	if err = pilotWrite(statePath, state); err != nil {
		return 2, err
	}
	runDir, err := os.MkdirTemp(dir, "run-")
	if err != nil {
		return 2, err
	}
	record := pilotRun{ID: filepath.Base(runDir), Task: pilotHash(*task), Group: group, Target: *target, Model: *model, HeadroomVersion: pilotHeadroomVersion, Started: time.Now().UTC(), Status: "started"}
	path := filepath.Join(runDir, "run.json")
	if err = pilotWrite(path, record); err != nil {
		return 2, err
	}
	code, runErr := pilotExecute(bin, runDir, *account, *model, flags.Args(), &record, out)
	finished := time.Now().UTC()
	record.Finished = &finished
	record.ExitCode = &code
	if runErr != nil {
		record.Status = "launch-failed"
	} else {
		record.Status = "finished"
	}
	if err = pilotWrite(path, record); err != nil {
		return 2, fmt.Errorf("could not save final metrics: %w", err)
	}
	return code, runErr
}

func pilotExecute(bin, dir string, account Account, model string, args []string, record *pilotRun, out io.Writer) (int, error) {
	record.Before = pilotLimits(account)
	base := "https://api.anthropic.com"
	if record.Group == "compression" {
		proxyBase, stop, err := pilotProxy(bin, dir)
		if err != nil {
			return 1, err
		}
		base = proxyBase
		defer stop()
		record.StatsBefore = pilotStats(base)
	}
	relay, err := newPilotRelay(base)
	if err != nil {
		return 1, err
	}
	defer relay.Close()
	defer func() {
		relay.Close()
		if record.Group == "compression" {
			record.Stats = pilotStats(base)
		}
		record.Requests = relay.Snapshot()
		record.After = pilotLimits(account)
	}()
	fmt.Fprintf(out, "Headroom pilot: %s · task %s · %s · %s\n", record.Group, record.Task[:12], account.Name, model)
	cmd := exec.Command("claude", append([]string{"--model", model}, args...)...)
	cmd.Env = childEnvironment(pilotEnvironment(account, relay.URL))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Child shares the terminal's process group, so Ctrl+C reaches it directly.
	// Keep the recorder alive to save the exit status and stop its sidecar.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err = cmd.Start(); err != nil {
		return 127, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case sig := <-sigs:
			_ = cmd.Process.Signal(sig)
		case err = <-done:
			if err == nil {
				return 0, nil
			}
			if exit, ok := err.(*exec.ExitError); ok {
				return exit.ExitCode(), nil
			}
			return 1, err
		}
	}
}

func pilotRuns(dir string) ([]pilotRun, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "run-*", "run.json"))
	if err != nil {
		return nil, err
	}
	runs := []pilotRun{}
	for _, path := range paths {
		raw, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		var run pilotRun
		if e = json.Unmarshal(raw, &run); e != nil {
			return nil, e
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Started.Before(runs[j].Started) })
	return runs, nil
}

func pilotReport(dir string, out io.Writer) (int, error) {
	runs, err := pilotRuns(dir)
	if err != nil {
		return 2, err
	}
	// Retain per-launch records for analysis instead of inventing p95 or cost
	// estimates from global aggregate counters.
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return 0, enc.Encode(map[string]any{"version": 1, "groups": pilotSummaries(runs), "runs": runs, "note": "No automatic verdict. Unfinished records may be active or interrupted. First byte measures HTTP body arrival, not first visible text. Provider token coverage and outcomes require review."})
}

func pilotOutcome(dir, task, status string, correction bool) (int, error) {
	if status != "completed" && status != "partial" && status != "abandoned" {
		return 2, fmt.Errorf("--status must be completed, partial, or abandoned")
	}
	runs, err := pilotRuns(dir)
	if err != nil {
		return 2, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Task == task {
			runs[i].Outcome = status
			runs[i].ContextCorrection = correction
			return 0, pilotWrite(filepath.Join(dir, runs[i].ID, "run.json"), runs[i])
		}
	}
	return 2, fmt.Errorf("no launch recorded for this task")
}
