package ocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var limitsNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func limitsPicker() *picker {
	p := mousePicker()
	p.clock = func() time.Time { return limitsNow }
	p.usage = UsageReport{Accounts: []AccountUsage{
		{Target: "oc", Source: SourceOpencode, Status: UsageUnknown, Detail: "provider does not expose subscription limits"},
		{Target: "cc1", Source: SourceClaude, Status: UsageAvailable, Windows: []UsageWindow{
			{Name: "5h", UsedPercent: 48, ResetsAt: "2026-10-15T14:30:00Z"}, {Name: "7d", UsedPercent: 59, ResetsAt: "2026-10-18T12:00:00Z"}}},
		{Target: "cc2", Source: SourceClaude, Status: UsageBlocked, Detail: "cached; refresh unavailable", Windows: []UsageWindow{
			{Name: "5h", UsedPercent: 100, ResetsAt: "2026-10-15T12:00:30Z"}}},
		{Target: "cx1", Source: SourceCodex, Status: UsageAvailable, Windows: []UsageWindow{{Name: "5h", UsedPercent: 0}}},
	}}
	finalizeUsage(&p.usage, targets, p.now())
	return p
}

// projectsPicker has sessions in three real directories, a newest first, and
// one in a directory that no longer exists.
func projectsPicker(t *testing.T) (*picker, string) {
	root := t.TempDir()
	for _, dir := range []string{"alpha", "beta", "gamma", "beta/inner"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := limitsPicker()
	at := float64(limitsNow.UnixMilli())
	for i, entry := range []struct {
		dir string
		age time.Duration
	}{{"beta", 2 * time.Hour}, {"alpha", time.Minute}, {"gone", 0}, {"gamma", 3 * time.Hour}, {"alpha", time.Hour}} {
		p.sessions[i].Directory = filepath.Join(root, entry.dir)
		p.sessions[i].UpdatedAtMs = at - float64(entry.age.Milliseconds())
	}
	for i := 5; i < len(p.sessions); i++ {
		p.sessions[i].Directory = ""
	}
	return p, root
}

func plain(frame string) string { return ansiPattern.ReplaceAllString(frame, "") }

func TestNewConversationShowsEveryAccountsLimits(t *testing.T) {
	p := limitsPicker()
	p.openNew("/tmp/project")
	frame := plain(p.frame(140, 30))
	for _, want := range []string{"Account", "CC1", "48%", "59%", "7d resets in 3d", "blocked", "5h resets now", "cached", "★ suggested", "no limits reported", "^g use CX1"} {
		if !strings.Contains(frame, want) {
			t.Errorf("dialog lacks %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "refresh unavailable") {
		t.Error("cached detail not shortened")
	}
	// Every account names its windows 5h/7d, so the names sit in the header.
	if strings.Count(frame, "5h ") > 2 {
		t.Errorf("window names repeated on every row:\n%s", frame)
	}
}

// A snapshot kept because the sign-in lapsed, observed before both of its
// windows reset: the old figures say nothing now, and it is no longer blocked.
func TestNewConversationStaleLimits(t *testing.T) {
	p := limitsPicker()
	p.usage.Accounts[2] = AccountUsage{Target: "cc2", Source: SourceClaude, Status: UsageBlocked,
		Detail: "cached; sign-in expired", Windows: []UsageWindow{
			{Name: "5h", UsedPercent: 3, ResetsAt: "2026-10-15T02:30:00Z"},
			{Name: "7d", UsedPercent: 100, ResetsAt: "2026-10-15T05:00:00Z"}}}
	finalizeUsage(&p.usage, targets, p.now())
	p.openNew("/tmp/project")
	var row string
	for _, line := range strings.Split(plain(p.frame(160, 30)), "\n") {
		if strings.Contains(line, "CC2") {
			row = line
		}
	}
	for _, want := range []string{"–", "5h reset since checked", "sign-in expired: open CC2 to renew"} {
		if !strings.Contains(row, want) {
			t.Errorf("CC2 row lacks %q: %s", want, row)
		}
	}
	for _, stale := range []string{"3%", "100%", "blocked", "resets now"} {
		if strings.Contains(row, stale) {
			t.Errorf("CC2 row still shows %q: %s", stale, row)
		}
	}
	if got := p.usage.Accounts[2].RemainingPercent; got == nil || *got != 100 {
		t.Errorf("windows past their reset still limit the account: %v", got)
	}
}

func TestNewConversationRefreshesStaleLimits(t *testing.T) {
	p := limitsPicker()
	p.usageKick = make(chan struct{}, 1)
	p.usage.GeneratedAt = limitsNow.Add(-10 * time.Minute).Format(time.RFC3339)
	p.openNew("/tmp/project")
	if len(p.usageKick) != 1 || !p.usageLoading {
		t.Fatal("opening on stale limits did not ask for a refresh")
	}
	if !strings.Contains(plain(p.frame(140, 30)), "checking limits") {
		t.Error("the dialog does not say it is checking")
	}
	p.newConversation, p.usageLoading = nil, false
	<-p.usageKick
	p.usage.GeneratedAt = limitsNow.Format(time.RFC3339)
	p.openNew("/tmp/project")
	if len(p.usageKick) != 0 {
		t.Fatal("fresh limits were refreshed again")
	}
}

func TestNewConversationPicksAccountFromLimits(t *testing.T) {
	p := limitsPicker()
	p.openNew("/tmp/project")
	p.frame(140, 30)
	press(t, p, Key{Ctrl: true, Name: "g"})
	if got := p.newTarget(); got.Name != "cx1" {
		t.Fatalf("Ctrl+G picked %s, want the suggested cx1", got.Name)
	}
	press(t, p, Key{Name: "left"})
	if got := p.newTarget(); got.Name != "cc2" {
		t.Fatalf("left picked %s", got.Name)
	}
	p.frame(140, 30)
	clickMouse(t, p, 5, p.newConversation.accountRow+1)
	if got := p.newTarget(); got.Name != "cc1" {
		t.Fatalf("click picked %s, want cc1", got.Name)
	}
}

func TestNewConversationSuggestsRecentProjects(t *testing.T) {
	p, root := projectsPicker(t)
	p.openNew(filepath.Join(root, "beta"))
	dialog := p.newConversation
	var paths []string
	for _, entry := range dialog.dirSuggestions() {
		paths = append(paths, filepath.Base(entry.path))
	}
	if strings.Join(paths, ",") != "alpha,beta,gamma" {
		t.Fatalf("suggestions %v, want newest first and only existing ones", paths)
	}
	if dialog.choice != 1 {
		t.Fatalf("the opening directory is not highlighted: %d", dialog.choice)
	}
	frame := plain(p.frame(120, 30))
	if !strings.Contains(frame, "2 sessions · 1m") {
		t.Errorf("alpha's sessions not counted:\n%s", frame)
	}

	press(t, p, Key{Name: "up"})
	if dialog.directory != ShortenHome(filepath.Join(root, "alpha")) {
		t.Fatalf("up chose %q", dialog.directory)
	}
	p.frame(120, 30)
	clickMouse(t, p, 5, dialog.dirRow+2)
	result := press(t, p, enter)
	if result == nil || result.Session.Directory != filepath.Join(root, "gamma") {
		t.Fatalf("clicked suggestion not used: %+v", result)
	}
}

func TestNewConversationFiltersAndCompletesDirectories(t *testing.T) {
	p, root := projectsPicker(t)
	p.openNew(root)
	dialog := p.newConversation
	press(t, p, Key{Name: "u", Ctrl: true}, Key{Text: "gam"})
	if got := dialog.dirSuggestions(); len(got) != 1 || filepath.Base(got[0].path) != "gamma" {
		t.Fatalf("filter gave %+v", got)
	}
	press(t, p, Key{Name: "down"})
	// Moving through the list keeps it filtered by what was typed.
	if len(dialog.dirSuggestions()) != 1 || filepath.Base(dialog.directory) != "gamma" {
		t.Fatalf("moving refiltered the list: %q", dialog.directory)
	}

	press(t, p, Key{Name: "u", Ctrl: true}, Key{Text: filepath.Join(root, "beta") + "/"})
	got := dialog.dirSuggestions()
	if len(got) == 0 || got[0].path != filepath.Join(root, "beta", "inner") {
		t.Fatalf("path completion gave %+v", got)
	}
	press(t, p, Key{Name: "u", Ctrl: true}, Key{Text: filepath.Join(root, "b")})
	got = dialog.dirSuggestions()
	if len(got) == 0 || got[0].path != filepath.Join(root, "beta") || got[0].sessions != 1 {
		t.Fatalf("prefix completion gave %+v", got)
	}
	press(t, p, Key{Name: "down"}, Key{Text: "/"})
	if dialog.directory != ShortenHome(filepath.Join(root, "beta"))+"/" {
		t.Fatalf("typing after a choice did not continue from it: %q", dialog.directory)
	}
}
