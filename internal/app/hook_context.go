package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

const (
	hookContextVersion = 1
	hookRecentLimit    = 3
	hookStateDirName   = "hook-state"
	verifyNotRun       = "not_run"
	verifyPassed       = "passed"
	verifyFailed       = "failed"
	statusNoClaim      = "no_active_claim"
)

type hookTask struct {
	ID     string `yaml:"id"`
	Status string `yaml:"status"`
	Title  string `yaml:"title"`
}

type hookNoClaim struct {
	Version int    `yaml:"atask_context_version"`
	Task    any    `yaml:"task"`
	Status  string `yaml:"status"`
}

type hookComplete struct {
	Version int      `yaml:"atask_context_version"`
	Task    hookTask `yaml:"task"`
}

type hookActive struct {
	Version      int                 `yaml:"atask_context_version"`
	Task         hookTask            `yaml:"task"`
	Objective    hookObjective       `yaml:"objective"`
	Acceptance   hookAcceptance      `yaml:"acceptance"`
	Progress     hookProgress        `yaml:"progress"`
	Blockers     []map[string]string `yaml:"blockers"`
	Verification hookVerification    `yaml:"verification"`
	Workspace    hookWorkspace       `yaml:"workspace"`
}

type hookObjective struct {
	Outcome string `yaml:"outcome"`
}

type hookAcceptance struct {
	Proof      string `yaml:"proof"`
	Regression string `yaml:"regression"`
}

type hookProgress struct {
	Done    []string `yaml:"done"`
	Next    *string  `yaml:"next"`
	Learned []string `yaml:"learned"`
}

type hookVerification struct {
	Proof       string       `yaml:"proof"`
	Regression  string       `yaml:"regression"`
	LastFailure *hookFailure `yaml:"last_failure"`
}

type hookFailure struct {
	Check  string `yaml:"check"`
	Reason string `yaml:"reason"`
}

type hookWorkspace struct {
	Path string `yaml:"path"`
}

// HookContext renders the current claim's minimal, deterministic context as
// YAML. It only reads: it never renews a lease or writes task state. An empty
// or unresolvable token renders the no-active-claim form.
func (e *Engine) HookContext(ctx context.Context, tok execution.Token) ([]byte, error) {
	if tok == "" {
		return marshalHook(hookNoClaim{Version: hookContextVersion, Status: statusNoClaim})
	}
	v, err := e.Whoami(ctx, tok)
	if err != nil {
		return marshalHook(hookNoClaim{Version: hookContextVersion, Status: statusNoClaim})
	}
	head := hookTask{ID: string(v.ID), Status: string(v.Status), Title: v.Title}
	if v.Status == task.StatusComplete {
		return marshalHook(hookComplete{Version: hookContextVersion, Task: head})
	}
	return marshalHook(buildHookActive(v, head))
}

func buildHookActive(v TaskView, head hookTask) hookActive {
	out := hookActive{
		Version:   hookContextVersion,
		Task:      head,
		Objective: hookObjective{Outcome: v.Outcome},
		Progress:  hookProgress{Done: []string{}, Learned: []string{}},
		Blockers:  []map[string]string{},
		Verification: hookVerification{
			Proof:      runVerdict(v.Verification.Task),
			Regression: runVerdict(v.Verification.Regression),
		},
	}
	if v.Proof != nil {
		out.Acceptance.Proof = v.Proof.String()
	}
	if v.Regression != nil {
		out.Acceptance.Regression = v.Regression.String()
	}
	for _, r := range v.Requires {
		if r.Status != task.StatusComplete {
			out.Blockers = append(out.Blockers, map[string]string{string(r.ID): r.Title})
		}
	}
	sort.Slice(out.Blockers, func(i, j int) bool { return blockerID(out.Blockers[i]) < blockerID(out.Blockers[j]) })
	if v.Attempt != nil {
		out.Workspace.Path = v.Attempt.Workspace
	}
	if h := v.Handoff; h != nil {
		fillHookHandoff(&out, *h)
	}
	return out
}

func blockerID(m map[string]string) string {
	for id := range m {
		return id
	}
	return ""
}

// fillHookHandoff copies the newest done entries (RecentLogs is newest
// first), the last next, the newest learnings and the last failed check.
func fillHookHandoff(out *hookActive, h execution.Handoff) {
	if h.LatestNext != "" {
		next := h.LatestNext
		out.Progress.Next = &next
	}
	for _, l := range h.RecentLogs {
		if l.Done != "" && len(out.Progress.Done) < hookRecentLimit {
			out.Progress.Done = append(out.Progress.Done, l.Done)
		}
	}
	for _, l := range h.Learnings {
		if len(out.Progress.Learned) < hookRecentLimit {
			out.Progress.Learned = append(out.Progress.Learned, l)
		}
	}
	if f := h.LastFailure; f != nil {
		out.Verification.LastFailure = &hookFailure{Check: f.Mode, Reason: f.Summary}
		checks := append([]execution.FailedCheck{}, f.Checks...)
		sort.Slice(checks, func(i, j int) bool { return checks[i].CheckID < checks[j].CheckID })
		if len(checks) > 0 {
			reason := checks[0].Message
			if reason == "" {
				reason = checks[0].Outcome
			}
			out.Verification.LastFailure = &hookFailure{Check: checks[0].CheckID, Reason: reason}
		}
	}
}

func runVerdict(r *RunView) string {
	if r == nil {
		return verifyNotRun
	}
	switch r.Status {
	case sqlite.RunPassed:
		return verifyPassed
	case sqlite.RunFailed, sqlite.RunError:
		return verifyFailed
	}
	return verifyNotRun
}

func marshalHook(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, fault.WrapInternal(err, "encode hook context")
	}
	return buf.Bytes(), enc.Close()
}

// HookStateRoot is where per-session emission hashes live, beside the database.
func HookStateRoot(dbPath string) string {
	if dbPath == ":memory:" {
		return filepath.Join(os.TempDir(), "atask-"+hookStateDirName)
	}
	return filepath.Join(filepath.Dir(dbPath), hookStateDirName)
}

// RecordHookEmission stores the hash of out for the session and reports
// whether it differs from the previous emission (true on the first one).
func (e *Engine) RecordHookEmission(sessionID string, out []byte) (bool, error) {
	name := filepath.Base(sessionID)
	if sessionID == "" || name != sessionID || name == "." || name == ".." {
		return true, nil
	}
	sum := sha256.Sum256(out)
	hash := hex.EncodeToString(sum[:])
	dir := HookStateRoot(e.store.Path())
	path := filepath.Join(dir, name)
	prev, err := os.ReadFile(filepath.Clean(path))
	switch {
	case err == nil && string(prev) == hash:
		return false, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return true, fault.WrapInternal(err, "read hook state %s", name)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return true, fault.WrapInternal(err, "create hook state directory")
	}
	if err := os.WriteFile(path, []byte(hash), 0o600); err != nil {
		return true, fault.WrapInternal(err, "write hook state %s", name)
	}
	return true, nil
}
