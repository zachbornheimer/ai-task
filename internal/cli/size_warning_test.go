package cli

import (
	"strings"
	"testing"
)

// A proof or regression that looks like a full build or suite, with no
// size=, gets the 30 s small budget and times out cold: warn when it is
// filed, not when it fails minutes into a completion.
func TestAddWarnsAboutAHeavyCheckWithoutSize(t *testing.T) {
	h := newProofHarness(t)
	code, out, errOut := h.run("add", "heavy", "--proof", "run: cargo test", "--regression", "run: test -d .")
	if code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	if !strings.Contains(out, "Warning:") || !strings.Contains(out, "size=") {
		t.Fatalf("no size warning for `cargo test` without size=:\n%s", out)
	}
}

func TestAddDoesNotWarnWhenTheHeavyCheckIsSized(t *testing.T) {
	h := newProofHarness(t)
	code, out, errOut := h.run("add", "sized", "--proof", "run: cargo test size=medium", "--regression", "run: go test ./... size=large")
	if code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	if strings.Contains(out, "Warning:") && strings.Contains(out, "size=") {
		t.Fatalf("warned about a sized check:\n%s", out)
	}
}

func TestUpdateWarnsAboutAHeavyRegressionWithoutSize(t *testing.T) {
	h := newProofHarness(t)
	code, out, errOut := h.run("add", "upd", "--key", "upd", "--proof", "run: test -d .", "--regression", "run: test -d .")
	if code != ExitOK {
		t.Fatalf("add: %d %s %s", code, out, errOut)
	}
	code, out, errOut = h.run("update", "upd", "--regression", "run: cargo test --workspace")
	if code != ExitOK {
		t.Fatalf("update: %d %s", code, errOut)
	}
	if !strings.Contains(out, "Warning:") || !strings.Contains(out, "size=") {
		t.Fatalf("no size warning on update:\n%s", out)
	}
}
