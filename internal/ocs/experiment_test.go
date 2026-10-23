package ocs

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPilotAssignmentsPersistAndBalance(t *testing.T) {
	state := pilotState{Assignments: map[string]string{}}
	dir := t.TempDir()
	counts := map[string]int{}
	for _, task := range []string{"a", "b", "c", "d"} {
		group, err := pilotAssign(&state, task)
		if err != nil {
			t.Fatal(err)
		}
		counts[group]++
		if err = pilotWrite(filepath.Join(dir, "state.json"), state); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "state.json"))
		var restored pilotState
		if err = json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		again, err := pilotAssign(&restored, task)
		if err != nil || again != group || len(restored.Pending) != len(state.Pending) {
			t.Fatal("resume changed assignment")
		}
		state = restored
	}
	if counts["control"] != 2 || counts["compression"] != 2 {
		t.Fatal(counts)
	}
	info, _ := os.Stat(filepath.Join(dir, "state.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	lock, err := pilotLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if other, err := pilotLock(dir); err == nil {
		other.Close()
		t.Fatal("concurrent experiment acquired lock")
	}
}

func TestPilotRelayPreservesSSEAndRecordsOnlyUsage(t *testing.T) {
	response := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"test-model\",\"usage\":{\"input_tokens\":100,\"output_tokens\":1,\"cache_read_input_tokens\":50,\"service_tier\":\"standard\",\"cache_creation\":{\"ephemeral_5m_input_tokens\":10}}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"SECRET_RESPONSE\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "SECRET_PROMPT" || r.Header.Get("Authorization") != "Bearer SECRET_TOKEN" {
			t.Error("relay changed payload/auth")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, part := range strings.SplitAfter(response, "\n") {
			_, _ = io.WriteString(w, part)
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()
	relay, err := newPilotRelay(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	req, _ := http.NewRequest("POST", relay.URL+"/v1/messages?beta=true", strings.NewReader("SECRET_PROMPT"))
	req.Header.Set("Authorization", "Bearer SECRET_TOKEN")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != response {
		t.Fatal("SSE changed")
	}
	var records []pilotRequest
	for i := 0; i < 100; i++ {
		records = relay.Snapshot()
		if len(records) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(records) != 1 || records[0].Incomplete || records[0].Usage["output_tokens"] != 20 || records[0].Usage["input_tokens"] != 100 || records[0].FirstByteMS == nil {
		t.Fatalf("%+v", records)
	}
	raw, _ := json.Marshal(records)
	if bytes.Contains(raw, []byte("SECRET")) {
		t.Fatal("content leaked")
	}
	res, err = http.Get(relay.URL + "/admin/runtime-env")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("admin surface exposed")
	}
}

func TestPilotReportCountsTasksNotResumes(t *testing.T) {
	done := time.Now()
	first := 12.0
	runs := []pilotRun{{Task: "a", Group: "control", Status: "finished", Finished: &done, Outcome: "partial", ContextCorrection: true, Requests: []pilotRequest{{Status: 200, FirstByteMS: &first, Usage: map[string]int64{"input_tokens": 10}}}},
		{Task: "a", Group: "control", Status: "finished", Finished: &done, Outcome: "completed"},
		{Task: "b", Group: "compression", Status: "launch-failed", Finished: &done}}
	s := pilotSummaries(runs)
	if s["control"].Tasks != 1 || s["control"].Launches != 2 || s["control"].CompletedTasks != 1 || s["control"].ContextCorrections != 1 || s["compression"].LaunchFailures != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestPilotAccountAndSettingsIsolation(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "wrong")
	t.Setenv("ANTHROPIC_API_KEY", "SECRET")
	t.Setenv("HEADROOM_OUTPUT_SHAPER", "1")
	env := childEnvironment(pilotEnvironment(Account{Home: "/desired/account"}, "http://127.0.0.1:9999"))
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CLAUDE_CONFIG_DIR=/desired/account") || strings.Contains(joined, "ANTHROPIC_API_KEY=") {
		t.Fatal("account isolation failed")
	}
	proxy := strings.Join(pilotProxyEnv(t.TempDir()), "\n")
	if !strings.Contains(proxy, "HEADROOM_OUTPUT_SHAPER=0") || strings.Contains(proxy, "HEADROOM_OUTPUT_SHAPER=1") {
		t.Fatal("inherited treatment setting leaked")
	}
}
