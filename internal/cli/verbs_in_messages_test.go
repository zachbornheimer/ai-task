package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Messages that tell a user to run a command must name a registered verb.
var commandHint = regexp.MustCompile("`(atask|tasks|at) ([a-z][a-z-]*)")

func TestMessagesNameRegisteredVerbs(t *testing.T) {
	for _, dir := range []string{"../fault", "../app", "../execution"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if filepath.Ext(f) == ".go" && len(f) > 8 && f[len(f)-8:] == "_test.go" {
				continue
			}
			src, err := os.ReadFile(filepath.Clean(f))
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range commandHint.FindAllStringSubmatch(string(src), -1) {
				if m[1] != "atask" {
					t.Errorf("%s: %q: %q is not a binary; use atask", f, m[0], m[1])
					continue
				}
				if _, ok := commands[m[2]]; !ok {
					t.Errorf("%s: %q names unregistered verb %q", f, m[0], m[2])
				}
			}
		}
	}
}
