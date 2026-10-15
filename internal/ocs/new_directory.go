package ocs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dirSuggestion is one directory the new conversation dialog offers: a project
// you already have sessions in, or a subdirectory of the path being typed.
type dirSuggestion struct {
	path     string
	sessions int
	// UpdatedAtMs of the newest session there, 0 for a plain subdirectory.
	updatedMs float64
}

// recentProjects is every directory with a local session that still exists,
// newest first.
func (p *picker) recentProjects() []dirSuggestion {
	byPath := map[string]*dirSuggestion{}
	var order []*dirSuggestion
	for i := range p.sessions {
		session := &p.sessions[i]
		if session.Directory == "" {
			continue
		}
		entry := byPath[session.Directory]
		if entry == nil {
			entry = &dirSuggestion{path: session.Directory}
			byPath[session.Directory] = entry
			order = append(order, entry)
		}
		entry.sessions++
		entry.updatedMs = max(entry.updatedMs, session.UpdatedAtMs)
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].updatedMs > order[j].updatedMs })
	var projects []dirSuggestion
	for _, entry := range order {
		if info, err := os.Stat(entry.path); err == nil && info.IsDir() {
			projects = append(projects, *entry)
		}
	}
	return projects
}

// suggestDirectories filters the recent projects by what was typed. A typed
// path ("~/…", "/…", "./…") also lists the subdirectories it could complete to,
// so any directory is reachable, not only the ones with sessions.
func suggestDirectories(projects []dirSuggestion, query string) []dirSuggestion {
	query = strings.TrimSpace(query)
	if query == "" {
		return projects
	}
	var out []dirSuggestion
	seen := map[string]bool{}
	add := func(entry dirSuggestion) {
		if !seen[entry.path] {
			seen[entry.path] = true
			out = append(out, entry)
		}
	}
	isPath := strings.HasPrefix(query, "/") || strings.HasPrefix(query, "~") || strings.HasPrefix(query, ".")
	if isPath {
		for _, path := range completeDirectory(query) {
			entry := dirSuggestion{path: path}
			for _, project := range projects {
				if project.path == path {
					entry = project
				}
			}
			add(entry)
		}
	}
	words := strings.Fields(strings.ToLower(query))
	for _, project := range projects {
		shown := strings.ToLower(ShortenHome(project.path))
		matches := true
		for _, word := range words {
			if !strings.Contains(shown, word) && !strings.Contains(strings.ToLower(project.path), word) {
				matches = false
				break
			}
		}
		if matches {
			add(project)
		}
	}
	return out
}

// completeDirectory is the typed path itself when it is a directory, then the
// subdirectories of its parent that start with its last element. Hidden ones
// only show once a "." is typed.
func completeDirectory(typed string) []string {
	expanded := expandHome(typed)
	if !filepath.IsAbs(expanded) {
		if absolute, err := filepath.Abs(expanded); err == nil {
			if strings.HasSuffix(typed, "/") {
				absolute += "/"
			}
			expanded = absolute
		}
	}
	parent, prefix := expanded, ""
	if !strings.HasSuffix(expanded, "/") {
		parent, prefix = filepath.Dir(expanded), filepath.Base(expanded)
	}
	var out []string
	if info, err := os.Stat(expanded); err == nil && info.IsDir() && prefix != "" {
		out = append(out, filepath.Clean(expanded))
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return out
	}
	lower := strings.ToLower(prefix)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), lower) {
			continue
		}
		path := filepath.Join(parent, name)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			continue
		}
		if name == prefix {
			continue // already listed as the typed path itself
		}
		out = append(out, path)
		if len(out) >= 50 {
			break
		}
	}
	return out
}
