package ocs

import (
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const textLimit = 140

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func ShortenHome(value string) string {
	home := homeDir()
	if home != "" && strings.HasPrefix(value, home) {
		return "~" + value[len(home):]
	}
	return value
}

func CollapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// Truncate counts runes, not bytes, so a multi-byte character is never cut in
// half.
func Truncate(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return strings.TrimRightFunc(string(runes[:max-1]), unicode.IsSpace) + "..."
}

func truncatePreview(value string) string {
	return Truncate(value, textLimit)
}

func FormatUpdatedAt(ms float64) string {
	return time.UnixMilli(int64(ms)).Format("2 Jan 2006, 15:04")
}

// lastN keeps the final n entries, the most recent turns of a transcript.
func lastN(values []string, n int) []string {
	if len(values) > n {
		values = values[len(values)-n:]
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = truncatePreview(value)
	}
	return out
}

func expandHome(value string) string {
	if value == "~" {
		return homeDir()
	}
	if strings.HasPrefix(value, "~/") {
		return homeDir() + value[1:]
	}
	return value
}
