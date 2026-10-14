// Command embedded_runner shows the intended host integration end to end
// with NO model provider: a fake planner proposes a plan from a prompt, a
// fake adversarial reviewer revises it until stable, and N concurrent
// workers drain the graph through the public at API, each handing its
// session to a fake coding agent that uses the token-scoped `at` calls.
//
// Run it against a scratch Git repository:
//
//	go run ./examples/embedded_runner -repo /tmp/scratch-repo -workers 4
//
// The fake agent "implements" a task by committing a file named after the
// task into its private worktree and then calling Verify(complete).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	at "github.com/zachbornheimer/ai-task"
)

func main() {
	repo := flag.String("repo", "", "path to a Git repository to work in (created and initialized if empty)")
	workers := flag.Int("workers", 4, "concurrent fake agents")
	db := flag.String("db", "", "database path (default: a temp file)")
	flag.Parse()
	if err := run(context.Background(), *repo, *db, *workers, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// Run is the whole host flow; it is also exercised by the package test.
func run(ctx context.Context, repo, db string, workers int, out *os.File) error {
	if repo == "" {
		var err error
		if repo, err = os.MkdirTemp("", "at-example-repo-"); err != nil {
			return err
		}
		if err := initRepo(repo); err != nil {
			return err
		}
	}
	if db == "" {
		dir, err := os.MkdirTemp("", "at-example-db-")
		if err != nil {
			return err
		}
		db = filepath.Join(dir, "at.db")
	}
	eng, err := at.Open(ctx, at.Config{Path: db, PollInterval: 200 * time.Millisecond})
	if err != nil {
		return err
	}
	defer eng.Close()
	proj, err := eng.InitProject(ctx, "example", repo)
	if err != nil {
		return err
	}

	// Step A: propose and apply atomically.
	prompt := "Build the OAuth token endpoint with a Postgres token store and scope enforcement, then verify the flow end to end."
	proposal := fakePlanner(prompt)
	snap, err := eng.List(ctx, at.ListQuery{ProjectID: proj.ID, Filter: at.FilterAll})
	if err != nil {
		return err
	}
	if _, err := eng.Apply(ctx, at.ChangeSet{ProjectID: proj.ID, ExpectedPlanRev: snap.Revision, IdempotencyKey: "plan-1", Operations: proposal}); err != nil {
		return err
	}

	// Step B: adversarial review until stable, bounded.
	const maxReviewRounds = 6
	stable := false
	for round := range maxReviewRounds {
		current, err := eng.List(ctx, at.ListQuery{ProjectID: proj.ID, Filter: at.FilterAll})
		if err != nil {
			return err
		}
		changes := fakeReviewer(current)
		if len(changes) == 0 {
			stable = true
			break
		}
		applied, err := eng.Apply(ctx, at.ChangeSet{ProjectID: proj.ID, ExpectedPlanRev: current.Revision, IdempotencyKey: fmt.Sprintf("review-%d", round), Operations: changes})
		if err != nil {
			return err // a stale plan would surface here as CONFLICT: re-read, re-review
		}
		if applied.Changed == 0 {
			stable = true
			break
		}
	}
	if !stable {
		return errors.New("review did not converge")
	}
	fmt.Fprintln(out, "plan ready:")
	printPlan(ctx, eng, proj.ID, out)

	// Step C: N workers drain the graph continuously. No batch barriers.
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := worker(ctx, eng, proj.ID, i, out); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	fmt.Fprintln(out, "finished:")
	printPlan(ctx, eng, proj.ID, out)
	sum, err := eng.Summary(ctx, proj.ID)
	if err != nil {
		return err
	}
	if !sum.Done {
		return fmt.Errorf("project not done: %+v", sum)
	}
	return nil
}

func worker(ctx context.Context, eng *at.Store, project at.ProjectID, n int, out *os.File) error {
	for {
		session, err := eng.Claim(ctx, at.ClaimRequest{ProjectID: project, Wait: true})
		switch {
		case errors.Is(err, at.ErrDone):
			return nil
		case errors.Is(err, at.ErrStalled):
			// A stall is a human's problem: say why, and show the plan.
			var fe *at.Error
			if errors.As(err, &fe) {
				if sum, ok := fe.Details.(at.Summary); ok {
					fmt.Fprintf(out, "worker %d: stalled: %v\n", n, sum.Reasons)
				}
			}
			printPlan(ctx, eng, project, out)
			return fmt.Errorf("worker %d: stalled, human action needed: %w", n, err)
		case err != nil:
			return err
		}
		if err := runOneAgent(ctx, eng, session, n, out); err != nil {
			// A recoverable model failure: log, release with --failed so the
			// engine backs off, and keep draining other work. A failed
			// Verify(complete) already ended nothing; the claim is still
			// ours, so release it for the next attempt.
			fmt.Fprintf(out, "worker %d: %s agent error: %v\n", n, session.Task.ID, err)
			_ = eng.Log(ctx, session.Token, at.LogEntry{Note: "agent error: " + err.Error()})
			_ = eng.Release(ctx, at.ReleaseRequest{Token: session.Token, Note: "released after agent error", Failed: true})
		}
	}
}

// runOneAgent is the fake coding agent. A real host would render a prompt
// from session.Task and session.Handoff, start the model in
// session.Workspace with ATASK_SESSION set, and keep renewing the lease.
func runOneAgent(ctx context.Context, eng *at.Store, s at.Session, n int, out *os.File) error {
	stop := heartbeat(ctx, eng, s.Token)
	defer stop()
	fmt.Fprintf(out, "worker %d: claimed %s (%s)\n", n, s.Task.ID, s.Task.Title)
	if err := eng.Log(ctx, s.Token, at.LogEntry{Done: "read the contract", Next: "implement " + s.Task.Title}); err != nil {
		return err
	}
	// "Implement": write the file the task's check expects, commit it.
	file := filepath.Join(s.Workspace, s.Task.Key+".txt")
	if err := os.WriteFile(file, []byte("implemented by worker "+strconv.Itoa(n)), 0o644); err != nil {
		return err
	}
	if err := git(s.Workspace, "add", "."); err != nil {
		return err
	}
	// The worktree persists across attempts, so a retry may find the work
	// already committed by the previous attempt.
	if git(s.Workspace, "diff", "--cached", "--quiet") != nil {
		if err := git(s.Workspace, "commit", "-q", "-m", "implement "+s.Task.Key); err != nil {
			return err
		}
	}
	// Iterate with provisional checks (a failure here is information for
	// the agent, not an error), then establish completion.
	if diag, err := eng.Verify(ctx, at.VerifyRequest{TaskID: s.Task.ID, Token: s.Token, Mode: at.VerifyProof}); err != nil && at.ErrorCode(err) != "CHECK_FAILED" {
		return err
	} else if !diag.Passed {
		_ = eng.Log(ctx, s.Token, at.LogEntry{Note: "provisional task checks: " + diag.Summary})
	}
	res, err := eng.Verify(ctx, at.VerifyRequest{TaskID: s.Task.ID, Token: s.Token, Mode: at.VerifyComplete})
	if err != nil {
		return err
	}
	switch {
	case res.Completed:
		fmt.Fprintf(out, "worker %d: %s complete\n", n, s.Task.ID)
	}
	return nil
}

func heartbeat(ctx context.Context, eng *at.Store, tok at.SessionToken) func() {
	hctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				if _, err := eng.Renew(hctx, tok); err != nil {
					return // the host would cancel the agent process here
				}
			}
		}
	}()
	return cancel
}

// regressionProof is every task's own "nothing else broke" proof.
var regressionProof = &at.ProofSpec{Preset: "run", Subject: "test -d .", Options: map[string]string{}, Size: at.SizeSmall}

// fileProof proves a task wrote its file.
func fileProof(file string) *at.ProofSpec {
	return &at.ProofSpec{Preset: "run", Subject: "test -f " + file, Options: map[string]string{}}
}

// fakePlanner turns the prompt into a typed proposal. A real planner asks a
// model and parses its answer into the same closed set of changes.
func fakePlanner(prompt string) []at.Change {
	task := func(key, title, parent string, requires ...at.Ref) at.AddTask {
		return at.AddTask{Key: key, Title: title, Parent: at.Ref(parent), Requires: requires, Proof: fileProof(key + ".txt"), Regression: regressionProof}
	}
	return []at.Change{
		at.AddGroup{Key: "identity", Title: "Identity Core", Proof: fileProof("compat.txt"), Regression: regressionProof},
		task("compat", "Confirm OAuth API compatibility", "identity"),
		task("store", "Implement PostgreSQL token store", "identity", "compat"),
		task("scopes", "Implement scope enforcement", "identity", "compat"),
		task("e2e", "Verify end-to-end OAuth flow", "identity", "store", "scopes"),
		at.AddGroup{Key: "oauth-flow", Title: "OAuth flow", Proof: &at.ProofSpec{Preset: "run", Subject: "sh -c 'test -f api.txt && test -f ui.txt'", Options: map[string]string{}}, Regression: regressionProof},
		task("api", "OAuth API side", "oauth-flow"),
		task("ui", "OAuth UI side", "oauth-flow"),
	}
}

// fakeReviewer sees the whole current plan each round. Round 1 it notices
// the leaked-credential rotation is missing and adds it as a prerequisite
// of the token store; afterwards it has nothing to add.
func fakeReviewer(current at.PlanSnapshot) []at.Change {
	for _, t := range current.Tasks {
		if t.Key == "rotate" {
			return nil
		}
	}
	return []at.Change{
		at.AddTask{Key: "rotate", Title: "Rotate leaked credentials", Blocks: []at.Ref{"store"}, Proof: fileProof("rotate.txt"), Regression: regressionProof},
	}
}

func printPlan(ctx context.Context, eng *at.Store, project at.ProjectID, out *os.File) {
	snap, err := eng.List(ctx, at.ListQuery{ProjectID: project, Filter: at.FilterAll})
	if err != nil {
		fmt.Fprintln(out, err)
		return
	}
	for _, g := range snap.Groups {
		fmt.Fprintf(out, "  group %s %s (%d/%d)\n", g.ID, g.Title, g.Progress.Complete, g.Progress.Total)
	}
	for _, t := range snap.Tasks {
		fmt.Fprintf(out, "  %s %s  %s [%s]", at.Glyph(t.Status, t.Started), t.ID, t.Title, t.Status)
		if r := t.Verification.Complete; r != nil && r.Summary != "" {
			fmt.Fprintf(out, " — %s: %s", r.Status, r.Summary)
		}
		fmt.Fprintln(out)
	}
}

func initRepo(dir string) error {
	if err := git(dir, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	if err := git(dir, "config", "user.name", "at-example"); err != nil {
		return err
	}
	if err := git(dir, "config", "user.email", "at@example"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("example"), 0o644); err != nil {
		return err
	}
	if err := git(dir, "add", "README.txt"); err != nil {
		return err
	}
	return git(dir, "commit", "-q", "-m", "init")
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=at-example", "GIT_AUTHOR_EMAIL=at@example", "GIT_COMMITTER_NAME=at-example", "GIT_COMMITTER_EMAIL=at@example", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HK=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return nil
}
