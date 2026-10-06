package ocs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func claudeLineJSON(t *testing.T, text string) string {
	return jsonl(t, obj{"type": "user", "cwd": "/w", "message": obj{"role": "user", "content": text}})
}

// scan runs one launch's worth of reading against the cache at cachePath and
// saves it, like AgentBridge does.
func scan(t *testing.T, cachePath string, account *Account) []Session {
	t.Helper()
	cache := OpenCache(cachePath, false)
	sessions := cache.fileSessions(claudeSource, account, ScopeUser)
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}
	return sessions
}

func prompts(sessions []Session) []string {
	var out []string
	for _, session := range sessions {
		out = append(out, strings.Split(session.SearchText, "\n")[3:]...)
	}
	return out
}

// touch moves the mtime forward, since a fast test can rewrite a file within
// one filesystem timestamp tick.
func touch(t *testing.T, path string) {
	t.Helper()
	later := time.Now().Add(time.Duration(time.Now().UnixNano()%1000+1) * time.Millisecond)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func setupClaude(t *testing.T) (account *Account, path, cachePath string) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Account{Tool: SourceClaude, Name: "cc", Home: home},
		filepath.Join(dir, "s.jsonl"),
		filepath.Join(t.TempDir(), "index.gob")
}

func TestCacheResumesAnAppendedTranscript(t *testing.T) {
	account, path, cachePath := setupClaude(t)
	os.WriteFile(path, []byte(claudeLineJSON(t, "one")+"\n"), 0o644)
	if got := prompts(scan(t, cachePath, account)); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("first scan = %v", got)
	}

	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	file.WriteString(claudeLineJSON(t, "two") + "\n")
	file.Close()
	touch(t, path)

	if got := prompts(scan(t, cachePath, account)); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("after append = %v", got)
	}
	// Unchanged: served from the cache, still identical.
	if got := prompts(scan(t, cachePath, account)); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("unchanged = %v", got)
	}
}

func TestCacheCountsALineStillBeingWritten(t *testing.T) {
	account, path, cachePath := setupClaude(t)
	full := claudeLineJSON(t, "one") + "\n"
	partial := claudeLineJSON(t, "two")
	os.WriteFile(path, []byte(full+partial), 0o644)

	if got := prompts(scan(t, cachePath, account)); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("with a final line lacking its newline = %v", got)
	}

	// The writer finishes that line and adds another: nothing counted twice.
	os.WriteFile(path, []byte(full+partial+"\n"+claudeLineJSON(t, "three")+"\n"), 0o644)
	touch(t, path)
	if got := prompts(scan(t, cachePath, account)); !reflect.DeepEqual(got, []string{"one", "two", "three"}) {
		t.Fatalf("after the line completed = %v", got)
	}
}

func TestCacheRereadsARewrittenTranscript(t *testing.T) {
	account, path, cachePath := setupClaude(t)
	os.WriteFile(path, []byte(claudeLineJSON(t, "original")+"\n"), 0o644)
	scan(t, cachePath, account)

	// Same size or larger, different content: must not be treated as an append.
	os.WriteFile(path, []byte(claudeLineJSON(t, "replaced")+"\n"+claudeLineJSON(t, "more")+"\n"), 0o644)
	touch(t, path)
	if got := prompts(scan(t, cachePath, account)); !reflect.DeepEqual(got, []string{"replaced", "more"}) {
		t.Fatalf("after rewrite = %v", got)
	}
}

func TestCacheKeepsOtherAccountsAndDropsDeletedFiles(t *testing.T) {
	first, firstPath, cachePath := setupClaude(t)
	second, secondPath, _ := setupClaude(t)
	os.WriteFile(firstPath, []byte(claudeLineJSON(t, "a")+"\n"), 0o644)
	os.WriteFile(secondPath, []byte(claudeLineJSON(t, "b")+"\n"), 0o644)
	scan(t, cachePath, first)
	scan(t, cachePath, second)

	// A run that only looks at the second account keeps the first one's entry.
	cache := OpenCache(cachePath, false)
	if _, ok := cache.last().Files[firstPath]; !ok {
		t.Fatal("an unscanned account's entry was pruned")
	}

	os.Remove(firstPath)
	scan(t, cachePath, second)
	if _, ok := OpenCache(cachePath, false).last().Files[firstPath]; ok {
		t.Fatal("a deleted transcript stayed in the cache")
	}
}

func TestCacheIgnoresAnotherVersion(t *testing.T) {
	account, path, cachePath := setupClaude(t)
	os.WriteFile(path, []byte(claudeLineJSON(t, "one")+"\n"), 0o644)
	os.WriteFile(cachePath, []byte("not a gob"), 0o644)

	if got := prompts(scan(t, cachePath, account)); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("with a corrupt cache = %v", got)
	}
	if _, ok := OpenCache(cachePath, false).last().Files[path]; !ok {
		t.Fatal("a corrupt cache was not replaced")
	}
}
