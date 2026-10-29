// Package project owns planning identity: a project is a board of related
// tasks with a stable random ID and a mutable display name. A project names
// no directory, no repository and no branch; the repository a task's work
// runs in is recorded on the task (see task.RepoRoot).
//
// Identity is never derived from a path, a Git remote, or a folder name.
package project

import (
	"crypto/rand"
	"io"
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

// Project is the planning identity plus project-wide retry policy.
type Project struct {
	ID   ID     `json:"id"`
	Name string `json:"name"`
	// PlanRev increments on every applied ChangeSet; callers pass the
	// revision they planned against for optimistic concurrency.
	PlanRev uint64 `json:"plan_rev"`
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
// without a project land here.
const GeneralName = "general"

// NameKey is the comparison key for a project name: lowercase, then every
// character that is not an ASCII letter or digit removed. "AI Tooling" and
// "ai-tooling" are the same key. An empty key matches nothing.
func NameKey(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IsGeneral reports whether p is the default project.
func (p Project) IsGeneral() bool { return p.Name == GeneralName }

// Validate enforces structural rules on a project record.
func (p Project) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fault.New(fault.CodeInvalidInput, "project name must not be empty")
	}
	if len(p.Name) > 200 {
		return fault.New(fault.CodeInvalidInput, "project name must be at most 200 characters")
	}
	if p.MaxAttempts < 0 || p.RetryCooldown < 0 {
		return fault.New(fault.CodeInvalidInput, "max attempts and retry cooldown must not be negative")
	}
	return nil
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
