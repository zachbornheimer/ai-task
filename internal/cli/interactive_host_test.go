package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
)

// The child exposes the real CLI with a short lease, not a production lease override.
func TestLeaseCLIHelper(t *testing.T) {
	if os.Getenv("ATASK_LEASE_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			cwd, err := os.Getwd()
			if err != nil {
				panic(err)
			}
			os.Exit(Main(Env{Args: os.Args[i+1:], Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv, Cwd: cwd,
				engineConfig: &app.Config{Lease: time.Second, HeartbeatInterval: 100 * time.Millisecond}}))
		}
	}
	t.Fatal("helper arguments missing")
}

func TestInteractiveLeaseHost(t *testing.T) {
	if os.Getenv("ATASK_INTERACTIVE_HOST") == "" {
		t.Skip("run mise test:lease for the interactive host integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	bin := t.TempDir()
	// PATH preserves the exact production command; only this fixture binary has short leases.
	quoted := "'" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "'"
	launcher := "#!/bin/sh\nexec " + quoted + " -test.run=^TestLeaseCLIHelper$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "atask"), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "bun", os.Getenv("ATASK_INTERACTIVE_HOST"))
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "ATASK_LEASE_HELPER=1", "ATASK_LEASE_CLI="+os.Args[0], "ATASK_DB="+filepath.Join(t.TempDir(), "lease.db"), "ATASK_DB_URL=", "ATASK_SESSION=", "ATASK_PROJECT=", "ATASK_RUNTIME_ROOT=", "PI_SESSION_ID=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("interactive host: %v\n%s", err, output)
	}
	t.Log(string(output))
}
