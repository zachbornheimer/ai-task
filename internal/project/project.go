// Package project owns project identity and resolution: a project has a
// stable random ID, a mutable display name, and a registered root path used
// only to resolve "which project am I in" from a working directory.
//
// Identity is never derived from the path, a Git remote, or a folder name.
package project

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// ID is an opaque project identifier of the form "proj-" + 16 base32 chars.
type ID string

const idPrefix = "proj-"

// NewID draws a fresh random ID. Collisions are rejected by the store's
// UNIQUE constraint and retried by the caller.
func NewID() ID {
	return ID(idPrefix + randomBase32(rand.Reader, 16))
}

// ParseID validates the external form.
func ParseID(s string) (ID, error) {
	if !strings.HasPrefix(s, idPrefix) || !validBase32(strings.TrimPrefix(s, idPrefix), 16) {
		return "", fault.New(fault.CodeInvalidInput, "invalid project id %q", s)
	}
	return ID(s), nil
}

// IntegrationPolicy states what "the output of a completed task is usable by
// dependents" means for the project.
type IntegrationPolicy string

const (
	// IntegrationNone: a verified submission is complete. This is the only
	// policy implemented in Milestone 1 and the right one for projects whose
	// tasks do not produce Git revisions.
	IntegrationNone IntegrationPolicy = "none"
	// IntegrationPromote: a verified revision must additionally be promoted
	// into the project's target branch before the task is complete and before
	// dependents are released (Milestone 3).
	IntegrationPromote IntegrationPolicy = "promote"
)

// Project is the registered identity plus project-wide configuration.
type Project struct {
	ID          ID                `json:"id"`
	Name        string            `json:"name"`
	RootPath    string            `json:"root_path,omitempty"` // canonical absolute path; "" for projects with no directory
	Integration IntegrationPolicy `json:"integration"`
	// Regression is the project-wide check list merged into every
	// submission's effective policy.
	Regression []verification.CheckSpec `json:"regression,omitempty"`
	// PlanRev increments on every applied ChangeSet; callers pass the
	// revision they planned against for optimistic concurrency.
	PlanRev uint64 `json:"plan_rev"`
	// TargetBranch is where verified work is promoted (Git projects).
	TargetBranch string `json:"target_branch,omitempty"`
	// WorkspaceRoot holds per-attempt worktrees; "" means the default
	// state directory.
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	// MaxAttempts bounds failed attempts before a task needs attention;
	// 0 means unlimited.
	MaxAttempts int `json:"max_attempts"`
	// RetryCooldown delays re-claiming a task after a failed attempt.
	RetryCooldown time.Duration `json:"retry_cooldown_ms"`
	// Budgets cap verification time: a task's checks by its size, the
	// regression suite by the project. Zero means the built-in default,
	// Unlimited disables the cap.
	Budgets   Budgets   `json:"budgets"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Budgets are verification time caps. Stored values: 0 = built-in default,
// Unlimited = no cap.
type Budgets struct {
	Small      time.Duration `json:"small_ms"`
	Medium     time.Duration `json:"medium_ms"`
	Large      time.Duration `json:"large_ms"`
	Regression time.Duration `json:"regression_ms"`
}

// Unlimited marks a budget with no cap.
const Unlimited time.Duration = -1

// Built-in budgets, after Google's test sizes and Bazel's timeouts, tuned
// for an agent's feedback loop: a small task proves itself in half a
// minute, the regression gate in two, and anything slower is a medium or
// large task by declaration.
const (
	DefaultSmallBudget      = 30 * time.Second
	DefaultMediumBudget     = 5 * time.Minute
	DefaultLargeBudget      = 15 * time.Minute
	DefaultRegressionBudget = 2 * time.Minute
)

// ForSize is the effective task-check budget for a size (0 = no cap).
func (b Budgets) ForSize(size string) time.Duration {
	switch size {
	case "medium":
		return effective(b.Medium, DefaultMediumBudget)
	case "large":
		return effective(b.Large, DefaultLargeBudget)
	}
	return effective(b.Small, DefaultSmallBudget)
}

// RegressionBudget is the effective regression-suite budget (0 = no cap).
func (b Budgets) RegressionBudget() time.Duration {
	return effective(b.Regression, DefaultRegressionBudget)
}

func effective(v, def time.Duration) time.Duration {
	switch {
	case v == Unlimited:
		return 0
	case v <= 0:
		return def
	}
	return v
}

// DefaultMaxAttempts applies to new projects.
const DefaultMaxAttempts = 5

// Validate enforces structural rules on a project record.
func (p Project) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fault.New(fault.CodeInvalidInput, "project name must not be empty")
	}
	if len(p.Name) > 200 {
		return fault.New(fault.CodeInvalidInput, "project name must be at most 200 characters")
	}
	switch p.Integration {
	case IntegrationNone, IntegrationPromote:
	default:
		return fault.New(fault.CodeInvalidInput, "unknown integration policy %q", p.Integration)
	}
	if p.RootPath != "" && !filepath.IsAbs(p.RootPath) {
		return fault.New(fault.CodeInvalidInput, "project root must be absolute: %q", p.RootPath)
	}
	if p.WorkspaceRoot != "" && !filepath.IsAbs(p.WorkspaceRoot) {
		return fault.New(fault.CodeInvalidInput, "workspace root must be absolute: %q", p.WorkspaceRoot)
	}
	if p.MaxAttempts < 0 || p.RetryCooldown < 0 {
		return fault.New(fault.CodeInvalidInput, "max attempts and retry cooldown must not be negative")
	}
	return (verification.Policy{Regression: p.Regression}).Validate()
}

// CanonicalRoot normalises a directory for registration and lookup: absolute,
// symlinks resolved, trailing separators removed. Two paths naming the same
// directory canonicalise identically.
func CanonicalRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

// LocateRoot finds the directory a working directory belongs to for the
// purpose of project resolution.
//
// If dir is inside a Git repository, the result is the repository's main
// worktree root, so every linked worktree (including those Worktrunk creates
// elsewhere on disk) resolves to the same project. Otherwise the result is
// "" and the caller falls back to registered-root prefix matching.
//
// Git is not invoked: a ".git" directory marks the main worktree; a ".git"
// file ("gitdir: <path>") marks a linked worktree whose gitdir lives under
// "<common>/.git/worktrees/<name>".
func LocateRoot(dir string) (string, error) {
	cur, err := CanonicalRoot(dir)
	if err != nil {
		return "", err
	}
	for {
		dotGit := filepath.Join(cur, ".git")
		fi, err := os.Lstat(dotGit)
		if err == nil {
			if fi.IsDir() {
				return cur, nil
			}
			if fi.Mode().IsRegular() {
				root, err := rootFromGitFile(dotGit)
				if err == nil {
					return root, nil
				}
				// Unreadable or unexpected .git file: treat as the root itself.
				return cur, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", nil
		}
		cur = parent
	}
}

func rootFromGitFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(b))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("unexpected .git file content")
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(path), gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	// Linked worktree: <common>/.git/worktrees/<name>
	wt := filepath.Dir(gitdir)
	if filepath.Base(wt) != "worktrees" {
		return "", fmt.Errorf("not a linked worktree gitdir: %s", gitdir)
	}
	common := filepath.Dir(wt) // <common>/.git
	return CanonicalRoot(filepath.Dir(common))
}

// MatchRoot picks the registered root that contains dir, preferring the
// longest match. It returns "" when no registered root contains dir.
func MatchRoot(dir string, roots []string) string {
	best := ""
	for _, r := range roots {
		if r == "" {
			continue
		}
		if dir == r || strings.HasPrefix(dir, r+string(filepath.Separator)) {
			if len(r) > len(best) {
				best = r
			}
		}
	}
	return best
}

// Base32 alphabet shared by every ID type: Crockford's set in lower case
// (digits plus letters without i, l, o, u) so IDs are unambiguous when read
// aloud or retyped. 16 characters carry 80 bits of entropy.
const base32Alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

func randomBase32(r io.Reader, n int) string {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = base32Alphabet[int(b)&31]
	}
	return string(out)
}

func validBase32(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range []byte(s) {
		if strings.IndexByte(base32Alphabet, c) < 0 {
			return false
		}
	}
	return true
}

// RandomBase32 and ValidBase32 are exported for sibling identity types
// (task IDs, session tokens) so that one alphabet is defined once.
func RandomBase32(n int) string        { return randomBase32(rand.Reader, n) }
func ValidBase32(s string, n int) bool { return validBase32(s, n) }

// MarshalJSON writes budgets in milliseconds, matching the field names;
// Unlimited is -1.
func (b Budgets) MarshalJSON() ([]byte, error) {
	type wire struct {
		Small      int64 `json:"small_ms"`
		Medium     int64 `json:"medium_ms"`
		Large      int64 `json:"large_ms"`
		Regression int64 `json:"regression_ms"`
	}
	return json.Marshal(wire{budgetMS(b.Small), budgetMS(b.Medium), budgetMS(b.Large), budgetMS(b.Regression)})
}

// UnmarshalJSON reads the millisecond form.
func (b *Budgets) UnmarshalJSON(data []byte) error {
	var w struct {
		Small      int64 `json:"small_ms"`
		Medium     int64 `json:"medium_ms"`
		Large      int64 `json:"large_ms"`
		Regression int64 `json:"regression_ms"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*b = Budgets{Small: budgetFromMS(w.Small), Medium: budgetFromMS(w.Medium), Large: budgetFromMS(w.Large), Regression: budgetFromMS(w.Regression)}
	return nil
}

func budgetMS(d time.Duration) int64 {
	if d == Unlimited {
		return -1
	}
	return d.Milliseconds()
}

func budgetFromMS(v int64) time.Duration {
	if v < 0 {
		return Unlimited
	}
	return time.Duration(v) * time.Millisecond
}
