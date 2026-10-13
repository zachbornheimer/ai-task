package proof

import (
	"os"
	"path/filepath"
	"regexp"
)

// langZeroTests matches every runner's "nothing ran" report.
var langZeroTests = regexp.MustCompile(`No tests|\b0 examples\b|Files=0|Executed 0 tests|\b0 tests\b`)

// fileExists is the one filesystem probe the fallbacks use.
var fileExists = func(path string) bool { _, err := os.Stat(path); return err == nil }

// langTest is a "<lang>_test: PATH [name=..]" preset: one language's runner,
// passing only when it exits 0 and reports at least one test.
type langTest struct {
	name string
	argv func(subject, name string, env Env) []string
}

var (
	_ = Preset(langTest{})
	_ = Judger(langTest{})
)

func init() {
	for _, p := range []langTest{
		{"pl_test", func(s, _ string, _ Env) []string { return []string{"prove", "-v", s} }},
		{"php_test", func(s, n string, e Env) []string {
			return withFlag(pick(e, "vendor/bin/phpunit", "phpunit"), s, "--filter", n)
		}},
		{"rb_test", func(s, n string, e Env) []string {
			cmd := []string{"rspec"}
			if fileExists(filepath.Join(e.Workdir, "Gemfile")) {
				cmd = []string{"bundle", "exec", "rspec"}
			}
			return withFlag(cmd, s, "-e", n)
		}},
		{"swift_test", func(_, n string, _ Env) []string { return withFlag([]string{"swift", "test"}, "", "--filter", n) }},
		{"sh_test", func(s, n string, _ Env) []string { return withFlag([]string{"bats"}, s, "-f", n) }},
	} {
		registerBuiltin(func() Preset { return p })
	}
}

// pick returns the project-local runner when present, else the global one.
func pick(env Env, local, global string) []string {
	if fileExists(filepath.Join(env.Workdir, local)) {
		return []string{local}
	}
	return []string{global}
}

// withFlag appends subject, then "flag value" when value is set.
func withFlag(cmd []string, subject, flag, value string) []string {
	if subject != "" {
		cmd = append(cmd, subject)
	}
	if value != "" {
		cmd = append(cmd, flag, value)
	}
	return cmd
}

func (p langTest) Name() string    { return p.name }
func (langTest) Options() []string { return []string{"name", "expect"} }
func (langTest) DefaultSize() Size { return SizeSmall }

func (p langTest) Argv(spec ProofSpec, env Env) ([]string, error) {
	return p.argv(spec.Subject, spec.Options["name"], env), nil
}

func (langTest) Judge(_ ProofSpec, output []byte, exitCode int) (bool, string) {
	if exitCode != 0 {
		return false, "runner exited non-zero"
	}
	if langZeroTests.Match(output) {
		return false, "runner ran zero tests"
	}
	return true, ""
}
