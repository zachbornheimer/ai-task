package app_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestVerifyWritesRunLogs(t *testing.T) {
	f := newGitFixture(t)
	// More output than evidence keeps (its head and tail), with the line
	// that matters in the middle, as go test prints a FAIL among PASSes.
	script := `awk 'BEGIN{for(i=0;i<3000;i++)print "pass line " i; print "MIDDLE-MARKER"; for(i=0;i<3000;i++)print "pass line " i}'; echo on-stderr >&2; test -f fixed`
	a := f.addWith("a", script)
	s := f.claim(string(a))

	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeTask)
	wantCode(t, err, fault.CodeVerificationFailed)
	if res.LogDir == "" || len(res.Evidence) != 1 {
		t.Fatalf("no run directory: %+v", res)
	}
	ev := res.Evidence[0]
	if strings.Contains(ev.Stdout, "MIDDLE-MARKER") {
		t.Fatal("test output too small: evidence kept the middle")
	}
	if ev.Log == "" || filepath.Dir(ev.Log) != res.LogDir {
		t.Fatalf("evidence log %q not in run directory %q", ev.Log, res.LogDir)
	}
	log, rerr := os.ReadFile(ev.Log)
	if rerr != nil || !strings.Contains(string(log), "MIDDLE-MARKER") || !strings.Contains(string(log), "on-stderr") || !strings.Contains(string(log), "pass line 2999") {
		t.Fatalf("log lacks the full output (err %v, %d bytes)", rerr, len(log))
	}
	var rr struct {
		OK     bool `json:"ok"`
		Result struct {
			RunID    int64 `json:"run_id"`
			Evidence []struct {
				Log string `json:"log"`
			} `json:"evidence"`
		} `json:"result"`
		Error *struct {
			Code fault.Code `json:"code"`
		} `json:"error"`
	}
	b, rerr := os.ReadFile(filepath.Join(res.LogDir, "result.json"))
	if rerr != nil || json.Unmarshal(b, &rr) != nil {
		t.Fatalf("result.json: %v\n%s", rerr, b)
	}
	if rr.OK || rr.Error == nil || rr.Error.Code != fault.CodeVerificationFailed || rr.Result.RunID != res.RunID || len(rr.Result.Evidence) != 1 || rr.Result.Evidence[0].Log != ev.Log {
		t.Fatalf("result.json does not record the failed run: %s", b)
	}

	// The stored evidence keeps the path: the next attempt's handoff
	// points at the failing run's full output.
	f.commit(s.Workspace, "a.txt", "attempt one")
	failed, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	if _, err := f.e.Release(f.ctx, s.Token, app.ReleaseOptions{Note: "stuck"}); err != nil {
		t.Fatal(err)
	}
	s = f.claim(string(a))
	if lf := s.Handoff.LastFailure; lf == nil || len(lf.Checks) != 1 || lf.Checks[0].Log == "" || filepath.Dir(lf.Checks[0].Log) != failed.LogDir {
		t.Fatalf("handoff does not point at the full output: %+v (run dir %s)", lf, failed.LogDir)
	}
	f.commit(s.Workspace, "fixed", "x")
	done, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !done.Completed || done.LogDir == "" || done.LogDir == res.LogDir {
		t.Fatalf("%+v %v", done, err)
	}
	for _, ev := range done.Evidence {
		if ev.Log == "" {
			t.Fatalf("check %s has no log", ev.CheckID)
		}
		if _, err := os.Stat(ev.Log); err != nil {
			t.Fatal(err)
		}
	}
	b, _ = os.ReadFile(filepath.Join(done.LogDir, "result.json"))
	var passed struct {
		OK    bool            `json:"ok"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(b, &passed); err != nil || !passed.OK || passed.Error != nil {
		t.Fatalf("result.json of the passing run: %s", b)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.path), "logs")); errors.Is(err, os.ErrNotExist) {
		t.Fatal("logs not beside the database")
	}
}
