package cli

import (
	"strings"
	"testing"
)

// TestOutcomeFlagIsDocumentedAsTheSpecField: a task's long description is
// its outcome (with --constraint and --accept), printed by `atask context`.
// Callers looking for a "description" or "spec" flag must find it in the
// help instead of cramming the spec into the title.
func TestOutcomeFlagIsDocumentedAsTheSpecField(t *testing.T) {
	for _, verb := range []string{"add", "update"} {
		code, out, errOut := runAt(t, verb, "--help")
		if code != ExitOK {
			t.Fatalf("atask %s --help: exit %d\n%s\n%s", verb, code, out, errOut)
		}
		help := out + errOut
		var line string
		for _, l := range strings.Split(help, "\n") {
			if strings.Contains(l, "-outcome") {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("atask %s --help lists no --outcome flag:\n%s", verb, help)
		}
		idx := strings.Index(help, line)
		doc := strings.ToLower(help[idx:min(len(help), idx+400)])
		if !strings.Contains(doc, "spec") || !strings.Contains(doc, "atask context") {
			t.Errorf("atask %s --help: --outcome is not described as the long spec field shown by `atask context`:\n%s", verb, doc)
		}
	}
}
