package cli

import "fmt"

// splitWords splits a command string into argv the way a POSIX shell would
// tokenise it (single quotes, double quotes, backslash escapes), without
// performing expansion. Checks are stored as argv and never re-joined, so
// this is the only place a shell-like syntax exists.
func splitWords(s string) ([]string, error) {
	var (
		out     []string
		cur     []rune
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur = append(cur, r)
			escaped = false
			inWord = true
		case r == '\\' && quote != '\'':
			escaped = true
			inWord = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur = append(cur, r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out = append(out, string(cur))
				cur, inWord = cur[:0], false
			}
		default:
			cur = append(cur, r)
			inWord = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape in %q", s)
	}
	if inWord {
		out = append(out, string(cur))
	}
	return out, nil
}
