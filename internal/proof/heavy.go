package proof

import (
	"regexp"
	"strings"
)

var (
	// sizeSet matches an explicit size= in a spec's options.
	sizeSet = regexp.MustCompile(`(^|\s)size=`)
	// heavyCommand matches commands that build or run a whole suite: cold
	// in a fresh snapshot, they outlast the 30 s small budget. A single
	// package's tests (go test ./pkg -run X) are deliberately not matched.
	heavyCommand = regexp.MustCompile(`(^|[\s;&|(])(` +
		`cargo\s+(build|test|check|clippy|nextest|bench)` +
		`|go\s+(build|test)\b[^;&|]*\./\.\.\.` +
		`|npm\s+(ci|install|test)|npm\s+run\s+build` +
		`|(pnpm|yarn)\s+(install|test|build|add)` +
		`|docker\s+build|mvn\s|gradlew?\s|bazel\s|make(\s|$)` +
		`|mise\s+run\s+(ci|test|build)|tox(\s|$)|nox(\s|$)` +
		`)`)
)

// HeavyWithoutSize returns a warning when raw (a --proof or --regression
// value) is a run: check that looks like a full build or suite yet sets no
// size=, or "" when there is nothing to say. A check without size= gets
// the small budget (30 s); a heavy one fails by timeout in a cold
// snapshot, so the author learns it at filing rather than at completion.
// Presets with their own default size (ci, build, rs_test) are never
// flagged.
func HeavyWithoutSize(raw string) string {
	preset, subject, ok := strings.Cut(raw, ":")
	if !ok || strings.TrimSpace(preset) != "run" {
		return ""
	}
	if sizeSet.MatchString(subject) || !heavyCommand.MatchString(subject) {
		return ""
	}
	return "this run: check looks like a full build or test suite but sets no size=, so it gets the 30 s small budget and times out in a cold snapshot; add size=medium (300 s) or size=large (900 s)"
}
