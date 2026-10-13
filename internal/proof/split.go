package proof

import (
	"fmt"
	"strings"
)

// token is one shell-style word and the byte range it occupied in the input.
type token struct {
	text       string
	start, end int
}

// splitWords splits s like a POSIX shell without expansion: single quotes are
// literal, double quotes and bare words honor backslash escapes.
func splitWords(s string) ([]token, error) {
	var toks []token
	var cur strings.Builder
	start, inWord := 0, false
	var quote byte
	flush := func(end int) {
		if inWord {
			toks = append(toks, token{cur.String(), start, end})
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case quote == '"':
			if c == '"' {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			if !inWord {
				start, inWord = i, true
			}
			quote = c
		case c == ' ' || c == '\t':
			flush(i)
		default:
			if !inWord {
				start, inWord = i, true
			}
			cur.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in %q", quote, s)
	}
	flush(len(s))
	return toks, nil
}
