package ocs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

const promptLimit = 3

// record is what a transcript file boils down to, independent of which account
// reads it or how broadly it is searched.
type record struct {
	ID        string
	Title     string
	Directory string
	User      []string
	Assistant []string
}

// parseState is a transcript reader stopped at a line boundary. Claude Code and
// Codex only ever append to a transcript, so the cache keeps this and resumes
// from Offset when a file grows instead of re-reading all of it: the session
// you are working in right now is the one that changed, and can be the
// biggest.
type parseState struct {
	// Bytes consumed, always just past a newline.
	Offset int64
	// The file's first bytes and the bytes just before Offset, checked on
	// resume to catch a file that was rewritten rather than appended to.
	Head []byte
	Tail []byte

	ID            string
	HaveID        bool
	Title         string
	Directory     string
	HaveDirectory bool

	// Claude keeps prompts and replies directly.
	User      []string
	Assistant []string

	// Codex records each turn twice, as a structured item and as a UI event;
	// only one list is used. Events are dropped once any item turns up.
	Items  []Turn
	Events []Turn
}

type transcriptFormat struct {
	feed   func(*parseState, []byte)
	record func(*parseState) *record
}

// clone copies the state so feeding the copy can never write into slices the
// original still owns.
func (state *parseState) clone() *parseState {
	copied := *state
	copied.Head = append([]byte(nil), state.Head...)
	copied.Tail = append([]byte(nil), state.Tail...)
	copied.User = state.User[:len(state.User):len(state.User)]
	copied.Assistant = state.Assistant[:len(state.Assistant):len(state.Assistant)]
	copied.Items = state.Items[:len(state.Items):len(state.Items)]
	copied.Events = state.Events[:len(state.Events):len(state.Events)]
	return &copied
}

const tailSize = 64

// resume feeds everything after state.Offset. Complete lines advance the saved
// state; a final line with no newline yet (a write in progress) still counts
// toward the returned record, but is read again next time rather than saved.
func resume(reader io.Reader, state *parseState, format transcriptFormat) (*parseState, *record, error) {
	buffered := bufio.NewReaderSize(reader, 256*1024)
	var recent []byte

	for {
		line, err := readLine(buffered)
		complete := len(line) > 0 && line[len(line)-1] == '\n'

		if complete {
			format.feed(state, line)
			state.Offset += int64(len(line))
			recent = append(recent, line...)
			if len(recent) > 4*tailSize {
				recent = append(recent[:0], recent[len(recent)-tailSize:]...)
			}
		} else if len(bytes.TrimSpace(line)) > 0 {
			partial := state.clone()
			format.feed(partial, line)
			state.Tail = keepTail(state.Tail, recent)
			return state, format.record(partial), nil
		}

		if err == io.EOF {
			state.Tail = keepTail(state.Tail, recent)
			return state, format.record(state), nil
		}
		if err != nil {
			return nil, nil, err
		}
	}
}

func keepTail(previous, recent []byte) []byte {
	joined := append(append([]byte(nil), previous...), recent...)
	if len(joined) > tailSize {
		joined = joined[len(joined)-tailSize:]
	}
	return joined
}

// readLine reads through the next newline however long the line is: a line
// can be a whole tool output.
func readLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if err != bufio.ErrBufferFull {
		return line, err
	}
	long := append([]byte(nil), line...)
	for err == bufio.ErrBufferFull {
		line, err = reader.ReadSlice('\n')
		long = append(long, line...)
	}
	return long, err
}

// eachLine yields every non-blank line, the last one whether or not it ends in
// a newline.
func eachLine(reader io.Reader, visit func(line []byte)) error {
	buffered := bufio.NewReaderSize(reader, 256*1024)
	for {
		line, err := readLine(buffered)
		if len(bytes.TrimSpace(line)) > 0 {
			visit(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// parseAll reads a whole transcript, the last line included whether or not it
// ends in a newline yet.
func parseAll(reader io.Reader, format transcriptFormat) (*parseState, error) {
	state := &parseState{}
	err := eachLine(reader, func(line []byte) { format.feed(state, line) })
	return state, err
}

type contentPart struct {
	Type string          `json:"type"`
	Text json.RawMessage `json:"text"`
}

func rawString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// extractText reads a message body that is either a bare string or a list of
// typed parts, keeping only the parts whose type carries prose.
func extractText(raw json.RawMessage, textTypes map[string]bool) string {
	raw = bytes.TrimSpace(raw)
	if value, ok := rawString(raw); ok {
		return CollapseWhitespace(value)
	}
	if len(raw) == 0 || raw[0] != '[' {
		return ""
	}

	var parts []*contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}

	var texts []string
	for _, part := range parts {
		if part == nil || !textTypes[part.Type] {
			continue
		}
		if text, ok := rawString(part.Text); ok {
			texts = append(texts, text)
		}
	}
	return CollapseWhitespace(joinSpace(texts))
}

func joinSpace(values []string) string {
	var buffer bytes.Buffer
	for i, value := range values {
		if i > 0 {
			buffer.WriteByte(' ')
		}
		buffer.WriteString(value)
	}
	return buffer.String()
}

func joinLines(values ...[]string) string {
	var buffer bytes.Buffer
	first := true
	for _, group := range values {
		for _, value := range group {
			if !first {
				buffer.WriteByte('\n')
			}
			first = false
			buffer.WriteString(value)
		}
	}
	return buffer.String()
}

func accountName(account *Account) string {
	if account == nil {
		return ""
	}
	return account.Name
}
