package proof

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

var (
	presetName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	optionPair = regexp.MustCompile(`^([a-z][a-z0-9_]*)=(.*)$`)
)

// Parse reads "PRESET: SUBJECT key=value ..." against the default registry.
func Parse(s string) (ProofSpec, error) { return DefaultRegistry().Parse(s) }

// Parse reads "PRESET: SUBJECT key=value ...". A bare registered-or-future
// name ("ci") has an empty subject. The colon form needs a registered preset,
// which is what rejects the legacy "id: shell command" form. Trailing
// key=value words become options only when the preset declares that key.
func (r *Registry) Parse(s string) (ProofSpec, error) {
	s = strings.TrimSpace(s)
	name, rest, hasColon := strings.Cut(s, ":")
	name = strings.TrimSpace(name)
	if !presetName.MatchString(name) {
		return ProofSpec{}, fault.New(fault.CodeInvalidInput, "proof %q: preset name must match [a-z][a-z0-9_]*", s)
	}
	spec := ProofSpec{Preset: name, Options: map[string]string{}}
	if !hasColon {
		return spec, nil
	}
	preset, ok := r.Lookup(name)
	if !ok {
		return ProofSpec{}, fault.New(fault.CodeInvalidInput,
			"proof %q: %q is not a registered preset (registered: %s); use \"run: COMMAND\" for a raw command",
			s, name, strings.Join(r.List(), ", "))
	}
	spec.Size = preset.DefaultSize()
	toks, err := splitWords(rest)
	if err != nil {
		return ProofSpec{}, fault.Wrap(err, fault.CodeInvalidInput, "proof %q", s)
	}
	toks = takeOptions(toks, preset.Options(), spec.Options)
	if len(toks) > 0 {
		spec.Subject = strings.TrimSpace(rest[toks[0].start:toks[len(toks)-1].end])
	}
	return spec, nil
}

// takeOptions moves trailing declared key=value words into dst and returns
// the remaining subject words.
func takeOptions(toks []token, declared []string, dst map[string]string) []token {
	for len(toks) > 0 {
		m := optionPair.FindStringSubmatch(toks[len(toks)-1].text)
		if m == nil || !contains(declared, m[1]) {
			break
		}
		dst[m[1]] = m[2]
		toks = toks[:len(toks)-1]
	}
	return toks
}

func contains(xs []string, x string) bool {
	return slices.Contains(xs, x)
}

func (s ProofSpec) String() string {
	return fmt.Sprintf("%s: %s", s.Preset, s.Subject)
}
