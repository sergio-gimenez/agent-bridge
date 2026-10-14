package ocs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"
)

type codexRPCResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type codexLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	ResetsAt           *int64  `json:"resetsAt"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
}

type codexRateLimits struct {
	PlanType             *string           `json:"planType"`
	Primary              *codexLimitWindow `json:"primary"`
	Secondary            *codexLimitWindow `json:"secondary"`
	RateLimitReachedType *string           `json:"rateLimitReachedType"`
	SpendControlReached  *bool             `json:"spendControlReached"`
}

type codexUsageResponse struct {
	OrdinaryUsageAllowed *bool                      `json:"ordinaryUsageAllowed"`
	RateLimits           codexRateLimits            `json:"rateLimits"`
	RateLimitsByLimitID  map[string]codexRateLimits `json:"rateLimitsByLimitId"`
}

func codexWindow(name string, value *codexLimitWindow) *UsageWindow {
	if value == nil {
		return nil
	}
	window := &UsageWindow{Name: name, UsedPercent: value.UsedPercent}
	if value.ResetsAt != nil {
		window.ResetsAt = time.Unix(*value.ResetsAt, 0).UTC().Format(time.RFC3339)
	}
	if value.WindowDurationMins != nil {
		window.DurationMinutes = *value.WindowDurationMins
		if *value.WindowDurationMins == 300 {
			window.Name = "5h"
		} else if *value.WindowDurationMins == 7*24*60 {
			window.Name = "7d"
		}
	}
	return window
}

func decodeCodexUsage(raw json.RawMessage) (AccountUsage, error) {
	var payload codexUsageResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return AccountUsage{}, err
	}
	limits := payload.RateLimits
	if named, ok := payload.RateLimitsByLimitID["codex"]; ok {
		limits = named
	}
	usage := AccountUsage{Status: UsageAvailable, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	if limits.PlanType != nil {
		usage.Plan = *limits.PlanType
	}
	if window := codexWindow("primary", limits.Primary); window != nil {
		usage.Windows = append(usage.Windows, *window)
	}
	if window := codexWindow("secondary", limits.Secondary); window != nil {
		usage.Windows = append(usage.Windows, *window)
	}
	if payload.OrdinaryUsageAllowed != nil && !*payload.OrdinaryUsageAllowed ||
		limits.RateLimitReachedType != nil || limits.SpendControlReached != nil && *limits.SpendControlReached {
		usage.Status = UsageBlocked
	}
	if len(usage.Windows) == 0 && usage.Status != UsageBlocked {
		return AccountUsage{}, fmt.Errorf("codex usage returned no limit windows")
	}
	return usage, nil
}

func probeCodexUsage(ctx context.Context, account Account) (AccountUsage, error) {
	command := exec.CommandContext(ctx, "codex", "app-server", "--stdio")
	command.Env = childEnvironment(codexEnvironment(&account))
	stdin, err := command.StdinPipe()
	if err != nil {
		return AccountUsage{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return AccountUsage{}, err
	}
	if err := command.Start(); err != nil {
		return AccountUsage{}, err
	}
	finished := false
	defer func() {
		_ = stdin.Close()
		if !finished && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	encoder := json.NewEncoder(stdin)
	requests := []any{
		map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
			"clientInfo":   map[string]string{"name": "agentbridge", "version": "1"},
			"capabilities": map[string]any{},
		}},
		map[string]any{"method": "initialized"},
		map[string]any{"id": 2, "method": "account/rateLimits/read", "params": map[string]any{
			"excludeResetCreditDetails": true,
		}},
	}
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			return AccountUsage{}, err
		}
	}

	decoder := json.NewDecoder(stdout)
	for {
		var response codexRPCResponse
		if err := decoder.Decode(&response); err != nil {
			if err == io.EOF && ctx.Err() != nil {
				return AccountUsage{}, ctx.Err()
			}
			return AccountUsage{}, err
		}
		if response.ID != 2 {
			continue
		}
		if response.Error != nil {
			return AccountUsage{}, fmt.Errorf("codex usage RPC %d", response.Error.Code)
		}
		usage, err := decodeCodexUsage(response.Result)
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
		finished = true
		return usage, err
	}
}
