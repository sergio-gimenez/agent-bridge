package ocs

import (
	"math"
	"sort"
)

type pilotSummary struct {
	Tasks              int              `json:"tasks"`
	Launches           int              `json:"launches"`
	LaunchFailures     int              `json:"launchFailures"`
	Unfinished         int              `json:"unfinished"`
	CompletedTasks     int              `json:"completedTasks"`
	RatedTasks         int              `json:"ratedTasks"`
	ContextCorrections int              `json:"contextCorrections"`
	Requests           int              `json:"requests"`
	FailedRequests     int              `json:"failedRequests"`
	RateLimited        int              `json:"rateLimitedRequests"`
	UsageRequests      int              `json:"requestsWithUsage"`
	ProviderTokens     map[string]int64 `json:"observedProviderTokens"`
	FirstByteMedian    *float64         `json:"firstByteMedianMs,omitempty"`
	FirstByteP95       *float64         `json:"firstByteP95Ms,omitempty"`
	DurationMedian     *float64         `json:"requestDurationMedianMs,omitempty"`
	DurationP95        *float64         `json:"requestDurationP95Ms,omitempty"`
}

func pilotPercentile(values []float64, p float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	value := values[int(math.Ceil(p*float64(len(values))))-1]
	return &value
}

func pilotSummaries(runs []pilotRun) map[string]pilotSummary {
	result := map[string]pilotSummary{}
	for _, group := range []string{"control", "compression"} {
		s := pilotSummary{ProviderTokens: map[string]int64{}}
		latest := map[string]pilotRun{}
		corrections := map[string]bool{}
		var first, duration []float64
		for _, run := range runs {
			if run.Group != group {
				continue
			}
			s.Launches++
			latest[run.Task] = run
			if run.ContextCorrection {
				corrections[run.Task] = true
			}
			if run.Status == "launch-failed" {
				s.LaunchFailures++
			}
			if run.Finished == nil {
				s.Unfinished++
			}
			for _, req := range run.Requests {
				s.Requests++
				if req.Status >= 400 {
					s.FailedRequests++
				}
				if req.Status == 429 {
					s.RateLimited++
				}
				if len(req.Usage) > 0 {
					s.UsageRequests++
					for key, n := range req.Usage {
						s.ProviderTokens[key] += n
					}
				}
				if req.Status >= 200 && req.Status < 300 && !req.Incomplete {
					duration = append(duration, req.DurationMS)
					if req.FirstByteMS != nil {
						first = append(first, *req.FirstByteMS)
					}
				}
			}
		}
		s.Tasks = len(latest)
		s.ContextCorrections = len(corrections)
		for _, run := range latest {
			if run.Outcome != "" {
				s.RatedTasks++
			}
			if run.Outcome == "completed" {
				s.CompletedTasks++
			}
		}
		s.FirstByteMedian = pilotPercentile(first, .5)
		s.FirstByteP95 = pilotPercentile(first, .95)
		s.DurationMedian = pilotPercentile(duration, .5)
		s.DurationP95 = pilotPercentile(duration, .95)
		result[group] = s
	}
	return result
}
