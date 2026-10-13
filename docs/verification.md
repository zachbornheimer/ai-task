# Verification

Verification is separate from implementation. An agent's word never
completes a task; only `verify complete` can, and only with fresh evidence.

## Two required categories

- **Task checks** (`atask add --proof`, `atask update --proof`) prove the task's
  own outcome. Required unless a task is a group.
- **Project regression checks** (`atask project --regression-check` /
  `--regression-json`) guard the rest of the project. Trusted
  configuration; never reachable through a session token.

Check IDs are separate namespaces; a task check whose ID matches a
regression check is rejected, and vice versa. A task missing either
category cannot complete (`MISSING_VERIFICATION`).

Each check is an argv array (no shell), optional directory, timeout
(default 30 min, stored as `timeout_ms`), and `required` flag. Policies are
canonical JSON with a `sha256:` digest; the digest in force is recorded on
every run.

Because there is no shell, a check written as `[id:] command args...` that
has a bare `&&`, `||`, `|`, `;`, `>`, `>>` or `<` among its words is
rejected (`INVALID_INPUT`); wrap it as `sh -c '...'`. Quoted operators
inside an argument are fine. So is anything only a shell expands, quoted
or not: `$(...)`, backticks, `$NAME` and `${NAME}` are refused the same
way (`test -z "$(gofmt -l .)"` would test the literal text), except in the
script of `sh -c` (or bash, zsh, dash, ksh). A bare `$`, as in a `-run`
regexp, and awk's `$1` are not expansions.

## Modes

| Invocation                | Inputs                                              | Effect                                                                                |
| ------------------------- | --------------------------------------------------- | ------------------------------------------------------------------------------------- |
| `atask verify proof`      | attempt worktree (editable)                         | runs task checks fresh; records a diagnostic run; never completes/releases/integrates |
| `atask verify regression` | attempt worktree                                    | same for regression checks                                                            |
| `atask verify complete`   | clean worktree → immutable HEAD → detached snapshot | runs BOTH categories fresh, integrates, finalises completion                          |
| `atask verify`            |                                                     | usage error                                                                           |

Order within a run: required task checks, optional task checks, then
regression checks; regression is skipped (recorded as `skipped`) when a
required task check failed. Outcomes: `passed`, `failed`, `timeout`,
`error` (could not run), `skipped`. Only `passed` counts; optional checks
never compensate.

## Run logs

Evidence keeps the head and tail of each stream (32 KiB each), which can
drop the middle of a long run, where `go test` prints a `--- FAIL`
among the passes. So every run also writes a directory beside the
database, `logs/<project>/run-<id>/`: one `<check>.log` per executed
check with its complete stdout and stderr, interleaved in arrival order
between a header (the command, directory and revision) and a footer (the
outcome), and `result.json` holding the verify result in the shape of the
JSON envelope. The result carries the directory as `log_dir` and each
evidence row its file as `log`; the human output names a failing check's
log and the directory, and the next attempt's handoff names the failing
checks' logs. Each project keeps its newest 100 run directories; a
missing log never fails a run.

## No reuse

Every invocation executes its checks. Stored evidence is history for
diagnosis (`atask show --full`), never proof for a new run. Checks run with
`GOFLAGS=-count=1` so Go's own test cache cannot replay a success, and
without `ATASK_SESSION` so a check cannot act on the task. The one exception
is an idempotent acknowledgement: `verify complete` with the same token
after a committed completion returns the stored result and runs nothing.

## Completion algorithm

1. Refuse unless both categories have checks and every prerequisite is
   complete; require a clean worktree and take HEAD as the submission.
2. Record the submission and a running run; renew the lease on a
   heartbeat while checks run.
3. Run the task checks on a detached snapshot of the revision, then the
   regression checks on a second, fresh snapshot (skipped, and recorded as
   such, when a required task check failed): nothing a task check leaves
   behind can be why a regression check passes. Each snapshot has a private
   `TMPDIR`; `ATASK_SESSION` is never in a check's environment. Each evidence
   row commits as it finishes, fenced on the session and on the run still
   being the newest.
4. Judge. On failure: run failed, failure count and cooldown recorded,
   claim stays live, `VERIFICATION_FAILED` with evidence.
5. In `promote` projects: merge the revision onto the target in a scratch
   worktree; if the merge changed content, run both suites on the merge
   too. Conflicts fail with `INTEGRATION_FAILED` and the target never
   moves. The merge commit (`at: integrate <task> (<title>)`, with an
   `At-Task: <id>` trailer per merged task) is recorded as the Git
   identity of whoever runs the verification, from the repository's
   configuration or `GIT_COMMITTER_*`, as `git merge` would; without one
   it fails with `WORKSPACE_UNAVAILABLE` naming the fix, never a strike.
6. **Intent.** One short transaction re-checks the session, prerequisites,
   the task's contract revision, the project regression digest, archive
   state, submission identity, and that stored evidence proves the
   candidate; then records an integration intent (task, attempt, run,
   submission, contract revision, regression digest, target, base,
   candidate). The task is now `awaiting_integration`: it cannot be
   claimed, edited, blocked or archived until completion is recorded.
7. **Promote.** Compare-and-swap the target under the repository's
   promotion lock (an advisory file lock owned by the process's descriptor,
   released by the kernel if the process dies; nothing is ever "stolen").
   When the target is checked out, its working tree may be dirty: the
   fast-forward proceeds unless it would touch a path with local changes
   or an untracked file, and then refuses naming the paths; local files
   are never overwritten, stashed or discarded.
   A moved target abandons the intent and rebuilds (up to three times);
   any other failure abandons it and the target never moved.
8. **Complete.** A second short transaction, authorised by the intent's
   owner rather than the lease, marks complete, ends the claim and records
   the integration.
9. **Recovery.** A process killed between 6 and 8 leaves an open intent.
   When its owner's lease expires, any engine reconciles from Git: if the
   candidate is an ancestor of the target the promotion happened and the
   task is completed (an exact retry of `verify complete` returns the
   stored acknowledgement); otherwise the intent is abandoned, the run is
   recorded as interrupted, and the task is claimable with no failure
   counted. Projects without promotion complete in step 6 directly.

No SQLite transaction is ever held across a Git command or a check
process.

## Cohorts

Tasks sharing `--cohort C` implement independently. `verify complete` on a
member records its submission and ends its claim. When every live member
has submitted, a verifier job (durable, leased, owned by a fresh token)
assembles one candidate by merging every member revision onto the target,
runs each member's task checks and the regression suite on it, pins one
intent per member, promotes under the same lock, and completes all
members in one transaction; the same recovery applies (a dead verifier's
job lease expires, Git decides, members complete or return to pending for
the next verifier). Failing member checks send
that member back for repair (one counted failure) while passing peers keep
waiting; a failing regression sends every member back with the evidence
and charges nobody; a late conflict on one member sends only that member
back; a promotion that cannot land keeps every submission and retries
with a capped exponential backoff. A verifier that dies leaves a job whose
lease expires (`interrupted`, not an error: no backoff); `Claim(wait)` in
any worker reconciles and retries it, so no hidden command is needed. A
submitted member's contract is pinned until the planner withdraws the
submission (`--planner --withdraw-submission`). A consumed token's
`verify complete` is an idempotent acknowledgement.

Cohorts are not hard edges and must not be used where A truly cannot be
implemented before B; that is a design problem a cohort cannot fix. Prefer
an explicit integration task that `--requires` independently verifiable
implementation tasks whenever possible.

## Budgets: checks must be fast

A task declares a size (`--size small|medium|large`, default small) and
the project's budgets turn it into a cap on the task-check suite (30s,
5m, 15m by default); the regression suite has its own cap (2m). The suite
shares the cap: a check that would start after it is spent is recorded as
`timed_out` without running. A timeout is its own outcome and result
code, `VERIFICATION_TIMEOUT`, and is never counted against the attempt:
it says the verification was built too slow for the task's size (or the
code hangs), not that the software is wrong. The message says which
budget and whose problem: a task-check timeout names the two fixes
(narrow with `-run` so the check proves only this outcome, or set
`--size medium|large`); a regression timeout is the planner's (narrow
the gate, raise `--regression-budget`). A small task whose check is
`go test` without `-run` is rejected at add/update, because a package
suite routinely outruns the 30s budget. Checks that pass using more than half their
budget are reported as warnings, because CI machines are slower. `at
doctor` times the regression suite on the target branch against the
budget. Checks receive `ATASK_CHANGED_FILES`, a file listing the paths the
candidate changed against the target, and `ATASK_TARGET_BRANCH`, so a
regression gate can run only affected tests and leave the full suite to
CI. The defaults follow Google's test sizes and Bazel's size-implied
timeouts, tightened for an agent's feedback loop.

## Epics: the integration tier

A group with checks is verified once on the target branch when all its
members are complete, under its own size budget (large, 15m, by default),
as a durable job run by whichever worker has capacity or by `atask verify
groups`. Task checks prove one outcome in seconds, the regression gate
guards the rest in two minutes, and the epic's checks are where the slow,
whole-system verification lives, paid once per epic rather than once per
task. A failed epic stalls the plan with its reason and is answered with
follow-up member tasks (which reopen the group) or a corrected check; it
does not rerun on the same revision and contract by itself.
