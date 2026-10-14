// Package project owns project identity and resolution: a project has a
// stable random ID, a mutable display name, and a registered root path used
// only to resolve "which project am I in" from a working directory.
//
// Identity is never derived from the path, a Git remote, or a folder name.
package project

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
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

// Project is the registered identity plus project-wide configuration.
type Project struct {
	ID       ID     `json:"id"`
	Name     string `json:"name"`
	RootPath string `json:"root_path,omitempty"` // canonical absolute path; "" for projects with no directory
	// PlanRev increments on every applied ChangeSet; callers pass the
	// revision they planned against for optimistic concurrency.
	PlanRev uint64 `json:"plan_rev"`
	// TargetBranch is the branch task branches start from (Git projects).
	TargetBranch string `json:"target_branch,omitempty"`
	// WorkspaceRoot holds per-attempt worktrees; "" means the default
	// state directory.
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	// MaxAttempts bounds failed attempts before a task needs attention;
	// 0 means unlimited.
	MaxAttempts int `json:"max_attempts"`
	// RetryCooldown delays re-claiming a task after a failed attempt.
	RetryCooldown time.Duration `json:"retry_cooldown_ms"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// DefaultMaxAttempts applies to new projects.
const DefaultMaxAttempts = 3

// GeneralName is the reserved name of the default project. Tasks filed
// outside a registered repo land here. Attaching a named project is
// recommended so claim can use a worktree.
const GeneralName = "general"

// IsGeneral reports whether p is the default project.
func (p Project) IsGeneral() bool { return p.Name == GeneralName && p.RootPath == "" }

// Rootless reports whether p has no repository: no root path, so no
// worktree, no target branch and no base resolution. Checks run in the
// directory the task was claimed from. general is the built-in rootless
// project; `atask project add` creates others.
func (p Project) Rootless() bool { return p.RootPath == "" }

// Validate enforces structural rules on a project record.
func (p Project) Validate() error {
	if p.Name == GeneralName {
		if p.RootPath != "" {
			return fault.New(fault.CodeInvalidInput, "project name %s is reserved for the default project, which has no root path", GeneralName)
		}
		return nil
	}
	if p.Rootless() && p.TargetBranch != "" {
		return fault.New(fault.CodeInvalidInput, "project %s has no repository, so it has no target branch", p.Name)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fault.New(fault.CodeInvalidInput, "project name must not be empty")
	}
	if len(p.Name) > 200 {
		return fault.New(fault.CodeInvalidInput, "project name must be at most 200 characters")
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
	return nil
}

// CanonicalRoot normalises a directory for registration and lookup: absolute,
// symlinks resolved, trailing separators removed. Two paths naming the same
// directory canonicalise identically.
func CanonicalRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
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
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(b))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", errors.New("unexpected .git file content")
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

// EncodeBase32 writes src as Crockford base32 (no padding). A 16-byte
// UUID encodes to 26 characters.
func EncodeBase32(src []byte) string {
	var n uint64
	var nbits int
	out := make([]byte, 0, (len(src)*8+4)/5)
	for _, b := range src {
		n = n<<8 | uint64(b)
		nbits += 8
		for nbits >= 5 {
			nbits -= 5
			out = append(out, base32Alphabet[(n>>nbits)&31])
			n &= (1 << nbits) - 1
		}
	}
	if nbits > 0 {
		out = append(out, base32Alphabet[(n<<(5-nbits))&31])
	}
	return string(out)
}

func validBase32(s string, n int) bool {
	if len(s) != n {
		return false
	}
	return validBase32Alphabet(s)
}

func validBase32Alphabet(s string) bool {
	if s == "" {
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

// ValidBase32Alphabet reports whether s is nonempty Crockford base32.
func ValidBase32Alphabet(s string) bool { return validBase32Alphabet(s) }
