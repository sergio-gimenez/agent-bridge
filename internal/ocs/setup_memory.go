package ocs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Shared Claude Code memory. Claude keeps auto-memory per project, in
// <config>/projects/<project>/memory, so two accounts (CLAUDE_CONFIG_DIR) each
// learn alone. A profile's "memory": {"share": ["cc1", "cc2"]} keeps the first
// account's folder as the one real store and makes every other account's
// folder a link to it. Memory either account already has is merged in first:
// new files move across, the MEMORY.md indexes are joined, and a file that
// differs between the two is a conflict for the user, never an overwrite.

type SetupMemory struct {
	Share []string `json:"share"`
}

const memoryIndex = "MEMORY.md"

// setupMemoryOp is one project folder of one account, joined to the store.
type setupMemoryOp struct {
	Account, Project string
	From, To         string // the account's memory folder; the store's
	Action           string // link, move or merge
	Moves            []string
	Index            []byte // merged MEMORY.md for To, nil if untouched
	Snapshot         string
}

func (memory *SetupMemory) validate(accounts map[string]Account, selected map[string]bool) ([]Account, error) {
	if len(memory.Share) < 2 {
		return nil, fmt.Errorf("memory.share needs at least two Claude accounts, the first being the store")
	}
	var shared []Account
	homes := map[string]string{}
	for _, name := range memory.Share {
		account, exists := accounts[name]
		if !exists || !selected[name] {
			return nil, fmt.Errorf("memory.share target %q is not in profile targets", name)
		}
		if account.Tool != SourceClaude {
			return nil, fmt.Errorf("memory.share target %q is not a Claude Code account", name)
		}
		projects := filepath.Clean(claudeProjectsPath(&account))
		if other, duplicate := homes[projects]; duplicate {
			return nil, fmt.Errorf("memory.share targets %q and %q use the same config dir", other, name)
		}
		homes[projects] = name
		shared = append(shared, account)
	}
	return shared, nil
}

// memoryFolders lists the projects that have a memory folder (or link) under
// a Claude projects dir.
func memoryFolders(projects string) ([]string, error) {
	entries, err := os.ReadDir(projects)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if _, err := os.Lstat(filepath.Join(projects, entry.Name(), "memory")); err == nil {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// memorySnapshot pins what a plan saw, so apply can refuse a folder that a
// running session changed in the meantime.
func memorySnapshot(paths ...string) string {
	var parts []string
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			parts = append(parts, path+" missing")
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			parts = append(parts, path+" -> "+target)
			continue
		}
		entries, _ := os.ReadDir(path)
		for _, entry := range entries {
			if detail, err := entry.Info(); err == nil {
				parts = append(parts, fmt.Sprintf("%s/%s %d %d", path, entry.Name(), detail.Size(), detail.ModTime().UnixNano()))
			}
		}
	}
	return fingerprint(parts)
}

func (plan *SetupPlan) planMemory(shared []Account, targetFilter string) error {
	store := shared[0]
	storeProjects := claudeProjectsPath(&store)
	stored, err := memoryFolders(storeProjects)
	if err != nil {
		return err
	}
	for _, account := range shared[1:] {
		if targetFilter != "" && account.Name != targetFilter {
			continue
		}
		projects := claudeProjectsPath(&account)
		own, err := memoryFolders(projects)
		if err != nil {
			return err
		}
		names := map[string]bool{}
		for _, name := range append(append([]string{}, stored...), own...) {
			names[name] = true
		}
		for _, project := range sortedKeys(names) {
			from := filepath.Join(projects, project, "memory")
			to := filepath.Join(storeProjects, project, "memory")
			step := SetupStep{Target: account.Name, Kind: "memory", Name: project, Path: from}
			op, err := planMemoryFolder(account.Name, project, from, to)
			if err != nil {
				step.Action, step.Message = "conflict", err.Error()
			} else if op == nil {
				step.Action = "present"
			} else {
				step.Action = op.Action
				step.Message = memoryMessage(op, store.Name)
				plan.memory = append(plan.memory, *op)
			}
			plan.Steps = append(plan.Steps, step)
		}
	}
	return nil
}

func memoryMessage(op *setupMemoryOp, store string) string {
	switch op.Action {
	case "link":
		return "link to " + store + "'s memory"
	case "move":
		return "move this memory to " + store + " and link to it"
	}
	parts := []string{fmt.Sprintf("move %d file(s) to %s", len(op.Moves), store)}
	if op.Index != nil {
		parts = append(parts, "join the MEMORY.md indexes")
	}
	return strings.Join(parts, ", ") + ", then link"
}

// planMemoryFolder works out how one account's memory folder joins the store,
// or nil when it already links there.
func planMemoryFolder(account, project, from, to string) (*setupMemoryOp, error) {
	op := &setupMemoryOp{Account: account, Project: project, From: from, To: to, Snapshot: memorySnapshot(from, to)}
	toInfo, toErr := os.Stat(to)
	if toErr == nil && !toInfo.IsDir() {
		return nil, fmt.Errorf("the store's memory is not a folder: %s", to)
	}
	fromInfo, err := os.Lstat(from)
	switch {
	case os.IsNotExist(err):
		op.Action = "link"
		return op, nil
	case err != nil:
		return nil, err
	case fromInfo.Mode()&os.ModeSymlink != 0:
		current, err := linkState(from)
		if err != nil {
			return nil, err
		}
		if want, err := filepath.EvalSymlinks(to); err == nil && current != filepath.Clean(to) {
			if resolved, err := filepath.EvalSymlinks(current); err != nil || resolved != want {
				return nil, fmt.Errorf("memory links somewhere other than the store: %s", current)
			}
		}
		return nil, nil
	case !fromInfo.IsDir():
		return nil, fmt.Errorf("memory is a file, not a folder")
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("memory holds %s, which is not a plain file; move it out first", entry.Name())
		}
	}
	if os.IsNotExist(toErr) {
		op.Action = "move"
		return op, nil
	}
	op.Action = "merge"
	var differ []string
	for _, entry := range entries {
		name := entry.Name()
		mine, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			return nil, err
		}
		theirs, err := os.ReadFile(filepath.Join(to, name))
		switch {
		case os.IsNotExist(err):
			op.Moves = append(op.Moves, name)
		case err != nil:
			return nil, err
		case bytes.Equal(mine, theirs):
		case name == memoryIndex:
			if merged := mergeMemoryIndex(theirs, mine); !bytes.Equal(merged, theirs) {
				op.Index = merged
			}
		default:
			differ = append(differ, name)
		}
	}
	if len(differ) > 0 {
		sort.Strings(differ)
		return nil, fmt.Errorf("both accounts have a different %s; keep one (or merge them by hand) and sync again", strings.Join(differ, ", "))
	}
	return op, nil
}

// mergeMemoryIndex keeps the store's index as it is and adds the other
// account's entries for files the store's index does not list yet. Headings
// and prose stay the store's; the other index survives in the sync backups.
func mergeMemoryIndex(store, other []byte) []byte {
	listed := map[string]bool{}
	for _, line := range strings.Split(string(store), "\n") {
		if target := memoryIndexTarget(line); target != "" {
			listed[target] = true
		}
	}
	merged := strings.TrimRight(string(store), "\n")
	added := false
	for _, line := range strings.Split(string(other), "\n") {
		target := memoryIndexTarget(line)
		if target == "" || listed[target] {
			continue
		}
		listed[target] = true
		merged += "\n" + strings.TrimRight(line, " \t")
		added = true
	}
	if !added {
		return store
	}
	return []byte(strings.TrimLeft(merged, "\n") + "\n")
}

// memoryIndexTarget is the file an index entry links to: "- [Title](file.md) — hook".
func memoryIndexTarget(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "- ") && !strings.HasPrefix(trimmed, "* ") {
		return ""
	}
	open := strings.Index(trimmed, "](")
	if open < 0 {
		return ""
	}
	end := strings.Index(trimmed[open+2:], ")")
	if end < 0 {
		return ""
	}
	return trimmed[open+2 : open+2+end]
}

// applyMemory joins one folder to the store. It only renames and adds: the
// account's own MEMORY.md, and anything else it held that the store already
// had, end up in the sync backups rather than deleted.
func applyMemory(op setupMemoryOp, backupDir string) error {
	if memorySnapshot(op.From, op.To) != op.Snapshot {
		return fmt.Errorf("memory changed since planning: %s; rerun sync", op.From)
	}
	if err := os.MkdirAll(filepath.Dir(op.From), 0o700); err != nil {
		return err
	}
	switch op.Action {
	case "move":
		if err := os.MkdirAll(filepath.Dir(op.To), 0o700); err != nil {
			return err
		}
		if err := os.Rename(op.From, op.To); err != nil {
			return err
		}
	case "merge":
		saved := filepath.Join(backupDir, "memory", op.Account, op.Project+"-"+time.Now().Format("20060102-150405"))
		if op.Index != nil {
			before, err := os.ReadFile(filepath.Join(op.To, memoryIndex))
			if err != nil {
				return err
			}
			if err := writeSetupFile(filepath.Join(saved, "store-"+memoryIndex), before, 0o600); err != nil {
				return err
			}
		}
		for _, name := range op.Moves {
			target := filepath.Join(op.To, name)
			if _, err := os.Lstat(target); err == nil {
				return fmt.Errorf("%s appeared in the store since planning; rerun sync", target)
			}
			if err := os.Rename(filepath.Join(op.From, name), target); err != nil {
				return err
			}
		}
		if op.Index != nil {
			if err := writeSetupFile(filepath.Join(op.To, memoryIndex), op.Index, 0o600); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(saved, 0o700); err != nil {
			return err
		}
		if err := os.Rename(op.From, filepath.Join(saved, "account")); err != nil {
			return err
		}
	}
	return writeSetupLink(op.From, op.To)
}
