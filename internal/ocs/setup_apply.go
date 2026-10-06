package ocs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func checkParent(path string) error {
	parent := filepath.Dir(path)
	for {
		info, err := os.Stat(parent)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("destination parent is not a directory: %s", parent)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return err
		}
		parent = next
	}
}

func writeSetupFile(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".agentbridge-sync-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func writeSetupLink(path, source string) error {
	if source == "" {
		return os.Remove(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".agentbridge-link-*")
	if err != nil {
		return err
	}
	file.Close()
	defer os.Remove(file.Name())
	if err := os.Remove(file.Name()); err != nil {
		return err
	}
	if err := os.Symlink(source, file.Name()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// ApplySetupPlan validates every precondition before changing registrations.
// Backups and rollback cover failures across the individually atomic writes.
func ApplySetupPlan(plan *SetupPlan) error {
	if plan.HasConflicts() {
		return fmt.Errorf("setup has conflicts; no changes applied")
	}
	if !plan.HasChanges() {
		return nil
	}
	if err := checkParent(plan.statePath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plan.statePath), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(plan.statePath), ".sync.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another setup sync is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	stateNow, err := os.ReadFile(plan.statePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !bytes.Equal(stateNow, plan.stateBefore) {
		return fmt.Errorf("ownership state changed since planning; rerun sync")
	}
	for path, expected := range plan.files {
		current, err := readSetupFile(path)
		if err != nil {
			return err
		}
		if current.Exists != expected.Exists || !bytes.Equal(current.Before, expected.Before) {
			return fmt.Errorf("configuration changed since planning: %s; rerun sync", path)
		}
		if err := checkParent(path); err != nil {
			return err
		}
	}
	for _, link := range plan.links {
		current, err := linkState(link.Path)
		if err != nil {
			return err
		}
		if current != link.Before {
			return fmt.Errorf("skill destination changed since planning: %s; rerun sync", link.Path)
		}
		if err := checkParent(link.Path); err != nil {
			return err
		}
		if link.After != "" {
			if info, err := os.Stat(filepath.Join(link.After, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("skill source changed since planning: %s", link.After)
			}
		}
	}
	backupDir := filepath.Join(filepath.Dir(plan.statePath), "sync-backups")
	for path, file := range plan.files {
		if file.Exists && !bytes.Equal(file.Before, file.After) {
			backup := filepath.Join(backupDir, fingerprint([]string{path, string(file.Before)})+".bak")
			if err := writeSetupFile(backup, file.Before, 0o600); err != nil {
				return err
			}
		}
	}
	var appliedFiles []string
	var appliedLinks []setupLink
	rollback := func(cause error) error {
		failed := false
		for i := len(appliedLinks) - 1; i >= 0; i-- {
			link := appliedLinks[i]
			current, err := linkState(link.Path)
			if err != nil || current != link.After {
				failed = true
				continue
			}
			if link.Before != "" {
				err = writeSetupLink(link.Path, link.Before)
			} else {
				err = os.Remove(link.Path)
			}
			if err != nil {
				failed = true
			}
		}
		for i := len(appliedFiles) - 1; i >= 0; i-- {
			path := appliedFiles[i]
			file := plan.files[path]
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(current, file.After) {
				failed = true
				continue
			}
			if file.Exists {
				err = writeSetupFile(path, file.Before, file.Mode)
			} else {
				err = os.Remove(path)
			}
			if err != nil {
				failed = true
			}
		}
		if failed {
			return fmt.Errorf("sync failed and rollback was incomplete; private backups: %s: %w", backupDir, cause)
		}
		return fmt.Errorf("sync failed; applied registrations rolled back: %w", cause)
	}
	for _, path := range sortedKeys(plan.files) {
		file := plan.files[path]
		if bytes.Equal(file.Before, file.After) {
			continue
		}
		if err := writeSetupFile(path, file.After, file.Mode); err != nil {
			return rollback(err)
		}
		appliedFiles = append(appliedFiles, path)
	}
	for _, link := range plan.links {
		if err := writeSetupLink(link.Path, link.After); err != nil {
			return rollback(err)
		}
		appliedLinks = append(appliedLinks, link)
	}
	raw, err := json.MarshalIndent(plan.state, "", "  ")
	if err != nil {
		return rollback(err)
	}
	if err := writeSetupFile(plan.statePath, append(raw, '\n'), 0o600); err != nil {
		return rollback(err)
	}
	return nil
}
