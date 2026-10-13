package proof

import (
	"regexp"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// maxRunArgLen caps one argument of a raw command; longer ones smuggle code.
const maxRunArgLen = 300

const shellSyntax = "|;>$"

var (
	encodedPayload = regexp.MustCompile(`base64|exec\(|eval `)
	_              = Preset(runPreset{})
)

// runPreset runs a raw command: argv from the subject, or sh -c when the
// subject uses shell syntax.
type runPreset struct{}

func (runPreset) Name() string      { return "run" }
func (runPreset) Options() []string { return []string{"expect"} }
func (runPreset) DefaultSize() Size { return SizeSmall }

func (runPreset) Argv(spec ProofSpec, _ Env) ([]string, error) {
	if strings.TrimSpace(spec.Subject) == "" {
		return nil, fault.New(fault.CodeInvalidInput, "run needs a command")
	}
	if encodedPayload.MatchString(spec.Subject) {
		return nil, fault.New(fault.CodeInvalidInput, "run %q embeds encoded or evaluated code (base64, exec(, eval)", spec.Subject)
	}
	if strings.ContainsAny(spec.Subject, shellSyntax) || strings.Contains(spec.Subject, "&&") {
		return checked([]string{"sh", "-c", spec.Subject}, spec.Subject)
	}
	toks, err := splitWords(spec.Subject)
	if err != nil {
		return nil, fault.Wrap(err, fault.CodeInvalidInput, "run %q", spec.Subject)
	}
	argv := make([]string, len(toks))
	for i, t := range toks {
		argv[i] = t.text
	}
	return checked(argv, spec.Subject)
}

func checked(argv []string, subject string) ([]string, error) {
	for _, a := range argv {
		if len(a) > maxRunArgLen {
			return nil, fault.New(fault.CodeInvalidInput, "run %.40q...: argument is %d chars, limit %d", subject, len(a), maxRunArgLen)
		}
	}
	return argv, nil
}
