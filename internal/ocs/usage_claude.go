package ocs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// errUsageSignInExpired is an account whose OAuth access token has lapsed. The
// tool renews it the next time it runs on that account; agb never does, since
// rotating the refresh token here would sign the tool itself out.
var errUsageSignInExpired = errors.New("sign-in expired")

var (
	claudeUsageURL    = "https://api.anthropic.com/api/oauth/usage"
	claudeUsageClient = &http.Client{Timeout: 10 * time.Second}
)

type claudeCredentials struct {
	OAuth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
		Plan        string `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
	Locked      *string  `json:"locked_reason"`
}

type claudeUsageResponse struct {
	FiveHour claudeUsageWindow `json:"five_hour"`
	SevenDay claudeUsageWindow `json:"seven_day"`
}

func claudeHome(account Account) string {
	if account.Home != "" {
		return account.Home
	}
	return filepath.Join(homeDir(), ".claude")
}

func probeClaudeUsage(ctx context.Context, account Account) (AccountUsage, error) {
	var credentials claudeCredentials
	raw, err := os.ReadFile(filepath.Join(claudeHome(account), ".credentials.json"))
	if err != nil {
		return AccountUsage{}, err
	}
	if err := json.Unmarshal(raw, &credentials); err != nil || credentials.OAuth.AccessToken == "" {
		return AccountUsage{}, fmt.Errorf("claude oauth credentials unavailable")
	}
	if credentials.OAuth.ExpiresAt > 0 && time.Now().UnixMilli() >= credentials.OAuth.ExpiresAt {
		return AccountUsage{}, errUsageSignInExpired
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return AccountUsage{}, err
	}
	request.Header.Set("Authorization", "Bearer "+credentials.OAuth.AccessToken)
	request.Header.Set("anthropic-beta", "oauth-2025-04-20")
	request.Header.Set("User-Agent", "agentbridge")
	response, err := claudeUsageClient.Do(request)
	if err != nil {
		return AccountUsage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return AccountUsage{}, fmt.Errorf("claude usage returned HTTP %d", response.StatusCode)
	}
	var payload claudeUsageResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return AccountUsage{}, err
	}

	usage := AccountUsage{Status: UsageAvailable, Plan: credentials.OAuth.Plan,
		ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	add := func(name string, window claudeUsageWindow) {
		if window.Utilization == nil {
			return
		}
		entry := UsageWindow{Name: name, UsedPercent: *window.Utilization}
		if window.ResetsAt != nil {
			entry.ResetsAt = *window.ResetsAt
		}
		usage.Windows = append(usage.Windows, entry)
		if window.Locked != nil || *window.Utilization >= 100 {
			usage.Status = UsageBlocked
		}
	}
	add("5h", payload.FiveHour)
	add("7d", payload.SevenDay)
	if len(usage.Windows) == 0 {
		return AccountUsage{}, fmt.Errorf("claude usage returned no limit windows")
	}
	return usage, nil
}
