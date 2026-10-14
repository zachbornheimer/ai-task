package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/must"
)

// isolateGitIdentity hides the global and system Git configuration from
// this test, so the only identity is the one the test gives the repository.
func isolateGitIdentity(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	must.OK(t, os.WriteFile(empty, nil, 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Setenv(k, "")
			must.OK(t, os.Unsetenv(k))
		}
	}
}

func TestDoctorGitIdentity(t *testing.T) {
	f := newGitFixture(t)
	isolateGitIdentity(t)
	f.git(f.repo, "config", "user.useConfigOnly", "true")
	f.git(f.repo, "config", "--unset-all", "user.name")
	f.git(f.repo, "config", "--unset-all", "user.email")
	find := func(rep app.DoctorReport) app.DoctorCheck {
		for _, c := range rep.Checks {
			if c.ID == "git_identity" {
				return c
			}
		}
		t.Fatalf("no git_identity check in %+v", rep)
		return app.DoctorCheck{}
	}
	rep, _ := f.e.Doctor(f.ctx, f.proj.ID)
	if c := find(rep); c.OK || c.Severity != "error" || rep.Healthy {
		t.Fatalf("missing identity must make doctor unhealthy: %+v healthy=%v", c, rep.Healthy)
	}
	f.git(f.repo, "config", "user.name", "Repo Owner")
	f.git(f.repo, "config", "user.email", "owner@example.com")
	rep, _ = f.e.Doctor(f.ctx, f.proj.ID)
	if c := find(rep); !c.OK || !strings.Contains(c.Message, "Repo Owner <owner@example.com>") {
		t.Fatalf("identity check: %+v", c)
	}
}
