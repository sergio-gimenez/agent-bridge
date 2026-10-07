package ocs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A demo remote is a directory standing in for another machine's home: what
// this side calls ~/x is <root>/x there, and the remote reports this home as
// its own, as a real second machine of the same user would.
func TestDemoRemote(t *testing.T) {
	linuxOnly(t)
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not available")
	}
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGB_DEMO_REMOTES", "sna=/elsewhere,desk="+root)

	var out bytes.Buffer
	link := dialRemote("desk", &out)
	if _, ok := link.(demoRemote); !ok {
		t.Fatalf("desk dialled %T", link)
	}
	if _, ok := dialRemote("other", &out).(sshRemote); !ok {
		t.Fatal("a host not named in AGB_DEMO_REMOTES must be a real ssh host")
	}

	writeFile(t, filepath.Join(root, "phd", "notes.md"), "on desk")
	got, err := link.Probe(`echo "home=$HOME"; cat ` + filepath.Join(home, "phd", "notes.md") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "home="+home+"\non desk" {
		t.Fatalf("probe printed %q", got)
	}

	transcript := filepath.Join(home, ".claude-cc2", "projects", "-p", "s.jsonl")
	writeFile(t, transcript, "here\n")
	writeFile(t, filepath.Join(home, ".claude-cc2", "projects", "-p", "s", "r.txt"), "result")
	if err := link.Copy([]string{transcript, strings.TrimSuffix(transcript, ".jsonl")}); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{".claude-cc2/projects/-p/s.jsonl": "here\n", ".claude-cc2/projects/-p/s/r.txt": "result"} {
		if raw, err := os.ReadFile(filepath.Join(root, rel)); err != nil || string(raw) != want {
			t.Errorf("%s there: %q %v", rel, raw, err)
		}
	}

	writeFile(t, filepath.Join(root, ".claude-cc2", "projects", "-p", "s.jsonl"), "here\nand there\n")
	if err := link.Fetch(transcript, filepath.Join(home, "pulled.jsonl")); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(home, "pulled.jsonl")); string(raw) != "here\nand there\n" {
		t.Errorf("fetched %q", raw)
	}

	if err := link.Run("echo ran in $(pwd | sed s,^" + root + ",ROOT,)"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ran in ROOT\n" {
		t.Errorf("run printed %q", out.String())
	}
}
