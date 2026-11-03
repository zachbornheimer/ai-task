package app_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// The child process of the dead-owner tests: it runs `verify complete` as a
// separate OS process under production timing (30-minute lease, 5-minute
// heartbeat), so the parent can kill it the way `pkill` does. It does nothing
// unless the parent set its environment.
const (
	helperDBEnv    = "ATASK_TEST_VERIFY_DB"
	helperTokenEnv = "ATASK_TEST_VERIFY_TOKEN"
)

func TestHelperVerifyProcess(t *testing.T) {
	path := os.Getenv(helperDBEnv)
	if path == "" {
		t.Skip("helper process of the dead-owner verify tests")
	}
	e, err := app.Open(context.Background(), app.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	_, _ = e.Verify(context.Background(), execution.Token(os.Getenv(helperTokenEnv)), verification.ModeComplete)
}

// deadOwnerRun is a task whose `verify complete` was started in another
// process and killed with SIGKILL while its check ran.
type deadOwnerRun struct {
	e     *app.Engine
	f     *fixture
	id    task.ID
	token execution.Token
}

// killedVerify claims a task whose first check run sleeps (recording its pid)
// and whose later runs pass, verifies it in a child process, and kills that
// process once the task reads verifying. The run's heartbeat is fresh and the
// attempt lease is live, as when an operator kills the CLI mid-run.
func killedVerify(t *testing.T, f *fixture) deadOwnerRun {
	t.Helper()
	e := f.realClockEngineWithLease(execution.DefaultLease, 0)
	dir := t.TempDir()
	marker, pidFile := filepath.Join(dir, "ran"), filepath.Join(dir, "pid")
	script := filepath.Join(dir, "check.sh")
	body := "if [ -e " + marker + " ]; then exit 0; fi\n: > " + marker + "\necho $$ > " + pidFile + "\nexec sleep 120\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	id := f.addWith("dead", "sh "+script)
	s, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &id})
	if err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "d.txt", "d")

	child := exec.Command(os.Args[0], "-test.run=^TestHelperVerifyProcess$")
	child.Env = append(os.Environ(), helperDBEnv+"="+f.path, helperTokenEnv+"="+string(s.Token))
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL) // the check outlives its killed parent
			}
		}
	})
	deadline := time.Now().Add(verifyingWait)
	for {
		if v, err := e.Show(f.ctx, f.proj.ID, string(id), false); err == nil && v.Status == task.StatusVerifying {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child never reached verifying within %s", verifyingWait)
		}
		time.Sleep(verifyingPoll)
	}
	// Verifying is recorded before the check starts: kill only once the
	// check is running, or the marker the later runs rely on is missing.
	for {
		if b, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(b)) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("check never started within %s", verifyingWait)
		}
		time.Sleep(verifyingPoll)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	return deadOwnerRun{e: e, f: f, id: id, token: s.Token}
}

func (r deadOwnerRun) lastRun(t *testing.T) (status, summary string) {
	t.Helper()
	db, err := sql.Open("sqlite", r.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer discard.CloseReadOnly(db)
	if err := db.QueryRow(`SELECT status, summary FROM verification_runs WHERE task_id = ? ORDER BY id LIMIT 1`, r.id).Scan(&status, &summary); err != nil {
		t.Fatal(err)
	}
	return status, summary
}

func (r deadOwnerRun) failures(t *testing.T) int {
	t.Helper()
	db, err := sql.Open("sqlite", r.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer discard.CloseReadOnly(db)
	var n int
	if err := db.QueryRow(`SELECT failures FROM tasks WHERE id = ?`, r.id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A `verify complete` whose process was killed does not pin the task: the
// next `verify complete` sees the owner gone, abandons the run (no strike
// against the attempt budget) and runs its own.
func TestVerifyDeadOwnerRunIsAbandonedByNextVerify(t *testing.T) {
	r := killedVerify(t, newGitFixture(t))
	res, err := r.e.Verify(r.f.ctx, r.token, verification.ModeComplete)
	if err != nil {
		t.Fatalf("next verify complete after the owner was killed: %v", err)
	}
	if !res.Completed {
		t.Fatalf("next verify complete did not complete: %+v", res)
	}
	if status, summary := r.lastRun(t); status != "error" || !strings.HasPrefix(summary, "abandoned:") {
		t.Fatalf("killed run = %s %q, want error \"abandoned: ...\"", status, summary)
	}
	if n := r.failures(t); n != 0 {
		t.Fatalf("an abandoned run charged %d failure(s) to the attempt budget", n)
	}
}

// After the operator releases the claim of a killed run, the task is
// claimable again instead of reading "has a submission being verified".
func TestVerifyDeadOwnerRunDoesNotBlockClaim(t *testing.T) {
	r := killedVerify(t, newGitFixture(t))
	if err := r.e.Release(r.f.ctx, app.ReleaseRequest{Token: r.token}); err != nil {
		t.Fatal(err)
	}
	if v, err := r.e.Show(r.f.ctx, r.f.proj.ID, string(r.id), false); err != nil || v.Status == task.StatusVerifying {
		t.Fatalf("task after release: status %s err %v, want not verifying", v.Status, err)
	}
	if _, err := r.e.Claim(r.f.ctx, app.ClaimRequest{TaskID: &r.id}); err != nil {
		t.Fatalf("claim after the verifier died: %v", err)
	}
}

// A pid the OS reused is not the verifier that recorded the run: the owner's
// recorded start time no longer matches the process now holding the pid.
func TestVerifyStaleOwnerPidIsAbandoned(t *testing.T) {
	f := newGitFixture(t)
	e, _, id, cancel := startSleepingVerify(t, f)
	f.sql(`UPDATE verification_runs SET owner_start = 'started-before-this-process' WHERE status = 'running'`)
	cancel()
	v, err := e.Show(f.ctx, f.proj.ID, string(id), false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status == task.StatusVerifying {
		t.Fatal("a run whose recorded pid now belongs to another process still pins the task")
	}
}

// `atask doctor` names a task whose verification run was abandoned until a
// later run supersedes it.
func TestVerifyOrphanRunIsReportedByDoctor(t *testing.T) {
	r := killedVerify(t, newGitFixture(t))
	report := func() app.DoctorCheck {
		rep, err := r.e.Doctor(r.f.ctx, r.f.proj.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range rep.Checks {
			if c.ID == "abandoned_runs" {
				return c
			}
		}
		t.Fatal("doctor has no abandoned_runs check")
		return app.DoctorCheck{}
	}
	if c := report(); c.OK || !strings.Contains(c.Message, string(r.id)) {
		t.Fatalf("doctor on an abandoned run = ok %v %q, want a finding naming %s", c.OK, c.Message, r.id)
	}
	if res, err := r.e.Verify(r.f.ctx, r.token, verification.ModeComplete); err != nil || !res.Completed {
		t.Fatalf("verify after abandonment: %+v %v", res, err)
	}
	if c := report(); !c.OK {
		t.Fatalf("doctor still reports an abandoned run after a later run: %q", c.Message)
	}
}
