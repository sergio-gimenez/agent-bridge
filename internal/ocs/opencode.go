package ocs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const opencodeSessionLimit = 200

func OpencodeDBPath() string {
	if path := os.Getenv("OPENCODE_DB_PATH"); path != "" {
		return path
	}
	return filepath.Join(homeDir(), ".local", "share", "opencode", "opencode.db")
}

func openOpencodeDB(path string) (*sql.DB, error) {
	// Opening a missing file would quietly create an empty database.
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	return sql.Open("sqlite", dsn)
}

type opencodeRow struct {
	ID          string
	Title       string
	Directory   string
	ProjectID   string
	TimeUpdated int64
}

// opencodeTables reports which session stores the database has. OpenCode 2
// moved sessions into session_v2 and session_message; a migrated database
// keeps the old session, message and part tables beside them.
func opencodeTables(db *sql.DB) (legacy, v2 bool, err error) {
	rows, err := db.Query(`select name from sqlite_master where type = 'table' and name in ('session', 'session_v2')`)
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, false, err
		}
		legacy, v2 = legacy || name == "session", v2 || name == "session_v2"
	}
	return legacy, v2, rows.Err()
}

// opencodeV2 is true when the store is OpenCode 2's, and so is the CLI that
// writes it: no --fork on the TUI, `session export`, --prompt for a new
// session.
func opencodeV2() bool {
	return opencodeAccountV2(nil)
}

func opencodeCommand(account *Account) string {
	if account != nil && account.Command != "" {
		return account.Command
	}
	return "opencode"
}

func opencodeAccountDB(account *Account) string {
	if account != nil && account.DBPath != "" {
		return account.DBPath
	}
	return OpencodeDBPath()
}

func opencodeAccountV2(account *Account) bool {
	db, err := openOpencodeDB(opencodeAccountDB(account))
	if err != nil {
		return false
	}
	defer db.Close()
	_, v2, _ := opencodeTables(db)
	return v2
}

func listOpencodeSessions(db *sql.DB, limit int) ([]opencodeRow, error) {
	legacy, v2, err := opencodeTables(db)
	if err != nil {
		return nil, err
	}
	// A session whose first turn failed has no title yet; one NULL must not
	// fail the scan and with it every OpenCode session.
	const columns = `id, coalesce(title, '') as title, coalesce(directory, '') as directory,
		coalesce(project_id, '') as project_id, time_updated`
	var stores []string
	if v2 {
		stores = append(stores, `select `+columns+` from session_v2 where time_archived is null`)
	}
	if legacy {
		stores = append(stores, `select `+columns+` from session where time_archived is null`)
	}
	if len(stores) == 0 {
		return nil, nil
	}
	// The migration copied every session into session_v2, and one left open in
	// an old OpenCode can keep going in the old table: one row per id, the
	// newer one (SQLite takes the bare columns from the max() row).
	rows, err := db.Query(`
		select id, title, directory, project_id, max(time_updated)
		from (`+strings.Join(stores, " union all ")+`)
		group by id
		order by max(time_updated) desc
		limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []opencodeRow
	for rows.Next() {
		var row opencodeRow
		if err := rows.Scan(&row.ID, &row.Title, &row.Directory, &row.ProjectID, &row.TimeUpdated); err != nil {
			return nil, err
		}
		sessions = append(sessions, row)
	}
	return sessions, rows.Err()
}

// parseTextPart pulls the prose out of a part's JSON; anything that is not a
// text string reads as empty and is dropped.
func parseTextPart(raw string) string {
	var part struct {
		Text json.RawMessage `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &part) != nil {
		return ""
	}
	text, _ := rawString(part.Text)
	return CollapseWhitespace(text)
}

// latestTexts returns, per session, the newest texts written in the given
// role, newest first: from session_message where OpenCode 2 keeps them, from
// message and part for sessions only the old store has.
func latestTexts(db *sql.DB, ids []string, role Role) (map[string][]string, error) {
	texts := map[string][]string{}
	if len(ids) == 0 {
		return texts, nil
	}
	legacy, v2, err := opencodeTables(db)
	if err != nil {
		return nil, err
	}
	missing := ids
	if v2 {
		if err := latestTextsV2(db, ids, role, texts); err != nil {
			return nil, err
		}
		missing = nil
		for _, id := range ids {
			if len(texts[id]) == 0 {
				missing = append(missing, id)
			}
		}
	}
	if legacy && len(missing) > 0 {
		if err := latestTextsLegacy(db, missing, role, texts); err != nil {
			return nil, err
		}
	}
	return texts, nil
}

func inPlaceholders(ids []string) (string, []any) {
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

func collectTexts(rows *sql.Rows, texts map[string][]string, parse func(string) string) error {
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if text := parse(raw); text != "" {
			texts[id] = append(texts[id], text)
		}
	}
	return rows.Err()
}

// OpenCode 2 writes a user message's text on the message itself and an
// assistant's as text items of its content.
func latestTextsV2(db *sql.DB, ids []string, role Role, texts map[string][]string) error {
	placeholders, args := inPlaceholders(ids)
	text := `json_extract(m.data, '$.text')`
	from := `session_message m`
	if role == RoleAssistant {
		text = `json_extract(c.value, '$.text')`
		from = `session_message m join json_each(m.data, '$.content') c
			on json_extract(c.value, '$.type') = 'text'`
	}
	args = append(args, string(role), promptLimit)
	rows, err := db.Query(fmt.Sprintf(`
		select ranked.session_id, ranked.text
		from (
			select
				m.session_id as session_id,
				%s as text,
				row_number() over (
					partition by m.session_id
					order by m.time_created desc, m.seq desc
				) as rank
			from %s
			where m.session_id in (%s) and m.type = ? and %s is not null
		) ranked
		where ranked.rank <= ?
		order by ranked.session_id, ranked.rank`, text, from, placeholders, text), args...)
	if err != nil {
		return err
	}
	return collectTexts(rows, texts, CollapseWhitespace)
}

func latestTextsLegacy(db *sql.DB, ids []string, role Role, texts map[string][]string) error {
	placeholders, args := inPlaceholders(ids)
	args = append(args, string(role), promptLimit)
	rows, err := db.Query(fmt.Sprintf(`
		select ranked.session_id, ranked.text
		from (
			select
				m.session_id as session_id,
				p.data as text,
				row_number() over (
					partition by m.session_id
					order by m.time_created desc, p.time_created desc, p.id desc
				) as rank
			from message m
			join part p on p.message_id = m.id
			where m.session_id in (%s)
				and json_extract(m.data, '$.role') = ?
				and json_extract(p.data, '$.type') = 'text'
		) ranked
		where ranked.rank <= ?
		order by ranked.session_id, ranked.rank`, placeholders), args...)
	if err != nil {
		return err
	}
	return collectTexts(rows, texts, parseTextPart)
}

func opencodeSession(row opencodeRow, cached cachedOpencode, scope SearchScope) Session {
	search := [][]string{{row.Title, row.Directory}, cached.User}
	if scope == ScopeAll {
		search = append(search, cached.Assistant)
	}
	return Session{
		ID:             row.ID,
		Title:          row.Title,
		Directory:      row.Directory,
		ProjectID:      row.ProjectID,
		Source:         SourceOpencode,
		UpdatedAtMs:    float64(row.TimeUpdated),
		UpdatedAtLabel: FormatUpdatedAt(float64(row.TimeUpdated)),
		// The query returns newest first; every other source lists a
		// conversation oldest first, so read it the same way.
		Prompts:          truncateAll(reversed(cached.User)),
		AssistantSnippet: truncateAll(reversed(cached.Assistant)),
		SearchText:       joinLines(search...),
	}
}

func reversed(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[len(values)-1-i] = value
	}
	return out
}

func truncateAll(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = truncatePreview(value)
	}
	return out
}

// OpencodeSessions lists the most recent OpenCode sessions. Their latest
// prompts are the expensive part (the query has to look inside every part of
// every message), so they come from the cache unless the session has been
// updated since.
func (cache *Cache) OpencodeSessions(path string, scope SearchScope) ([]Session, error) {
	db, err := openOpencodeDB(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := listOpencodeSessions(db, opencodeSessionLimit)
	if err != nil {
		return nil, err
	}

	previous := cache.last()
	previousStore, reuse := previous.OpencodeStores[path]
	if !reuse && previous.OpencodeDB == path {
		previousStore, reuse = previous.Opencode, true
	}
	next := make(map[string]cachedOpencode, len(rows))
	var stale []string
	for _, row := range rows {
		cached, ok := previousStore[row.ID]
		if reuse && ok && cached.TimeUpdated == row.TimeUpdated {
			next[row.ID] = cached
			continue
		}
		stale = append(stale, row.ID)
	}

	if len(stale) > 0 {
		user, err := latestTexts(db, stale, RoleUser)
		if err != nil {
			return nil, err
		}
		assistant, err := latestTexts(db, stale, RoleAssistant)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, ok := next[row.ID]; !ok {
				next[row.ID] = cachedOpencode{
					TimeUpdated: row.TimeUpdated,
					User:        user[row.ID],
					Assistant:   assistant[row.ID],
				}
			}
		}
	}

	cache.mu.Lock()
	cache.next.OpencodeDB = path
	cache.next.Opencode = next
	cache.next.OpencodeStores[path] = next
	if len(stale) > 0 || !reuse {
		cache.dirty = true
	}
	cache.mu.Unlock()

	sessions := make([]Session, len(rows))
	for i, row := range rows {
		sessions[i] = opencodeSession(row, next[row.ID], scope)
	}
	return sessions, nil
}
