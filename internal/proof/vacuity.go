package proof

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// noOpCommands are commands that cannot fail, so they prove nothing.
var noOpCommands = map[string]bool{"true": true, ":": true, "echo": true}

var (
	exitZero  = regexp.MustCompile(`^exit(\s+0)?$`)
	runFilter = regexp.MustCompile(`(^|\s)-{1,2}(test\.)?run([=\s]|$)`)
	goTestCmd = regexp.MustCompile(`(^|\s)go\s+test(\s|$)`)
)

// Vacuity returns INVALID_INPUT, naming the rule, when spec cannot prove
// anything: a no-op, an unbounded small go_test, an encoded payload, or a
// run subject over maxRunArgLen. It owns the rule for add, update, and add -f.
func Vacuity(spec ProofSpec) error {
	switch spec.Preset {
	case "run":
		return runVacuity(spec)
	case "go_test":
		if spec.Size == SizeSmall && spec.Options["name"] == "" {
			return unboundedGoTest(spec.Subject)
		}
	}
	return nil
}

func runVacuity(spec ProofSpec) error {
	subject := strings.TrimSpace(spec.Subject)
	switch {
	case encodedPayload.MatchString(subject):
		return fault.New(fault.CodeInvalidInput, "encoded payload: run %.40q embeds encoded or evaluated code (base64, exec(, eval)", subject)
	case len(subject) > maxRunArgLen:
		return fault.New(fault.CodeInvalidInput, "too long: run %.40q... is %d chars, limit %d", subject, len(subject), maxRunArgLen)
	case isNoOp(subject):
		return fault.New(fault.CodeInvalidInput, "no-op: run %q cannot fail, so it proves nothing; run a command that checks this outcome", subject)
	case spec.Size == SizeSmall && goTestCmd.MatchString(subject) && !runFilter.MatchString(subject):
		return unboundedGoTest(subject)
	}
	return nil
}

func isNoOp(subject string) bool {
	if exitZero.MatchString(subject) {
		return true
	}
	if strings.ContainsAny(subject, shellSyntax) || strings.Contains(subject, "&") {
		return false
	}
	first, _, _ := strings.Cut(subject, " ")
	return noOpCommands[filepath.Base(first)]
}

func unboundedGoTest(subject string) error {
	return fault.New(fault.CodeInvalidInput, "unbounded go_test: %q has no name= (-run) filter; a small proof must finish within %s. Narrow it so it proves only this outcome, or set size=medium|large", subject, SizeSmall.Budget())
}
