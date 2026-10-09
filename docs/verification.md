# Verification

Verification is separate from implementation. An agent's word never
completes a task; only `verify complete` can, and only with fresh evidence.

## Two required categories

- **Task checks** (`at add --check`, `at update --check`) prove the task's
  own outcome. Required unless a task is a group.
- **Project regression checks** (`at project --regression-check` /
  `--regression-json`) guard the rest of the project. Trusted
  configuration; never reachable through a session token.

Check IDs are separate namespaces; a task check whose ID matches a
regression check is rejected, and vice versa. A task missing either
category cannot complete (`MISSING_VERIFICATION`).

Each check is an argv array (no shell), optional directory, timeout
(default 30 min, stored as `timeout_ms`), and `required` flag. Policies are
canonical JSON with a `sha256:` digest; the digest in force is recorded on
every run.

## Modes

| Invocation | Inputs | Effect |
|---|---|---|
| `at verify task` | attempt worktree (editable) | runs task checks fresh; records a diagnostic run; never completes/releases/integrates |
| `at verify regression` | attempt worktree | same for regression checks |
| `at verify complete` | clean worktree → immutable HEAD → detached snapshot | runs BOTH categories fresh, integrates, finalises completion |
| `at verify` | | usage error |

Order within a run: required task checks, optional task checks, then
regression checks; regression is skipped (recorded as `skipped`) when a
required task check failed. Outcomes: `passed`, `failed`, `timeout`,
`error` (could not run), `skipped`. Only `passed` counts; optional checks
never compensate.

## No reuse

Every invocation executes its checks. Stored evidence is history for
diagnosis (`at show --full`), never proof for a new run. Checks run with
`GOFLAGS=-count=1` so Go's own test cache cannot replay a success, and
without `AT_SESSION` so a check cannot act on the task. The one exception
is an idempotent acknowledgement: `verify complete` with the same token
after a committed completion returns the stored result and runs nothing.

## Completion algorithm

1. Refuse unless both categories have checks and every prerequisite is
   complete; require a clean worktree and take HEAD as the submission.
2. Record the submission and a running run; renew the lease on a
   heartbeat while checks run.
3. Check out the revision into a detached snapshot and run both suites
   there; each evidence row commits as it finishes, fenced on the session
   and on the run still being the newest.
4. Judge. On failure: run failed, failure count and cooldown recorded,
   claim stays live, `VERIFICATION_FAILED` with evidence.
5. In `promote` projects: merge the revision onto the target in a scratch
   worktree; if the merge changed content, run both suites on the merge
   too; compare-and-swap the target (rebuild on a moved base up to three
   times). Conflicts and dirty target checkouts fail with
   `INTEGRATION_FAILED` and the target never moves.
6. One transaction re-checks the session, prerequisites, the task's
   contract revision, the project regression digest, and that stored
   evidence proves the final revision; then marks complete and ends the
   claim.

## Cohorts

Tasks sharing `--cohort C` implement independently. `verify complete` on a
member records its submission and ends its claim. When every live member
has submitted, a verifier job (durable, leased, owned by a fresh token)
assembles one candidate by merging every member revision onto the target,
runs each member's task checks and the regression suite on it, promotes,
and completes all members in one transaction. Failing member checks send
that member back for repair while passing peers keep waiting; a failing
regression sends every member back. A verifier that dies leaves a job
whose lease expires; `Claim(wait)` in any worker reconciles and retries it,
so no hidden command is needed.

Cohorts are not hard edges and must not be used where A truly cannot be
implemented before B; that is a design problem a cohort cannot fix. Prefer
an explicit integration task that `--requires` independently verifiable
implementation tasks whenever possible.
