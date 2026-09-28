package ocs

import (
	"time"
	"unicode/utf8"
)

// escapeWait is how long a trailing ESC waits for the rest of its sequence
// before it counts as the Escape key.
const escapeWait = 40 * time.Millisecond

// incompleteEscape returns where a trailing, unfinished escape sequence starts
// in data, or -1 when data ends cleanly.
func incompleteEscape(data []byte) int {
	for i := len(data) - 1; i >= 0 && i >= len(data)-16; i-- {
		if data[i] != 0x1b {
			continue
		}
		rest := data[i+1:]
		switch {
		case len(rest) == 0:
			return i
		case rest[0] == 'O' && len(rest) == 1:
			return i
		case rest[0] == '[':
			for _, b := range rest[1:] {
				if b >= 0x40 && b <= 0x7e {
					return -1
				}
			}
			return i
		}
		return -1
	}
	return -1
}

// Key is one decoded keypress: a named key, possibly with Ctrl held, or text to
// append to the query.
type Key struct {
	Name string
	Ctrl bool
	Text string
}

var csiNames = map[string]string{
	"A": "up", "B": "down", "C": "right", "D": "left",
	"H": "home", "F": "end", "Z": "backtab",
	"1~": "home", "7~": "home", "4~": "end", "8~": "end",
	"5~": "pageup", "6~": "pagedown", "3~": "delete",
}

var ss3Names = map[byte]string{'A': "up", 'B': "down", 'C': "right", 'D': "left", 'H': "home", 'F': "end"}

// DecodeKeys splits one read from the terminal into keypresses. A terminal
// writes an escape sequence in one go, so a lone ESC at the end of a read is
// the Escape key itself rather than the start of a sequence still to come.
func DecodeKeys(input []byte) []Key {
	var keys []Key
	for i := 0; i < len(input); {
		b := input[i]

		switch {
		case b == 0x1b:
			if i+1 >= len(input) {
				keys = append(keys, Key{Name: "escape"})
				i++
				continue
			}
			switch input[i+1] {
			case '[':
				// CSI: parameter bytes, then one final byte in 0x40..0x7e.
				j := i + 2
				for j < len(input) && (input[j] < 0x40 || input[j] > 0x7e) {
					j++
				}
				if j >= len(input) {
					return keys
				}
				body := string(input[i+2 : j+1])
				name, ok := csiNames[body]
				if !ok {
					// Modified keys ("1;5A") keep their final byte's meaning.
					name = csiNames[string(input[j])]
					if input[j] == '~' {
						name = ""
					}
				}
				if name != "" {
					keys = append(keys, Key{Name: name})
				}
				i = j + 1
			case 'O':
				if i+2 < len(input) {
					if name, ok := ss3Names[input[i+2]]; ok {
						keys = append(keys, Key{Name: name})
					}
				}
				i += 3
			default:
				// Alt+key: never part of the query.
				_, size := utf8.DecodeRune(input[i+1:])
				i += 1 + size
			}
		case b == '\r' || b == '\n':
			keys = append(keys, Key{Name: "enter"})
			i++
		case b == '\t':
			keys = append(keys, Key{Name: "tab"})
			i++
		case b == 0x7f || b == 0x08:
			keys = append(keys, Key{Name: "backspace"})
			i++
		case b < 0x20:
			keys = append(keys, Key{Name: string(rune(b + 0x60)), Ctrl: true})
			i++
		default:
			r, size := utf8.DecodeRune(input[i:])
			if r != utf8.RuneError {
				keys = append(keys, Key{Text: string(r)})
			}
			i += size
		}
	}
	return keys
}
