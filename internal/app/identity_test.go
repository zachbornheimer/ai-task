package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// isolateGitIdentity hides the global and system Git configuration from
// this test, so the only identity is the one the test gives the repository.
func isolateGitIdentity(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	os.WriteFile(empty, nil, 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
}

func TestIntegrationMergeIdentity(t *testing.T) {
	isolateGitIdentity(t)
	f := newGitFixture(t)
	f.git(f.repo, "config", "user.useConfigOnly", "true")
	f.git(f.repo, "config", "user.name", "Repo Owner")
	f.git(f.repo, "config", "user.email", "owner@example.com")

	// The target moved, so at records a merge: as the repository's
	// identity, naming the task it integrates.
	a := f.add("a")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	f.commit(f.repo, "other.txt", "main moved")
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !res.Completed || res.IntegratedRevision == "" || res.IntegratedRevision == res.Revision {
		t.Fatalf("%+v %v", res, err)
	}
	who := f.git(f.repo, "log", "-1", "--format=%an <%ae>|%cn <%ce>", res.IntegratedRevision)
	if who != "Repo Owner <owner@example.com>|Repo Owner <owner@example.com>" {
		t.Fatalf("merge recorded as %q, want the repository's identity", who)
	}
	if trailers := f.git(f.repo, "log", "-1", "--format=%(trailers:key=At-Task,valueonly)", res.IntegratedRevision); trailers != string(a) {
		t.Fatalf("At-Task trailer = %q, want %s", trailers, a)
	}

	// Without an identity the merge cannot be recorded: a setup problem
	// named as such, not a conflict, and not counted against the attempt.
	f.git(f.repo, "config", "--unset", "user.name")
	f.git(f.repo, "config", "--unset", "user.email")
	b := f.add("b")
	sb := f.claim(string(b))
	f.commit(sb.Workspace, "b.txt", "b")
	f.commit(f.repo, "other.txt", "main moved again")
	before := f.git(f.repo, "rev-parse", "main")
	res, err = f.e.Verify(f.ctx, sb.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeWorkspaceUnavailable)
	if res.Completed || len(res.Conflicts) != 0 || !strings.Contains(res.Summary, "config user.name") {
		t.Fatalf("missing identity result: %+v", res)
	}
	if f.git(f.repo, "rev-parse", "main") != before {
		t.Fatal("target moved without an identity")
	}
	if v := f.show(string(b)); v.Failures != 0 {
		t.Fatalf("missing identity charged a strike: failures=%d", v.Failures)
	}
}

func TestDoctorGitIdentity(t *testing.T) {
	isolateGitIdentity(t)
	f := newGitFixture(t)
	f.git(f.repo, "config", "user.useConfigOnly", "true")
	find := func(rep app.DoctorReport) app.DoctorCheck {
		for _, c := range rep.Checks {
			if c.ID == "git_identity" {
				return c
			}
		}
		t.Fatalf("no git_identity check in %+v", rep)
		return app.DoctorCheck{}
	}
	rep, _ := f.e.Doctor(f.ctx, f.proj.ID, app.DoctorOptions{SkipBaseline: true})
	if c := find(rep); c.OK || c.Severity != "error" || rep.Healthy {
		t.Fatalf("missing identity must make doctor unhealthy: %+v healthy=%v", c, rep.Healthy)
	}
	f.git(f.repo, "config", "user.name", "Repo Owner")
	f.git(f.repo, "config", "user.email", "owner@example.com")
	rep, _ = f.e.Doctor(f.ctx, f.proj.ID, app.DoctorOptions{SkipBaseline: true})
	if c := find(rep); !c.OK || !strings.Contains(c.Message, "Repo Owner <owner@example.com>") {
		t.Fatalf("identity check: %+v", c)
	}
}
