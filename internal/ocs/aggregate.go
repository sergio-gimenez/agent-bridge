package ocs

import (
	"sort"
	"strings"
	"sync"
)

func MergeSessions(groups ...[]Session) []Session {
	var merged []Session
	for _, group := range groups {
		merged = append(merged, group...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].UpdatedAtMs > merged[j].UpdatedAtMs
	})
	return merged
}

type fileSource struct {
	list    func(root string) []scannedFile
	root    func(*Account) string
	format  transcriptFormat
	session func(*record, scannedFile, SearchScope, *Account) Session
}

var (
	claudeSource = fileSource{listClaudeFiles, claudeProjectsPath, claudeFormat, claudeSession}
	codexSource  = fileSource{listCodexFiles, codexSessionsPath, codexFormat, codexSession}
)

func (cache *Cache) fileSessions(source fileSource, account *Account, scope SearchScope) []Session {
	files := source.list(source.root(account))
	records := cache.records(files, source.format)

	var sessions []Session
	for i, rec := range records {
		if rec != nil {
			sessions = append(sessions, source.session(rec, files[i], scope, account))
		}
	}
	return sessions
}

// AllSessions reads every store at once. A store that is missing or unreadable
// is skipped rather than fatal: not having Codex installed should still get
// you your Claude and OpenCode sessions, and the other way round.
func (cache *Cache) AllSessions(config Config, scope SearchScope) []Session {
	accounts := make([]Account, 0, len(config.ClaudeAccounts)+len(config.CodexAccounts))
	accounts = append(accounts, config.ClaudeAccounts...)
	accounts = append(accounts, config.CodexAccounts...)

	// One slot per store, filled in parallel, merged in a fixed order so equal
	// timestamps always list the same way.
	groups := make([][]Session, 1+len(accounts))
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		groups[0], _ = cache.OpencodeSessions(OpencodeDBPath(), scope)
	}()

	for i := range accounts {
		wg.Add(1)
		go func(slot int, account *Account) {
			defer wg.Done()
			source := claudeSource
			if account.Tool == SourceCodex {
				source = codexSource
			}
			groups[slot] = cache.fileSessions(source, account, scope)
		}(i+1, &accounts[i])
	}

	wg.Wait()
	return MergeSessions(groups...)
}

// SearchSessions keeps the sessions whose search text holds every term of the
// query, in any order.
func SearchSessions(sessions []Session, query string) []Session {
	normalized := strings.ToLower(CollapseWhitespace(query))
	if normalized == "" {
		return sessions
	}
	terms := strings.Split(normalized, " ")

	var matches []Session
	for i := range sessions {
		haystack := sessions[i].searchLower()
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, sessions[i])
		}
	}
	return matches
}
