package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestRenderVerifyPointsAtLogs(t *testing.T) {
	res := app.VerifyResult{TaskID: "at-aaaaaa", Mode: verification.ModeTask, LogDir: "/state/logs/p/run-7", Evidence: []verification.Evidence{
		{CheckID: "unit", Required: true, Outcome: verification.OutcomeFailed, ExitCode: 1, Stdout: "--- FAIL: TestX", Log: "/state/logs/p/run-7/unit.log"},
		{CheckID: "vet", Required: true, Outcome: verification.OutcomePassed, Log: "/state/logs/p/run-7/vet.log"},
	}}
	var b bytes.Buffer
	renderVerify(&b, res)
	out := b.String()
	if !strings.Contains(out, "full output: /state/logs/p/run-7/unit.log") || !strings.Contains(out, "Logs:      /state/logs/p/run-7 (result.json and one log per check)") {
		t.Fatalf("output does not point at the logs:\n%s", out)
	}
	if strings.Contains(out, "vet.log") {
		t.Fatalf("a passing check's log is listed:\n%s", out)
	}
}
