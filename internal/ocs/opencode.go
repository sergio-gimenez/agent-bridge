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

func listOpencodeSessions(db *sql.DB, limit int) ([]opencodeRow, error) {
	rows, err := db.Query(`
		select id, title, directory, project_id, time_updated
		from session
		where time_archived is null
		order by time_updated desc
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

// latestTexts returns, per session, the newest text parts written in the given
// role, newest first.
func latestTexts(db *sql.DB, ids []string, role Role) (map[string][]string, error) {
	texts := map[string][]string{}
	if len(ids) == 0 {
		return texts, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
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
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if text := parseTextPart(raw); text != "" {
			texts[id] = append(texts[id], text)
		}
	}
	return texts, rows.Err()
}

func opencodeSession(row opencodeRow, cached cachedOpencode, scope SearchScope) Session {
	search := [][]string{{row.Title, row.Directory}, cached.User}
	if scope == ScopeAll {
		search = append(search, cached.Assistant)
	}
	return Session{
		ID:               row.ID,
		Title:            row.Title,
		Directory:        row.Directory,
		ProjectID:        row.ProjectID,
		Source:           SourceOpencode,
		UpdatedAtMs:      float64(row.TimeUpdated),
		UpdatedAtLabel:   FormatUpdatedAt(float64(row.TimeUpdated)),
		Prompts:          truncateAll(cached.User),
		AssistantSnippet: truncateAll(cached.Assistant),
		SearchText:       joinLines(search...),
	}
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
	reuse := previous.OpencodeDB == path
	next := make(map[string]cachedOpencode, len(rows))
	var stale []string
	for _, row := range rows {
		cached, ok := previous.Opencode[row.ID]
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
