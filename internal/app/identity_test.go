package app_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/must"
)

func isolateGitIdentity(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	must.OK(t, os.WriteFile(empty, nil, 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Setenv(k, "")
			must.OK(t, os.Unsetenv(k))
		}
	}
}

func TestIsolateGitIdentityInjectedConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"count", map[string]string{
			"GIT_CONFIG_COUNT":      "1",
			"GIT_CONFIG_KEY_0":      "core.abbrev",
			"GIT_CONFIG_VALUE_0":    "12",
			"GIT_CONFIG_PARAMETERS": "",
		}},
		{"parameters", map[string]string{
			"GIT_CONFIG_COUNT":      "0",
			"GIT_CONFIG_PARAMETERS": "'core.abbrev=12'",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			repo := t.TempDir()
			abbrev := func() (string, error) {
				cmd := exec.Command("git", "config", "--get", "core.abbrev")
				cmd.Dir = repo
				out, err := cmd.CombinedOutput()
				return strings.TrimSpace(string(out)), err
			}
			if got, err := abbrev(); err != nil || got != "12" {
				t.Fatalf("injected config before isolation = %q, %v; want 12", got, err)
			}
			t.Run("isolated", func(t *testing.T) {
				isolateGitIdentity(t)
				got, err := abbrev()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || got != "" {
					t.Fatalf("injected config leaked through isolation: %q, %v", got, err)
				}
			})
			if got, err := abbrev(); err != nil || got != "12" {
				t.Fatalf("injected config after cleanup = %q, %v; want 12", got, err)
			}
		})
	}
}

func requireGitIdentityCheck(t *testing.T, report app.DoctorReport) app.DoctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == "git_identity" {
			return check
		}
	}
	t.Fatalf("no git_identity check in %+v", report)
	return app.DoctorCheck{}
}

func TestDoctorGitIdentity(t *testing.T) {
	f := newGitFixture(t)
	isolateGitIdentity(t)
	f.git(f.repo, "config", "user.useConfigOnly", "true")
	f.git(f.repo, "config", "--unset-all", "user.name")
	f.git(f.repo, "config", "--unset-all", "user.email")
	rep, err := f.e.Doctor(f.ctx, f.proj.ID)
	must.OK(t, err)
	if c := requireGitIdentityCheck(t, rep); c.OK || c.Severity != "error" || rep.Healthy {
		t.Fatalf("missing identity must make doctor unhealthy: %+v healthy=%v", c, rep.Healthy)
	}
	f.git(f.repo, "config", "user.name", "Repo Owner")
	f.git(f.repo, "config", "user.email", "owner@example.com")
	rep, err = f.e.Doctor(f.ctx, f.proj.ID)
	must.OK(t, err)
	if c := requireGitIdentityCheck(t, rep); !c.OK || !strings.Contains(c.Message, "Repo Owner <owner@example.com>") {
		t.Fatalf("identity check: %+v", c)
	}
}
