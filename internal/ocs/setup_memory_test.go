package ocs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memoryFixture is two Claude accounts with a profile sharing cc1's memory.
func memoryFixture(t *testing.T) (Config, string, string, string) {
	t.Helper()
	root := t.TempDir()
	cc1, cc2 := filepath.Join(root, "claude"), filepath.Join(root, "claude-cc2")
	raw, err := json.Marshal(map[string]any{
		"claudeAccounts": []obj{{"name": "cc1", "configDir": cc1}, {"name": "cc2", "configDir": cc2}},
		"setup": obj{"version": 1, "profiles": obj{"mine": obj{
			"targets": []string{"cc1", "cc2"},
			"memory":  obj{"share": []string{"cc1", "cc2"}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	return config, path, cc1, cc2
}

func writeMemory(t *testing.T, home, project string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(home, "projects", project, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func memoryActions(plan *SetupPlan) map[string]string {
	actions := map[string]string{}
	for _, step := range plan.Steps {
		if step.Kind == "memory" {
			actions[step.Name] = step.Action
		}
	}
	return actions
}

func TestSharedMemoryJoinsTheSecondAccountToTheFirst(t *testing.T) {
	config, path, cc1, cc2 := memoryFixture(t)
	// Only cc1 has it, only cc2 has it, both have it.
	writeMemory(t, cc1, "-w-only1", map[string]string{"MEMORY.md": "- [A](a.md) — a\n", "a.md": "a"})
	writeMemory(t, cc2, "-w-only2", map[string]string{"MEMORY.md": "- [B](b.md) — b\n", "b.md": "b"})
	writeMemory(t, cc1, "-w-both", map[string]string{"MEMORY.md": "- [A](a.md) — a\n", "a.md": "a", "same.md": "s"})
	writeMemory(t, cc2, "-w-both", map[string]string{"MEMORY.md": "- [C](c.md) — c\n- [A](a.md) — other hook\n", "c.md": "c", "same.md": "s"})
	// Both have it, but cc2's folder is empty: a merge with no index to join.
	writeMemory(t, cc1, "-w-empty", map[string]string{"a.md": "a"})
	writeMemory(t, cc2, "-w-empty", map[string]string{})

	plan, err := BuildSetupPlan(config, path, "mine", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"-w-only1": "link", "-w-only2": "move", "-w-both": "merge", "-w-empty": "merge"}
	if got := memoryActions(plan); len(got) != 4 || got["-w-only1"] != want["-w-only1"] || got["-w-only2"] != want["-w-only2"] ||
		got["-w-both"] != want["-w-both"] || got["-w-empty"] != want["-w-empty"] {
		t.Fatalf("planned %v, want %v", got, want)
	}
	if err := ApplySetupPlan(plan); err != nil {
		t.Fatal(err)
	}

	for _, project := range []string{"-w-only1", "-w-only2", "-w-both", "-w-empty"} {
		link := filepath.Join(cc2, "projects", project, "memory")
		if target, err := os.Readlink(link); err != nil || target != filepath.Join(cc1, "projects", project, "memory") {
			t.Fatalf("%s: %s -> %q, %v", project, link, target, err)
		}
	}
	if got := readSeed(t, filepath.Join(cc1, "projects", "-w-only2", "memory", "b.md")); got != "b" {
		t.Fatal("cc2-only memory did not move to the store")
	}
	both := filepath.Join(cc1, "projects", "-w-both", "memory")
	if readSeed(t, filepath.Join(both, "c.md")) != "c" || readSeed(t, filepath.Join(both, "same.md")) != "s" {
		t.Fatal("merge lost a file")
	}
	if got := readSeed(t, filepath.Join(both, "MEMORY.md")); got != "- [A](a.md) — a\n- [C](c.md) — c\n" {
		t.Fatalf("index joined as %q", got)
	}
	// cc2's own index is kept, not deleted.
	saved, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "sync-backups", "memory", "cc2", "-w-both-*", "account", "MEMORY.md"))
	if len(saved) != 1 || !strings.Contains(readSeed(t, saved[0]), "other hook") {
		t.Fatalf("cc2's own index is gone: %v", saved)
	}

	again, err := BuildSetupPlan(config, path, "mine", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.HasChanges() {
		t.Fatalf("second sync is not a no-op: %+v", again.Steps)
	}
	// Writing through the link lands in the store.
	if err := os.WriteFile(filepath.Join(cc2, "projects", "-w-both", "memory", "new.md"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if readSeed(t, filepath.Join(both, "new.md")) != "n" {
		t.Fatal("cc2 does not write into the shared store")
	}
}

func TestSharedMemoryRefusesAFileThatDiffers(t *testing.T) {
	config, path, cc1, cc2 := memoryFixture(t)
	writeMemory(t, cc1, "-w-p", map[string]string{"fact.md": "one version"})
	writeMemory(t, cc2, "-w-p", map[string]string{"fact.md": "another version", "extra.md": "x"})
	plan, err := BuildSetupPlan(config, path, "mine", "")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.HasConflicts() || !strings.Contains(plan.Steps[len(plan.Steps)-1].Message, "fact.md") {
		t.Fatalf("expected a conflict naming fact.md: %+v", plan.Steps)
	}
	if err := ApplySetupPlan(plan); err == nil {
		t.Fatal("a conflict must stop the sync")
	}
	if _, err := os.Lstat(filepath.Join(cc1, "projects", "-w-p", "memory", "extra.md")); err == nil {
		t.Fatal("a refused sync moved files")
	}
}

func TestSharedMemoryValidation(t *testing.T) {
	config, path, _, _ := memoryFixture(t)
	profile := config.Setup.Profiles["mine"]
	for _, share := range [][]string{{"cc1"}, {"cc1", "cx9"}} {
		profile.Memory = &SetupMemory{Share: share}
		config.Setup.Profiles["mine"] = profile
		if _, err := BuildSetupPlan(config, path, "mine", ""); err == nil {
			t.Fatalf("share %v should be refused", share)
		}
	}
}

func TestMergeMemoryIndexKeepsTheStoreAndAddsTheRest(t *testing.T) {
	store := "# Memory\n\n- [A](a.md) — a\n"
	other := "# Other heading\n- [A](a.md) — a again\n* [B](b.md) — b\nprose line\n"
	if got := string(mergeMemoryIndex([]byte(store), []byte(other))); got != "# Memory\n\n- [A](a.md) — a\n* [B](b.md) — b\n" {
		t.Fatalf("got %q", got)
	}
	if got := mergeMemoryIndex([]byte(store), []byte("- [A](a.md) — x\n")); string(got) != store {
		t.Fatalf("nothing new should leave the store untouched, got %q", got)
	}
}
