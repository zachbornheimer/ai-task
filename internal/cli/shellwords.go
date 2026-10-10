package cli

import "fmt"

// splitWords splits a command string into argv the way a POSIX shell would
// tokenise it (single quotes, double quotes, backslash escapes), without
// performing expansion. Checks are stored as argv and never re-joined, so
// this is the only place a shell-like syntax exists.
func splitWords(s string) ([]string, error) {
	words, _, err := splitWordsMarked(s)
	return words, err
}

// splitWordsMarked is splitWords that also reports, per word, whether any
// part of it was quoted or escaped, so a bare operator can be told from a
// quoted one that is just an argument.
func splitWordsMarked(s string) ([]string, []bool, error) {
	var (
		out     []string
		marks   []bool
		cur     []rune
		inWord  bool
		quoted  bool
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
			inWord, quoted = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur = append(cur, r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord, quoted = true, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out, marks = append(out, string(cur)), append(marks, quoted)
				cur, inWord, quoted = cur[:0], false, false
			}
		default:
			cur = append(cur, r)
			inWord = true
		}
	}
	if escaped || quote != 0 {
		return nil, nil, fmt.Errorf("unterminated quote or escape in %q", s)
	}
	if inWord {
		out, marks = append(out, string(cur)), append(marks, quoted)
	}
	return out, marks, nil
}
