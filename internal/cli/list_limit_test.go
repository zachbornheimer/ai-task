package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
)

// A bounded history listing says it is bounded, and how to lift the bound.
func TestRenderListNamesWhatTheLimitOmitted(t *testing.T) {
	var b bytes.Buffer
	snap := app.PlanSnapshot{Tasks: []app.TaskView{{ID: "at-aaaaaa", Title: "newest"}}, Omitted: 7}
	renderList(&b, snap, app.FilterComplete, painter{})
	out := b.String()
	if !strings.Contains(out, "7 older") || !strings.Contains(out, "--limit 0") {
		t.Fatalf("the listing does not say 7 older tasks were left out or how to list them:\n%s", out)
	}
}

func TestRenderListIsSilentWhenNothingWasOmitted(t *testing.T) {
	var b bytes.Buffer
	renderList(&b, app.PlanSnapshot{Tasks: []app.TaskView{{ID: "at-aaaaaa", Title: "only"}}}, app.FilterComplete, painter{})
	if strings.Contains(b.String(), "--limit") {
		t.Fatalf("mentioned a limit when none applied:\n%s", b.String())
	}
}

func TestListLimitMustBeANonNegativeNumber(t *testing.T) {
	h := newProofHarness(t)
	for _, bad := range []string{"-5", "abc"} {
		if code, _, _ := h.run("list", "complete", "--limit", bad); code == ExitOK {
			t.Errorf("list complete --limit %s succeeded, want a usage error", bad)
		}
	}
	for _, good := range []string{"0", "5"} {
		if code, out, errOut := h.run("list", "complete", "--limit", good); code != ExitOK {
			t.Errorf("list complete --limit %s: exit %d\n%s\n%s", good, code, out, errOut)
		}
	}
}
