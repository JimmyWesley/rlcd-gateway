package setup

// A small structural TOML scanner, enough to edit ~/.codex/config.toml
// without a dependency. It does not build values; it finds statements
// (table headers and key/value pairs) with their line spans and table
// path, and rejects input that is not well-formed at that level
// (unterminated strings, unbalanced arrays, junk after a value, ...), so we
// never rewrite a file we do not understand.

import (
	"fmt"
	"strconv"
	"strings"
)

type tomlStmt struct {
	header    bool     // a [table] or [[array]] header
	path      []string // header: the table path; key/value: the table it is in
	key       []string // key/value: the (dotted) key
	value     string   // key/value: the raw value text
	startLine int      // 0-based, inclusive
	endLine   int
}

// full returns the absolute key path of a key/value statement.
func (s tomlStmt) full() []string { return append(append([]string{}, s.path...), s.key...) }

type tomlScanner struct {
	src  string
	pos  int
	line int
}

func (t *tomlScanner) errf(format string, a ...any) error {
	return fmt.Errorf("line %d: %s", t.line+1, fmt.Sprintf(format, a...))
}

func (t *tomlScanner) eof() bool { return t.pos >= len(t.src) }
func (t *tomlScanner) peek() byte {
	if t.eof() {
		return 0
	}
	return t.src[t.pos]
}
func (t *tomlScanner) has(s string) bool { return strings.HasPrefix(t.src[t.pos:], s) }
func (t *tomlScanner) next() byte {
	c := t.src[t.pos]
	t.pos++
	if c == '\n' {
		t.line++
	}
	return c
}

func (t *tomlScanner) skipSpace() {
	for !t.eof() && (t.peek() == ' ' || t.peek() == '\t') {
		t.pos++
	}
}

func (t *tomlScanner) skipComment() {
	if t.peek() == '#' {
		for !t.eof() && t.peek() != '\n' {
			t.pos++
		}
	}
}

// endOfLine accepts trailing whitespace and a comment, then a newline or EOF.
func (t *tomlScanner) endOfLine() error {
	t.skipSpace()
	t.skipComment()
	if t.has("\r\n") {
		t.pos++
	}
	if t.eof() {
		return nil
	}
	if t.peek() != '\n' {
		return t.errf("unexpected %q", t.peek())
	}
	t.next()
	return nil
}

// skipBlank skips whitespace, newlines and comments (inside arrays).
func (t *tomlScanner) skipBlank() {
	for !t.eof() {
		switch t.peek() {
		case ' ', '\t', '\r', '\n':
			t.next()
		case '#':
			t.skipComment()
		default:
			return
		}
	}
}

func parseTOML(src string) ([]tomlStmt, error) {
	t := &tomlScanner{src: src}
	var out []tomlStmt
	var table []string
	for {
		t.skipBlank()
		if t.eof() {
			return out, nil
		}
		start := t.line
		if t.peek() == '[' {
			t.next()
			array := t.peek() == '['
			if array {
				t.next()
			}
			t.skipSpace()
			path, err := t.key()
			if err != nil {
				return nil, err
			}
			t.skipSpace()
			closing := "]"
			if array {
				closing = "]]"
			}
			if !t.has(closing) {
				return nil, t.errf("unterminated table header")
			}
			t.pos += len(closing)
			if err := t.endOfLine(); err != nil {
				return nil, err
			}
			table = path
			out = append(out, tomlStmt{header: true, path: path, startLine: start, endLine: start})
			continue
		}
		key, err := t.key()
		if err != nil {
			return nil, err
		}
		t.skipSpace()
		if t.peek() != '=' {
			return nil, t.errf("expected '=' after key")
		}
		t.next()
		t.skipSpace()
		vstart := t.pos
		if err := t.value(); err != nil {
			return nil, err
		}
		value := strings.TrimRight(t.src[vstart:t.pos], " \t")
		end := t.line
		if err := t.endOfLine(); err != nil {
			return nil, err
		}
		out = append(out, tomlStmt{path: table, key: key, value: value, startLine: start, endLine: end})
	}
}

// key reads a dotted key of bare or quoted parts.
func (t *tomlScanner) key() ([]string, error) {
	var parts []string
	for {
		t.skipSpace()
		switch c := t.peek(); {
		case c == '"' || c == '\'':
			s, err := t.shortString()
			if err != nil {
				return nil, err
			}
			v, err := unquoteTOML(s)
			if err != nil {
				return nil, t.errf("bad quoted key: %v", err)
			}
			parts = append(parts, v)
		case isBare(c):
			st := t.pos
			for !t.eof() && isBare(t.peek()) {
				t.pos++
			}
			parts = append(parts, t.src[st:t.pos])
		default:
			return nil, t.errf("expected a key")
		}
		t.skipSpace()
		if t.peek() != '.' {
			return parts, nil
		}
		t.next()
	}
}

func isBare(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// shortString reads a single-line basic or literal string, quotes included.
func (t *tomlScanner) shortString() (string, error) {
	st := t.pos
	q := t.next()
	for {
		if t.eof() || t.peek() == '\n' {
			return "", t.errf("unterminated string")
		}
		c := t.next()
		if c == '\\' && q == '"' {
			if t.eof() {
				return "", t.errf("unterminated string")
			}
			t.next()
			continue
		}
		if c == q {
			return t.src[st:t.pos], nil
		}
	}
}

func (t *tomlScanner) value() error {
	switch c := t.peek(); {
	case t.has(`"""`) || t.has(`'''`):
		q := t.src[t.pos : t.pos+3]
		t.pos += 3
		for {
			if t.eof() {
				return t.errf("unterminated multi-line string")
			}
			if q == `"""` && t.peek() == '\\' {
				t.next()
				if !t.eof() {
					t.next()
				}
				continue
			}
			if t.has(q) {
				t.pos += 3
				// Up to two more quotes may belong to the content.
				for i := 0; i < 2 && t.peek() == q[0]; i++ {
					t.pos++
				}
				return nil
			}
			t.next()
		}
	case c == '"' || c == '\'':
		_, err := t.shortString()
		return err
	case c == '[':
		t.next()
		for {
			t.skipBlank()
			if t.peek() == ']' {
				t.next()
				return nil
			}
			if err := t.value(); err != nil {
				return err
			}
			t.skipBlank()
			switch t.peek() {
			case ',':
				t.next()
			case ']':
				t.next()
				return nil
			default:
				return t.errf("expected ',' or ']' in array")
			}
		}
	case c == '{':
		t.next()
		for {
			t.skipBlank()
			if t.peek() == '}' {
				t.next()
				return nil
			}
			if _, err := t.key(); err != nil {
				return err
			}
			t.skipSpace()
			if t.peek() != '=' {
				return t.errf("expected '=' in inline table")
			}
			t.next()
			t.skipSpace()
			if err := t.value(); err != nil {
				return err
			}
			t.skipBlank()
			switch t.peek() {
			case ',':
				t.next()
			case '}':
				t.next()
				return nil
			default:
				return t.errf("expected ',' or '}' in inline table")
			}
		}
	default:
		// Scalars: booleans, numbers, dates. Dates may contain one space.
		st := t.pos
		for !t.eof() && !strings.ContainsRune(",]}#\r\n", rune(t.peek())) {
			t.pos++
		}
		v := strings.TrimRight(t.src[st:t.pos], " \t")
		t.pos = st + len(v)
		if v == "" {
			return t.errf("missing value")
		}
		for _, r := range v {
			if !(isBare(byte(r)) || strings.ContainsRune("+.: ", r)) || r > 127 {
				return t.errf("invalid value %q", v)
			}
		}
		return nil
	}
}

// unquoteTOML decodes a single-line string value; ok=false for anything else.
func unquoteTOML(s string) (string, error) {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1], nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' && !strings.HasPrefix(s, `"""`) {
		return strconv.Unquote(s)
	}
	return "", fmt.Errorf("not a single-line string: %s", s)
}

func pathEq(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasPrefix(a []string, p ...string) bool { return len(a) >= len(p) && pathEq(a[:len(p)], p...) }
