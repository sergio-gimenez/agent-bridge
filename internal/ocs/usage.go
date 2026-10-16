package ocs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	UsageAvailable = "available"
	UsageBlocked   = "blocked"
	UsageUnknown   = "unknown"

	usageCacheVersion = 1
	usageCacheMaxAge  = 2 * time.Minute
	usageProbeTimeout = 12 * time.Second
)

// UsageWindow is one provider-defined rolling limit. UsedPercent is normalized
// to 0..100; ResetsAt is RFC3339 when the provider supplies it.
type UsageWindow struct {
	Name            string  `json:"name"`
	UsedPercent     float64 `json:"usedPercent"`
	ResetsAt        string  `json:"resetsAt,omitempty"`
	DurationMinutes int64   `json:"durationMinutes,omitempty"`
}

// AccountUsage is the provider-neutral limit state for one configured target.
// Detail is deliberately coarse: credentials, endpoints and provider response
// bodies must never enter the cache or the JSON interface.
type AccountUsage struct {
	Target           string        `json:"target"`
	Source           Source        `json:"source"`
	Status           string        `json:"status"`
	Plan             string        `json:"plan,omitempty"`
	Windows          []UsageWindow `json:"windows,omitempty"`
	RemainingPercent *float64      `json:"remainingPercent,omitempty"`
	ObservedAt       string        `json:"observedAt,omitempty"`
	Detail           string        `json:"detail,omitempty"`
	Recommended      bool          `json:"recommended,omitempty"`
}

// UsageReport is the stable machine-readable boundary consumed by launchers
// and other integrations. AgentBridge has no knowledge of those consumers.
type UsageReport struct {
	Version     int            `json:"version"`
	GeneratedAt string         `json:"generatedAt"`
	Recommended string         `json:"recommended,omitempty"`
	Accounts    []AccountUsage `json:"accounts"`
}

type accountUsageProbe func(context.Context, Account) (AccountUsage, error)

var usageProbes = map[Source]accountUsageProbe{
	SourceClaude: probeClaudeUsage,
	SourceCodex:  probeCodexUsage,
}

func UsagePath() string {
	return filepath.Join(filepath.Dir(CachePath()), "limits.json")
}

func usageKey(source Source, target string) string {
	return string(source) + "\x00" + target
}

func clampPercent(value float64) float64 {
	return math.Max(0, math.Min(100, value))
}

// remainingFor is the headroom left in the tightest window. A window whose
// reset time has passed since it was observed has reset, so it no longer
// limits anything, though what it used since is unknown.
func remainingFor(windows []UsageWindow, now time.Time) *float64 {
	if len(windows) == 0 {
		return nil
	}
	remaining := 100.0
	for i := range windows {
		windows[i].UsedPercent = clampPercent(windows[i].UsedPercent)
		if windows[i].resetBy(now) {
			continue
		}
		remaining = math.Min(remaining, 100-windows[i].UsedPercent)
	}
	return &remaining
}

// resetBy reports whether the window's reset time is known and not after now.
func (window UsageWindow) resetBy(now time.Time) bool {
	resets, err := time.Parse(time.RFC3339, window.ResetsAt)
	return err == nil && !resets.After(now)
}

func finalizeUsage(report *UsageReport, targets []Account, now time.Time) {
	report.Version = usageCacheVersion
	if report.GeneratedAt == "" {
		report.GeneratedAt = now.UTC().Format(time.RFC3339)
	}
	// Keep output in Config.Targets order even when a caller supplies results in
	// another order. This also makes configured order the recommendation tie-breaker.
	order := map[string]int{}
	for i, target := range targets {
		order[usageKey(target.Tool, target.Name)] = i
	}
	sort.SliceStable(report.Accounts, func(i, j int) bool {
		return order[usageKey(report.Accounts[i].Source, report.Accounts[i].Target)] <
			order[usageKey(report.Accounts[j].Source, report.Accounts[j].Target)]
	})

	report.Recommended = ""
	bestRemaining := -1.0
	bestIndex := -1
	bestCached := true
	for i := range report.Accounts {
		usage := &report.Accounts[i]
		usage.Recommended = false
		usage.RemainingPercent = remainingFor(usage.Windows, now)
		if usage.Status != UsageAvailable || usage.RemainingPercent == nil {
			continue
		}
		// A figure that could not be refreshed only wins when nothing current
		// does: its windows may have reset, but what was used since is unknown.
		cached := strings.HasPrefix(usage.Detail, "cached")
		if cached && !bestCached {
			continue
		}
		// Strictly greater preserves configured target order as the tie-breaker.
		if *usage.RemainingPercent > bestRemaining || bestCached && !cached {
			bestRemaining = *usage.RemainingPercent
			bestIndex = i
			bestCached = cached
		}
	}
	if bestIndex >= 0 {
		report.Accounts[bestIndex].Recommended = true
		report.Recommended = report.Accounts[bestIndex].Target
	}
}

// ProbeUsage asks every supported account concurrently. Unsupported tools are
// reported as unknown so consumers still get the complete target registry.
func ProbeUsage(ctx context.Context, config Config) UsageReport {
	targets := config.Targets()
	accounts := make([]AccountUsage, len(targets))
	var wait sync.WaitGroup
	for i, target := range targets {
		i, target := i, target
		probe := usageProbes[target.Tool]
		if probe == nil {
			accounts[i] = AccountUsage{Target: target.Name, Source: target.Tool, Status: UsageUnknown,
				Detail: "provider does not expose subscription limits"}
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			usage, err := probe(ctx, target)
			usage.Target, usage.Source = target.Name, target.Tool
			if err != nil {
				usage.Status = UsageUnknown
				usage.Detail = "usage unavailable"
				if errors.Is(err, errUsageSignInExpired) {
					usage.Detail = errUsageSignInExpired.Error()
				}
			}
			if usage.Status == "" {
				usage.Status = UsageUnknown
			}
			accounts[i] = usage
		}()
	}
	wait.Wait()
	report := UsageReport{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Accounts: accounts}
	finalizeUsage(&report, targets, time.Now())
	return report
}

func ReadUsageReport(path string) (UsageReport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return UsageReport{}, err
	}
	var report UsageReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return UsageReport{}, err
	}
	if report.Version != usageCacheVersion {
		return UsageReport{}, fmt.Errorf("unsupported usage cache version %d", report.Version)
	}
	return report, nil
}

func WriteUsageReport(path string, report UsageReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".agentbridge-limits-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(report); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func cachedUsage(config Config) (UsageReport, bool) {
	report, err := ReadUsageReport(UsagePath())
	if err != nil {
		return UsageReport{}, false
	}
	// Drop removed accounts and add newly configured ones as unknown.
	byTarget := map[string]AccountUsage{}
	for _, usage := range report.Accounts {
		byTarget[usageKey(usage.Source, usage.Target)] = usage
	}
	report.Accounts = nil
	for _, target := range config.Targets() {
		usage, ok := byTarget[usageKey(target.Tool, target.Name)]
		if !ok {
			usage = AccountUsage{Target: target.Name, Source: target.Tool, Status: UsageUnknown,
				Detail: "not checked yet"}
		}
		report.Accounts = append(report.Accounts, usage)
	}
	finalizeUsage(&report, config.Targets(), time.Now())
	return report, true
}

// CachedUsage is fast and never performs network or starts an agent process.
// The picker uses it for its first frame, then refreshes in the background.
func CachedUsage(config Config) UsageReport {
	if report, ok := cachedUsage(config); ok {
		return report
	}
	report := UsageReport{Accounts: make([]AccountUsage, 0, len(config.Targets()))}
	for _, target := range config.Targets() {
		report.Accounts = append(report.Accounts, AccountUsage{Target: target.Name, Source: target.Tool,
			Status: UsageUnknown, Detail: "not checked yet"})
	}
	finalizeUsage(&report, config.Targets(), time.Now())
	return report
}

func (report UsageReport) For(target *Account) *AccountUsage {
	if target == nil {
		return nil
	}
	for i := range report.Accounts {
		if report.Accounts[i].Source == target.Tool && report.Accounts[i].Target == target.Name {
			return &report.Accounts[i]
		}
	}
	return nil
}

func usageFresh(report UsageReport, now time.Time) bool {
	generated, err := time.Parse(time.RFC3339, report.GeneratedAt)
	return err == nil && now.Sub(generated) >= 0 && now.Sub(generated) <= usageCacheMaxAge
}

// CurrentUsage returns the short-lived cache when possible, otherwise probes.
// A failed probe keeps a previously observed account value, marked with its old
// ObservedAt, rather than turning a useful offline snapshot into "unknown".
func CurrentUsage(config Config, force bool) UsageReport {
	previous, havePrevious := cachedUsage(config)
	if !force && havePrevious && usageFresh(previous, time.Now()) {
		return previous
	}
	ctx, cancel := context.WithTimeout(context.Background(), usageProbeTimeout)
	defer cancel()
	fresh := ProbeUsage(ctx, config)
	if havePrevious {
		old := map[string]AccountUsage{}
		for _, usage := range previous.Accounts {
			old[usageKey(usage.Source, usage.Target)] = usage
		}
		for i, usage := range fresh.Accounts {
			if usage.Status != UsageUnknown {
				continue
			}
			if fallback, ok := old[usageKey(usage.Source, usage.Target)]; ok && fallback.Status != UsageUnknown {
				fallback.Detail = "cached; refresh unavailable"
				if usage.Detail == errUsageSignInExpired.Error() {
					fallback.Detail = "cached; " + usage.Detail
				}
				fallback.Recommended = false
				fresh.Accounts[i] = fallback
			}
		}
		finalizeUsage(&fresh, config.Targets(), time.Now())
	}
	_ = WriteUsageReport(UsagePath(), fresh)
	return fresh
}

func WriteUsageJSON(out io.Writer, report UsageReport) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func PrintUsage(out io.Writer, report UsageReport) {
	for _, usage := range report.Accounts {
		label := strings.ToUpper(usage.Target)
		marker := "  "
		if usage.Recommended {
			marker = "* "
		}
		if usage.Status == UsageBlocked {
			fmt.Fprintf(out, "%s%-8s blocked", marker, label)
			if len(usage.Windows) > 0 && usage.Windows[0].ResetsAt != "" {
				fmt.Fprintf(out, " · resets %s", usage.Windows[0].ResetsAt)
			}
			fmt.Fprintln(out)
			continue
		}
		if usage.RemainingPercent == nil {
			fmt.Fprintf(out, "%s%-8s %s\n", marker, label, usage.Status)
			continue
		}
		fmt.Fprintf(out, "%s%-8s %.0f%% remaining", marker, label, *usage.RemainingPercent)
		if len(usage.Windows) > 0 && usage.Windows[0].ResetsAt != "" {
			fmt.Fprintf(out, " · %s resets %s", usage.Windows[0].Name, usage.Windows[0].ResetsAt)
		}
		fmt.Fprintln(out)
	}
	if report.Recommended != "" {
		fmt.Fprintf(out, "\nSuggested target: %s\n", strings.ToUpper(report.Recommended))
	}
}

func RunUsageCommand(argv []string, out io.Writer) (int, error) {
	jsonOutput, refresh := false, false
	for _, arg := range argv {
		switch arg {
		case "--json":
			jsonOutput = true
		case "--refresh":
			refresh = true
		case "--help", "-h":
			fmt.Fprintln(out, "usage: agb limits [--json] [--refresh]")
			return 0, nil
		default:
			return 2, fmt.Errorf("unknown limits option %q", arg)
		}
	}
	report := CurrentUsage(LoadConfig(), refresh)
	if jsonOutput {
		return 0, WriteUsageJSON(out, report)
	}
	PrintUsage(out, report)
	return 0, nil
}
