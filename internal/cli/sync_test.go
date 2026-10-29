package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// runSyncAt runs the CLI with an explicit environment.
func runSyncAt(t *testing.T, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: t.TempDir(), Getenv: getenv})
	return code, out.String(), errb.String()
}

// seedSyncCLI creates a real task and a real claim through the CLI, so the
// source holds the shapes sync must copy: a project, a task with acceptance
// criteria and task_projects membership, an attempt, and a session file.
func seedSyncCLI(t *testing.T, db string) {
	code, out, errOut := runSyncAt(t, func(string) string { return "" },
		"--json", "--db", db, "add", "cli sync source", "--proof", "run: go version", "--regression", "run: go version")
	if code != ExitOK {
		t.Fatalf("add: exit %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = runSyncAt(t, func(string) string { return "" }, "--json", "--db", db, "claim")
	if code != ExitOK {
		t.Fatalf("claim: exit %d\n%s\n%s", code, out, errOut)
	}
}

// tasksIn returns the number of task records the database holds.
func tasksIn(t *testing.T, db string) int {
	t.Helper()
	s, err := sqlite.Open(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, s.Close()) }()
	var n int
	must.OK(t, s.Read(t.Context(), func(tx *sqlite.Tx) error {
		records, err := tx.ListRecords(project.ID(""), sqlite.ScopeAll, time.Now(), 5)
		n = len(records)
		return err
	}))
	return n
}

func TestSyncCopiesIntoDestinationAndCounts(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.db")
	dst := filepath.Join(t.TempDir(), "dst.db")
	seedSyncCLI(t, src)

	code, out, errOut := runSyncAt(t, func(string) string { return "" }, "--json", "sync", "--from", src, "--to", dst)
	if code != ExitOK {
		t.Fatalf("sync: exit %d\n%s\n%s", code, out, errOut)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			From   string `json:"from"`
			To     string `json:"to"`
			DryRun bool   `json:"dry_run"`
			Rows   int    `json:"rows"`
			Tables []struct {
				Table string `json:"table"`
				Rows  int    `json:"rows"`
			} `json:"tables"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	if !env.OK || env.Result.DryRun || env.Result.From != src {
		t.Fatalf("envelope: %+v\n%s", env.Result, out)
	}
	byTable := map[string]int{}
	for _, ct := range env.Result.Tables {
		byTable[ct.Table] = ct.Rows
	}
	if byTable["tasks"] != 1 || byTable["execution_attempts"] != 1 {
		t.Fatalf("counts: %+v", byTable)
	}
	if n := tasksIn(t, dst); n != 1 {
		t.Fatalf("destination holds %d tasks after sync; want 1", n)
	}
}

func TestSyncDryRunWritesNothing(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.db")
	dst := filepath.Join(t.TempDir(), "dst.db")
	seedSyncCLI(t, src)
	code, out, errOut := runSyncAt(t, func(string) string { return "" }, "--json", "sync", "--from", src, "--to", dst, "--dry-run")
	if code != ExitOK {
		t.Fatalf("sync --dry-run: exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, `"dry_run":true`) {
		t.Fatalf("dry run envelope: %s", out)
	}
	if n := tasksIn(t, dst); n != 0 {
		t.Fatalf("dry run wrote %d tasks into the destination", n)
	}
}

func TestSyncNeedsADestination(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.db")
	seedSyncCLI(t, src)
	code, _, errOut := runSyncAt(t, func(string) string { return "" }, "sync", "--from", src)
	if code != ExitUsage {
		t.Fatalf("sync without a destination: exit %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "sync needs a destination") {
		t.Fatalf("message: %s", errOut)
	}
	// ATASK_DB_URL is the default destination.
	code, _, errOut = runSyncAt(t, func(k string) string {
		if k == "ATASK_DB_URL" {
			return filepath.Join(t.TempDir(), "dst.db")
		}
		return ""
	}, "sync", "--from", src)
	if code != ExitOK {
		t.Fatalf("sync with ATASK_DB_URL: exit %d\n%s", code, errOut)
	}
}

func TestSyncRefusesRemoteSource(t *testing.T) {
	code, _, errOut := runSyncAt(t, func(string) string { return "" }, "sync", "--from", "postgres://u@h/atask", "--to", filepath.Join(t.TempDir(), "dst.db"))
	if code != ExitUsage {
		t.Fatalf("sync from a remote source: exit %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "sync reads a local SQLite path") {
		t.Fatalf("message: %s", errOut)
	}
}

func TestHelpListsSyncFlags(t *testing.T) {
	code, out, errOut := runSyncAt(t, func(string) string { return "" }, "help", "sync")
	if code != ExitOK {
		t.Fatalf("help sync: exit %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"-from", "-to", "-dry-run"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help sync lacks %s:\n%s", want, out)
		}
	}
}
