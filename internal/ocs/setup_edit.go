package ocs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// JSONC normalization retains byte offsets, so edits splice only the selected
// entry and preserve every unrelated setting and comment.
func cleanJSONC(raw []byte) ([]byte, error) {
	clean := append([]byte(nil), raw...)
	inString, escape := false, false
	for i := 0; i < len(clean); i++ {
		if inString {
			if escape {
				escape = false
			} else if clean[i] == '\\' {
				escape = true
			} else if clean[i] == '"' {
				inString = false
			}
			continue
		}
		if clean[i] == '"' {
			inString = true
			continue
		}
		if clean[i] != '/' || i+1 >= len(clean) {
			continue
		}
		if clean[i+1] == '/' {
			for i < len(clean) && clean[i] != '\n' {
				clean[i] = ' '
				i++
			}
		} else if clean[i+1] == '*' {
			clean[i], clean[i+1] = ' ', ' '
			i += 2
			for i+1 < len(clean) && !(clean[i] == '*' && clean[i+1] == '/') {
				if clean[i] != '\n' && clean[i] != '\r' {
					clean[i] = ' '
				}
				i++
			}
			if i+1 >= len(clean) {
				return nil, fmt.Errorf("unterminated JSONC comment")
			}
			clean[i], clean[i+1] = ' ', ' '
			i++
		}
	}
	return clean, nil
}

func normalizedJSON(raw []byte) ([]byte, error) {
	clean, err := cleanJSONC(raw)
	if err != nil {
		return nil, err
	}
	inString, escape := false, false
	for i, char := range clean {
		if inString {
			if escape {
				escape = false
			} else if char == '\\' {
				escape = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
		}
		if char == ',' {
			j := i + 1
			for j < len(clean) && strings.ContainsRune(" \t\r\n", rune(clean[j])) {
				j++
			}
			if j < len(clean) && (clean[j] == '}' || clean[j] == ']') {
				clean[i] = ' '
			}
		}
	}
	return clean, nil
}

type jsonMember struct {
	name                           string
	keyStart, valueStart, valueEnd int
}

func jsonMembers(raw []byte) ([]jsonMember, int, error) {
	normal, err := normalizedJSON(raw)
	if err != nil {
		return nil, 0, err
	}
	if !json.Valid(normal) {
		return nil, 0, fmt.Errorf("invalid JSON/JSONC")
	}
	decoder := json.NewDecoder(bytes.NewReader(normal))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, 0, fmt.Errorf("expected JSON object")
	}
	var members []jsonMember
	seen := map[string]bool{}
	for decoder.More() {
		start := int(decoder.InputOffset())
		for start < len(normal) && (strings.ContainsRune(" \t\r\n", rune(normal[start])) || normal[start] == ',') {
			start++
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, 0, fmt.Errorf("invalid JSON key")
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return nil, 0, fmt.Errorf("duplicate or invalid JSON key")
		}
		seen[name] = true
		valueStart := int(decoder.InputOffset())
		for valueStart < len(normal) && (strings.ContainsRune(" \t\r\n", rune(normal[valueStart])) || normal[valueStart] == ':') {
			valueStart++
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, 0, fmt.Errorf("invalid JSON value")
		}
		members = append(members, jsonMember{name, start, valueStart, int(decoder.InputOffset())})
	}
	close := int(decoder.InputOffset())
	for close < len(normal) && strings.ContainsRune(" \t\r\n", rune(normal[close])) {
		close++
	}
	return members, close, nil
}

func splice(raw []byte, start, end int, value []byte) []byte {
	result := make([]byte, 0, len(raw)-(end-start)+len(value))
	result = append(result, raw[:start]...)
	result = append(result, value...)
	return append(result, raw[end:]...)
}

// A nil value deletes a member. Recursing into only the requested path avoids
// reserializing account metadata, permissions, or other servers.
func setJSONPath(raw []byte, path []string, value []byte) ([]byte, error) {
	members, close, err := jsonMembers(raw)
	if err != nil {
		return nil, err
	}
	for index, member := range members {
		if member.name != path[0] {
			continue
		}
		if len(path) > 1 {
			child, err := setJSONPath(raw[member.valueStart:member.valueEnd], path[1:], value)
			if err != nil {
				return nil, err
			}
			return splice(raw, member.valueStart, member.valueEnd, child), nil
		}
		if value != nil {
			return splice(raw, member.valueStart, member.valueEnd, value), nil
		}
		clean, _ := cleanJSONC(raw)
		// Remove one delimiter separately, leaving intervening comments intact.
		comma := -1
		end := close
		if index+1 < len(members) {
			end = members[index+1].keyStart
		}
		if offset := bytes.IndexByte(clean[member.valueEnd:end], ','); offset >= 0 {
			comma = member.valueEnd + offset
		}
		if comma < 0 && index > 0 {
			if offset := bytes.IndexByte(clean[members[index-1].valueEnd:member.keyStart], ','); offset >= 0 {
				comma = members[index-1].valueEnd + offset
			}
		}
		result := append([]byte(nil), raw...)
		if comma >= 0 {
			result[comma] = ' '
		}
		return splice(result, member.keyStart, member.valueEnd, nil), nil
	}
	if value == nil {
		return raw, nil
	}
	if len(path) > 1 {
		value, err = setJSONPath([]byte("{}"), path[1:], value)
		if err != nil {
			return nil, err
		}
	}
	key, _ := json.Marshal(path[0])
	prefix := ""
	if len(members) > 0 {
		clean, _ := cleanJSONC(raw)
		if !bytes.Contains(clean[members[len(members)-1].valueEnd:close], []byte(",")) {
			prefix = ","
		}
	}
	addition := append([]byte(prefix+"\n  "+string(key)+": "), value...)
	addition = append(addition, '\n')
	return splice(raw, close, close, addition), nil
}

func nativeRoot(raw []byte, tool Source) (map[string]any, error) {
	root := make(map[string]any)
	if tool == SourceCodex {
		if err := toml.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("invalid TOML configuration; refusing to edit")
		}
	} else {
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = []byte("{}")
		}
		normal, err := normalizedJSON(raw)
		if err != nil {
			return nil, err
		}
		if _, _, err := jsonMembers(raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(normal, &root); err != nil || root == nil {
			return nil, fmt.Errorf("invalid JSON configuration; refusing to edit")
		}
	}
	return root, nil
}

func nativeMCPKey(tool Source) string {
	if tool == SourceCodex {
		return "mcp_servers"
	}
	if tool == SourceOpencode {
		return "mcp"
	}
	return "mcpServers"
}

// Matching requested fields leaves an equivalent manual installation in place,
// including its extra native settings. Explicitly disabled entries do not
// satisfy a profile that requests an enabled server.
func nativeMCPMatches(current any, desired map[string]any, tool Source) bool {
	entry, ok := current.(map[string]any)
	if !ok || entry["enabled"] == false {
		return false
	}
	if desired["url"] != nil && entry["command"] != nil {
		return false
	}
	if desired["command"] != nil && entry["url"] != nil {
		return false
	}
	if desired["command"] != nil && tool == SourceClaude && entry["type"] != nil && entry["type"] != "stdio" {
		return false
	}
	for key, want := range desired {
		actual, exists := entry[key]
		if !exists && key == "enabled" && want == true {
			continue
		}
		if !exists && key == "args" && fingerprint(want) == fingerprint([]string{}) {
			continue
		}
		if key == "type" && tool == SourceClaude && actual == "streamable-http" && want == "http" {
			continue
		}
		if !exists || fingerprint(actual) != fingerprint(want) {
			return false
		}
	}
	return true
}

func nativeEntries(raw []byte, tool Source) (map[string]any, error) {
	root, err := nativeRoot(raw, tool)
	if err != nil {
		return nil, err
	}
	value, exists := root[nativeMCPKey(tool)]
	if !exists {
		return make(map[string]any), nil
	}
	entries, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("MCP configuration must be an object/table")
	}
	if tool != SourceCodex {
		members, _, _ := jsonMembers(raw)
		for _, member := range members {
			if member.name == nativeMCPKey(tool) {
				if _, _, err := jsonMembers(raw[member.valueStart:member.valueEnd]); err != nil {
					return nil, err
				}
			}
		}
	}
	return entries, nil
}

func editNativeMCP(raw []byte, tool Source, name string, value map[string]any) ([]byte, error) {
	before, err := nativeRoot(raw, tool)
	if err != nil {
		return nil, err
	}
	key := nativeMCPKey(tool)
	entries, err := nativeEntries(raw, tool)
	if err != nil {
		return nil, err
	}
	_, exists := entries[name]
	var result []byte
	if tool != SourceCodex {
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = []byte("{}\n")
		}
		var encoded []byte
		if value != nil {
			encoded, _ = json.MarshalIndent(value, "  ", "  ")
		}
		result, err = setJSONPath(raw, []string{key, name}, encoded)
	} else {
		// Owned TOML entries are bounded blocks. Other TOML syntax, including
		// multiline strings and comments, is never searched as table headers.
		begin, end := "# AgentBridge MCP begin: "+name+"\n", "# AgentBridge MCP end: "+name+"\n"
		start := bytes.Index(raw, []byte(begin))
		// Blocks written before the rename remain owned. The next update rewrites
		// their markers with the AgentBridge name; removal works as usual.
		if start < 0 {
			begin, end = "# ocs MCP begin: "+name+"\n", "# ocs MCP end: "+name+"\n"
			start = bytes.Index(raw, []byte(begin))
		}
		stop := -1
		if start >= 0 {
			if offset := bytes.Index(raw[start+len(begin):], []byte(end)); offset >= 0 {
				stop = start + len(begin) + offset + len(end)
			}
		}
		if exists && (start < 0 || stop < 0) {
			return nil, fmt.Errorf("owned MCP block markers missing; restore the entry before syncing")
		}
		var block []byte
		if value != nil {
			encoded, marshalErr := toml.Marshal(map[string]any{key: map[string]any{name: value}})
			if marshalErr != nil {
				return nil, fmt.Errorf("cannot encode MCP entry")
			}
			encoded = bytes.TrimPrefix(encoded, []byte("[mcp_servers]\n"))
			block = []byte("# AgentBridge MCP begin: " + name + "\n" + string(encoded) + "# AgentBridge MCP end: " + name + "\n")
		}
		if start >= 0 && stop >= 0 {
			result = splice(raw, start, stop, block)
		} else if value != nil {
			result = append(append(append([]byte(nil), raw...), '\n'), block...)
		} else {
			result = raw
		}
	}
	if err != nil {
		return nil, err
	}
	if value == nil {
		delete(entries, name)
	} else {
		entries[name] = value
	}
	// Verify the complete semantic result, catching inline-table restrictions,
	// misplaced markers, and any edit that changes unrelated configuration.
	before[key] = entries
	after, err := nativeRoot(result, tool)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 && after[key] == nil {
		delete(before, key)
	}
	if fingerprint(before) != fingerprint(after) {
		return nil, fmt.Errorf("configuration layout cannot be edited without changing unrelated fields")
	}
	return result, nil
}
