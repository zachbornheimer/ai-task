package verification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Outcome is what happened when a check ran.
type Outcome string

const (
	OutcomePassed  Outcome = "passed"
	OutcomeFailed  Outcome = "failed"  // ran, non-zero exit
	OutcomeTimeout Outcome = "timeout" // killed at the deadline
	OutcomeError   Outcome = "error"   // could not run: missing binary, bad dir, workspace changed
	OutcomeSkipped Outcome = "skipped" // not run because an earlier required task check failed
)

// Evidence is the durable record of one check in one verification run. It
// names every input the result depends on so that reuse decisions and
// audits never rely on the task ID alone.
type Evidence struct {
	ID           int64     `json:"id,omitempty"`
	RunID        int64     `json:"run_id,omitempty"`
	CheckID      string    `json:"check_id"`
	CheckVersion string    `json:"check_version,omitempty"`
	CheckDigest  string    `json:"check_digest"`
	Required     bool      `json:"required"`
	Revision     string    `json:"revision,omitempty"`
	PolicyDigest string    `json:"policy_digest"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	ExitCode     int       `json:"exit_code"`
	Outcome      Outcome   `json:"outcome"`
	// Reused is true when the result was copied from earlier evidence with
	// identical inputs instead of executing the check; ReusedFrom names it.
	Reused     bool   `json:"reused"`
	ReusedFrom int64  `json:"reused_from,omitempty"`
	Message    string `json:"message,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
}

// Digest identifies the content of a check: command, directory, timeout,
// required flag, ID and version. Two checks with equal digests would do
// exactly the same thing.
func (c CheckSpec) Digest() string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Reusable decides whether prior evidence can stand in for running check c
// at revision. The rule is deliberately strict: identical check content, a
// non-empty identical revision, and a passed outcome. Failures are never
// reused (a retry after a fix always changes the revision anyway), and
// evidence without a revision is never reused because its inputs are
// unknown.
func Reusable(prior Evidence, c CheckSpec, revision string) bool {
	return revision != "" &&
		prior.Revision == revision &&
		prior.CheckDigest == c.Digest() &&
		prior.Outcome == OutcomePassed &&
		!prior.Reused // always point at the original execution
}
