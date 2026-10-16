package ocs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func percent(value float64) *float64 { return &value }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestUsageRecommendationUsesWorstWindowAndSkipsBlocked(t *testing.T) {
	targets := []Account{
		{Tool: SourceClaude, Name: "cc1"},
		{Tool: SourceClaude, Name: "cc2"},
		{Tool: SourceCodex, Name: "cx1"},
	}
	report := UsageReport{Accounts: []AccountUsage{
		{Target: "cx1", Source: SourceCodex, Status: UsageAvailable,
			Windows: []UsageWindow{{Name: "5h", UsedPercent: 5}, {Name: "7d", UsedPercent: 80}}},
		{Target: "cc2", Source: SourceClaude, Status: UsageBlocked,
			Windows: []UsageWindow{{Name: "5h", UsedPercent: 1}}},
		{Target: "cc1", Source: SourceClaude, Status: UsageAvailable,
			Windows: []UsageWindow{{Name: "5h", UsedPercent: 30}, {Name: "7d", UsedPercent: 40}}},
	}}
	finalizeUsage(&report, targets, time.Unix(0, 0))
	if report.Recommended != "cc1" || !report.Accounts[0].Recommended {
		t.Fatalf("report = %+v", report)
	}
	if got := *report.Accounts[0].RemainingPercent; got != 60 {
		t.Fatalf("remaining = %v", got)
	}
	if order := []string{report.Accounts[0].Target, report.Accounts[1].Target, report.Accounts[2].Target}; strings.Join(order, ",") != "cc1,cc2,cx1" {
		t.Fatalf("order = %v", order)
	}
}

func TestUsageRecommendationTieKeepsConfiguredOrder(t *testing.T) {
	targets := []Account{{Tool: SourceClaude, Name: "cc2"}, {Tool: SourceCodex, Name: "cx1"}}
	report := UsageReport{Accounts: []AccountUsage{
		{Target: "cx1", Source: SourceCodex, Status: UsageAvailable, Windows: []UsageWindow{{UsedPercent: 25}}},
		{Target: "cc2", Source: SourceClaude, Status: UsageAvailable, Windows: []UsageWindow{{UsedPercent: 25}}},
	}}
	finalizeUsage(&report, targets, time.Now())
	if report.Recommended != "cc2" {
		t.Fatalf("recommendation = %q", report.Recommended)
	}
}

func TestPrintUsageCallsBlockedAccountsBlocked(t *testing.T) {
	report := UsageReport{Accounts: []AccountUsage{{Target: "cc1", Status: UsageBlocked,
		RemainingPercent: percent(80), Windows: []UsageWindow{{ResetsAt: "2026-10-08T18:00:00Z"}}}}}
	var out bytes.Buffer
	PrintUsage(&out, report)
	if got := out.String(); !strings.Contains(got, "blocked") || strings.Contains(got, "80% remaining") {
		t.Fatalf("output = %q", got)
	}
}

func TestUsageCacheIsPrivateAndFiltersConfiguredTargets(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGB_CACHE_PATH", filepath.Join(root, "index.gob"))
	report := UsageReport{Version: usageCacheVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Accounts: []AccountUsage{
		{Target: "old", Source: SourceClaude, Status: UsageAvailable, Windows: []UsageWindow{{UsedPercent: 1}}},
		{Target: "cc1", Source: SourceClaude, Status: UsageAvailable, Windows: []UsageWindow{{UsedPercent: 20}}},
	}}
	if err := WriteUsageReport(UsagePath(), report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(UsagePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", info.Mode().Perm(), err)
	}
	config := Config{Opencode: OpencodeAccount,
		ClaudeAccounts: []Account{{Tool: SourceClaude, Name: "cc1"}}, DefaultClaudeAccount: "cc1",
		CodexAccounts: []Account{{Tool: SourceCodex, Name: "cx1"}}, DefaultCodexAccount: "cx1"}
	got := CachedUsage(config)
	if len(got.Accounts) != 3 || got.Accounts[1].Target != "cc1" || got.Accounts[2].Target != "cx1" ||
		got.Accounts[2].Status != UsageUnknown {
		t.Fatalf("cache = %+v", got.Accounts)
	}
}

func TestCurrentUsageKeepsLastKnownValueWhenRefreshFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGB_CACHE_PATH", filepath.Join(root, "index.gob"))
	observed := "2026-10-08T10:00:00Z"
	old := UsageReport{Version: usageCacheVersion, GeneratedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		Accounts: []AccountUsage{{Target: "cc1", Source: SourceClaude, Status: UsageAvailable,
			ObservedAt: observed, Windows: []UsageWindow{{Name: "5h", UsedPercent: 35}}}}}
	if err := WriteUsageReport(UsagePath(), old); err != nil {
		t.Fatal(err)
	}
	original := usageProbes[SourceClaude]
	usageProbes[SourceClaude] = func(context.Context, Account) (AccountUsage, error) {
		return AccountUsage{}, os.ErrNotExist
	}
	defer func() { usageProbes[SourceClaude] = original }()
	config := Config{Opencode: OpencodeAccount,
		ClaudeAccounts: []Account{{Tool: SourceClaude, Name: "cc1"}}, DefaultClaudeAccount: "cc1"}

	report := CurrentUsage(config, true)
	usage := report.For(&config.ClaudeAccounts[0])
	if usage == nil || usage.Status != UsageAvailable || usage.ObservedAt != observed ||
		usage.Detail != "cached; refresh unavailable" || report.Recommended != "cc1" {
		t.Fatalf("fallback = %+v, report = %+v", usage, report)
	}

	// A lapsed sign-in says so, since opening the account is what fixes it.
	usageProbes[SourceClaude] = func(context.Context, Account) (AccountUsage, error) {
		return AccountUsage{}, errUsageSignInExpired
	}
	if err := WriteUsageReport(UsagePath(), old); err != nil {
		t.Fatal(err)
	}
	report = CurrentUsage(config, true)
	if usage := report.For(&config.ClaudeAccounts[0]); usage == nil || usage.Detail != "cached; sign-in expired" {
		t.Fatalf("sign-in fallback = %+v", usage)
	}
}

func TestSuggestionPrefersCurrentFiguresOverCached(t *testing.T) {
	now := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	targets := []Account{{Tool: SourceClaude, Name: "cc2"}, {Tool: SourceClaude, Name: "cc3"}}
	report := UsageReport{Accounts: []AccountUsage{
		// Both windows reset after it was seen, so it reads as 100% free.
		{Target: "cc2", Source: SourceClaude, Status: UsageAvailable, Detail: "cached; sign-in expired",
			Windows: []UsageWindow{{Name: "7d", UsedPercent: 73, ResetsAt: "2026-10-16T05:00:00Z"}}},
		{Target: "cc3", Source: SourceClaude, Status: UsageAvailable,
			Windows: []UsageWindow{{Name: "5h", UsedPercent: 4}}},
	}}
	finalizeUsage(&report, targets, now)
	if report.Recommended != "cc3" || *report.Accounts[0].RemainingPercent != 100 {
		t.Fatalf("suggested %q with cc2 at %v", report.Recommended, *report.Accounts[0].RemainingPercent)
	}
	report.Accounts[1].Status = UsageBlocked
	finalizeUsage(&report, targets, now)
	if report.Recommended != "cc2" {
		t.Fatalf("with nothing current, the cached account is not suggested: %q", report.Recommended)
	}
}

func TestDecodeCodexUsage(t *testing.T) {
	allowed := true
	raw, _ := json.Marshal(map[string]any{
		"ordinaryUsageAllowed": allowed,
		"rateLimits": map[string]any{
			"planType":  "pro",
			"primary":   map[string]any{"usedPercent": 18, "resetsAt": 1_800_000_000, "windowDurationMins": 300},
			"secondary": map[string]any{"usedPercent": 42, "windowDurationMins": 10080},
		},
	})
	usage, err := decodeCodexUsage(raw)
	if err != nil || usage.Status != UsageAvailable || usage.Plan != "pro" || len(usage.Windows) != 2 {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}
	if usage.Windows[0].Name != "5h" || usage.Windows[1].Name != "7d" || usage.Windows[0].ResetsAt == "" {
		t.Fatalf("windows = %+v", usage.Windows)
	}

	raw, _ = json.Marshal(map[string]any{
		"ordinaryUsageAllowed": false,
		"rateLimits":           map[string]any{"rateLimitReachedType": "rate_limit_reached"},
	})
	usage, err = decodeCodexUsage(raw)
	if err != nil || usage.Status != UsageBlocked {
		t.Fatalf("blocked usage = %+v, err = %v", usage, err)
	}
}

func TestProbeClaudeUsage(t *testing.T) {
	home := t.TempDir()
	credentials := `{"claudeAiOauth":{"accessToken":"secret","expiresAt":4102444800000,"subscriptionType":"pro"}}`
	if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("anthropic-beta") == "" {
			t.Error("missing Claude OAuth headers")
		}
		body := `{
			"five_hour":{"utilization":12.5,"resets_at":"2026-10-08T17:00:00Z","locked_reason":null},
			"seven_day":{"utilization":45,"resets_at":"2026-10-12T00:00:00Z","locked_reason":null}
		}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	oldURL, oldClient := claudeUsageURL, claudeUsageClient
	claudeUsageURL, claudeUsageClient = "https://usage.invalid", client
	defer func() { claudeUsageURL, claudeUsageClient = oldURL, oldClient }()

	usage, err := probeClaudeUsage(context.Background(), Account{Tool: SourceClaude, Name: "cc2", Home: home})
	if err != nil || usage.Status != UsageAvailable || usage.Plan != "pro" || len(usage.Windows) != 2 ||
		usage.Windows[0].UsedPercent != 12.5 {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}
}

func TestPickerRendersUsageSuggestion(t *testing.T) {
	p := newTestPicker([]Session{claudeTestSession()}, nil)
	p.usage = UsageReport{Recommended: "cx1", Accounts: []AccountUsage{{Target: "cx1", Source: SourceCodex,
		Status: UsageAvailable, RemainingPercent: percent(72), Recommended: true,
		Windows: []UsageWindow{{Name: "5h", UsedPercent: 28}}}}}
	p.pinned = 3
	frame := ansiPattern.ReplaceAllString(p.frame(150, 30), "")
	for _, want := range []string{"suggest CX1 72% free", "limits CX1 · 5h 28% used · suggested"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame lacks %q:\n%s", want, frame)
		}
	}
	p.pinned = -1
	_, _ = p.handle(Key{Name: "g", Ctrl: true})
	if p.currentTarget() == nil || p.currentTarget().Name != "cx1" {
		t.Fatalf("Ctrl+G target = %+v", p.currentTarget())
	}
}
