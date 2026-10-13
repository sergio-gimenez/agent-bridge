package ocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrRightClickRouting(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	binary := filepath.Join(dir, "herdr")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$AGB_TEST_HERDR_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGB_TEST_HERDR_ARGS", log)
	t.Setenv("HERDR_BIN_PATH", binary)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w5:p1")
	if err := enableHerdrRightClick(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "pane\ninput\n--pane\nw5:p1\n--right-click\npane\n" {
		t.Fatalf("routing: %q, %v", data, err)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	for _, env := range []struct{ herdr, pane string }{{"", "w5:p1"}, {"1", ""}} {
		t.Setenv("HERDR_ENV", env.herdr)
		t.Setenv("HERDR_PANE_ID", env.pane)
		if err := enableHerdrRightClick(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatal("changed routing without an explicit Herdr pane")
		}
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w5:p1")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho unsupported >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := enableHerdrRightClick(); err == nil || !strings.Contains(err.Error(), "Ctrl+E") {
		t.Fatalf("no useful error: %v", err)
	}
}
