# Agent contract: `at` CLI and JSON

Seven everyday verbs: `add`, `update`, `claim`, `log`, `verify`, `show`,
`list`. Project bootstrap (`init`, `project`, `projects`), `status`,
`whoami`, `version` and `help` exist outside the agent's instruction set.
The CLI is a thin adapter over the Go engine; both apply the same rules.

## Output and exit codes

`AT_OUTPUT=json` (or `--json`) makes every command print exactly one JSON
envelope on stdout and nothing else there, compact on one line (`--pretty`
indents it):

```json
{"ok":true,"result":{...}}
{"ok":false,"error":{"code":"TASK_BLOCKED","message":"...","details":{...}}}
```

Human mode prints an acknowledgement on stdout after commit and
`error [CODE]: message` on stderr. Exit status: `0` success, `1` coded
domain failure, `2` usage error. Progress never goes to JSON stdout.

## Error codes

| Code | Meaning |
|---|---|
| `INVALID_INPUT` | malformed argument, reference, or spec; `at verify` without a mode |
| `NOT_FOUND` | unknown task, key, or project |
| `NO_PROJECT` | no project for this directory; `--project` or `at init` |
| `PLAN_CONFLICT` | stale `--expect-rev`; edit to a completed or claimed contract; illegal archive |
| `DUPLICATE_KEY` | key exists with a different definition (blockers included) |
| `IDEMPOTENCY_CONFLICT` | the idempotency key was already used for a different change set |
| `DEPENDENCY_CYCLE`, `SELF_DEPENDENCY`, `DUPLICATE_DEPENDENCY`, `CROSS_PROJECT_DEPENDENCY` | hard-edge rules |
| `TASK_BLOCKED` | prerequisites incomplete (claim or complete) |
| `ALREADY_CLAIMED` | a live lease exists; on `verify`, an open attempt owns the task |
| `AWAITING_VERIFICATION`, `INTEGRATION_PENDING`, `TASK_COMPLETE` | not claimable in that state |
| `NO_ELIGIBLE_WORK` | nothing claimable now (non-waiting claim, cooldown) |
| `DONE` | every executable task is complete (waiting claim) |
| `STALLED` | open tasks remain, nothing claimable, nothing in flight, nothing cooling down; `details` carries the summary with reasons |
| `NEEDS_ATTENTION` | this task needs a planner: attempts exhausted, or its worktree could not be prepared (`attention_reason` says which) |
| `INVALID_SESSION`, `LEASE_EXPIRED`, `SESSION_SUPERSEDED`, `SESSION_FINISHED` | authority failures |
| `MISSING_VERIFICATION` | `add` without `--check`; `claim` in a project without regression checks; a check set emptied under a live claim |
| `VERIFICATION_FAILED` | checks ran and a required one did not pass; `details` is the result with evidence; counted against the attempt once |
| `VERIFICATION_RUNNING` | this attempt's `verify complete` is still running; wait for its result |
| `INTEGRATION_FAILED` | merge conflict, moved target that kept moving, or dirty target checkout |
| `WORKSPACE_DIRTY`, `WORKSPACE_UNAVAILABLE` | uncommitted changes at `verify complete` / the worktree is gone |
| `INTERNAL` | bug or I/O failure |

## Planning (planner authority)

```
at add "title" [--key K] [--group] [--parent REF] [--requires REF]* [--blocks REF]* [--cohort C]
       [--outcome ..] [--constraint ..]* [--accept ..]* [--check "[id:] cmd"]* [--optional-check ..]*
       [--expect-rev N] [--idempotency-key K] [--planner]
at update REF [--title ..] [--outcome ..] [--parent REF|""] [--cohort C|""]
       [--requires REF]* [--remove-requires REF]* [--set-requires REF]*
       [--accept ..]* [--constraint ..]* [--check ..]*
       [--archive --reason ..] [--reset-attempts] [--expect-rev N] [--planner]
```

`REF` is a task ID (`at-…`) or a key. One `add`/`update` is one atomic
plan revision; the Go `Apply` takes a whole batch; a batch that changes
nothing is not a revision. An `--idempotency-key` is bound to the exact
request: an identical replay returns the committed result (even with a
stale `--expect-rev`), a different request under the same key is
`IDEMPOTENCY_CONFLICT`, and a new request with a stale `--expect-rev` is
`PLAN_CONFLICT`. Rules:

- Every task carries at least one task check (`--check`). `add` without
  one is `MISSING_VERIFICATION`; `update --check` replaces the set but
  cannot empty it. A trivial check (`true`) is the planner's own risk.
- `B --requires A` means B cannot be claimed until A is complete. Hard
  edges form a DAG; cycles, self-edges, and cross-project edges are
  rejected and nothing is written.
- Groups (`--group`, `--parent`) are organizational: membership never
  implies an edge, groups cannot be claimed, and group progress is derived.
- `--cohort C` marks coupled verification: members implement independently
  and are judged together on one assembled candidate. It is not a blocker.
- `--blocks C` on a claimed task needs C's session token (`AT_SESSION`) or
  `--planner`; on a completed task it is rejected.
- Completed tasks cannot be edited or archived. Claimed tasks accept
  prerequisite changes with authority; contract changes need `--planner`
  and make a running verification fail closed.
- `--archive --reason ".."` is soft deletion with a recorded reason
  (shown as `archive_reason`): rejected for claimed/completed tasks and
  when live dependents or members remain unless the same batch repairs
  them.
- `add`/`update` results may carry `warnings` (keyed by task reference):
  advisory planning hints such as a check that looks like a no-op.
- There is no status flag of any kind. Status is derived.

## Execution (session authority)

```
at claim [REF] [--wait] [--lease 30m]    one atomic leased attempt; prints the token once
at claim renew [token|-]                 extend the lease (inside the worktree no token is needed)
at claim release [token|-] [--note ..] [--failed]
at log --done .. --next .. --learned .. --note ..     (token: see Token transport)
at verify task | regression | complete               (same token sources)
```

`claim` with no REF takes the oldest claimable task (stable ID tie-break;
cooldowns, exhausted tasks and tasks whose worktree could not be prepared
excluded). Eligibility is one rule: `list ready`, `status`, automatic and
explicit claims all derive it from the same facts, and a task in retry
cooldown is not claimable however its last proof ended. `claim --wait`
treats a cooldown as something to wait for (it wakes at the earliest
expiry; `status` reports `cooling` and `next_eligible_at`) and returns
`STALLED` only when nothing can proceed without a planner. A worktree that cannot be prepared marks that task
`needs_attention` with the reason and ends the attempt; the queue moves
on to the next task. `at claim <ref>` on such a task retries the
preparation (the repair path after fixing the worktree); `at update <ref>
--reset-attempts` clears the mark. It refuses with
`MISSING_VERIFICATION` while the project has no regression checks: work
that can never complete is never handed out. `--wait` blocks until work is
claimable, `DONE`, `STALLED`, or interrupted; while waiting the process
runs any pending cohort verification job it finds.

Every claim gets the task's own worktree on branch `at/<task-id>`, created
from the project's target branch on the first attempt and reused by every
later attempt, so the next attempt starts from the previous one's commits
(and sees its uncommitted edits: the claim reports `workspace_dirty`).
Worktrees are not deleted by `at`; prune them with your worktree tooling
once the task is complete.

**Lease.** The default lease is 30 minutes (minimum 5, maximum 4 hours).
Every authenticated command (`log`, `verify`, `claim renew`, a session
`add --blocks`) renews it, so an agent that logs at least every half hour
never loses its claim; the host that launched the agent renews it on the
agent's behalf (`Renew`, or `at claim renew` with the token) while the
agent is busy. When a lease does expire the task becomes claimable again;
the old token answers `LEASE_EXPIRED` (or `SESSION_SUPERSEDED` once a new
attempt exists) and the holder must stop editing. The next claim
quarantines the old worktree (see Token transport), so a holder that does
not stop can harm nothing but its own quarantined copy.

**Failure budget.** Only a genuine failed proof counts against a task: a
required check failing in `verify complete`, or `claim release --failed`.
It counts at most once per attempt (repairing and re-verifying within
the same lease adds nothing) and only for the attempt that is still the
task's current generation. Environment problems (snapshot or worktree
trouble, a dirty or moving target, a merge to resolve) and authority or
contract conflicts (superseded session, planner edit, new prerequisite)
stop the run, are reported as `last_error`, and are never a strike. A
`verify complete` fired while the attempt's previous one is still running
is refused with `VERIFICATION_RUNNING`; after completion it returns the
stored acknowledgement.

**Handoff.** The claim result carries the previous attempts' `latest_next`,
learnings and recent log, plus `inherited` learnings recorded on the
task's direct prerequisites and `last_failure`: the failed checks of the
newest failed verification run with the tail of their output. Read it
before touching the code.

`verify task` and `verify regression` run that category fresh in the
attempt's worktree. They are diagnostic: they never complete, release, or
integrate. `verify complete`:

1. refuses unless both categories have checks (`MISSING_VERIFICATION`) and
   every prerequisite is complete (`TASK_BLOCKED`);
2. requires a clean worktree and records its HEAD as the immutable
   submission (nothing is committed on your behalf);
3. runs BOTH suites fresh on a detached snapshot of that revision;
4. in `promote` projects, merges the revision onto the target branch in a
   scratch worktree, re-runs both suites on the merge when it changed
   content, and compare-and-swaps the target (rebuilding on a moved base
   up to three times);
5. in one transaction re-checks the session, prerequisites, contract
   revision, regression policy and the run's evidence, then records
   completion and ends the claim.

For a cohort member, step 2 records the submission and ends the claim;
when every peer has submitted, a verifier job assembles one candidate,
runs every member's task checks and the regression suite on it, promotes,
and completes all members atomically. The consumed token is not a cohort
credential.

A failed `verify complete` returns `VERIFICATION_FAILED` with the evidence
in `error.details`; the claim stays live for repair and the task's failure
count and cooldown are recorded. Replaying `verify complete` with the same
token after a committed completion returns the stored result.

## Reading

```
at show REF [--full]        glyph line, [STATE], DESCRIPTION, OUTCOME, ACCEPTANCE, REQUIRES, BLOCKS,
                            TASK CHECKS, VERIFICATION, ATTEMPT, SUBMISSION, HANDOFF, HISTORY
at list                     hierarchy of open work (groups indented; blocked tasks show requires:)
at list ready | blocked | all | archived
at status                   counts, claimable, active, pending cohorts, done, stalled with reasons
```

Glyphs: `○` not in progress (with `[blocked]`, `[failed]`, `[awaiting
verification]`, …), `◐` live claim or live verification, `●` complete. A
group shows `(n/m complete)` and `●` only when a nonempty member set is
complete. Tokens never appear in any read view.

## Token transport

`claim` is the only command that prints a token, and it also stores the
token inside the task worktree's private Git directory
(`.git/worktrees/<name>/at-session`, never in the tree, never in the
database, which keeps only a digest). Any `at` command run with the
worktree as its working directory finds it, planning commands included,
so an agent started inside the worktree needs no token at all. Explicit
sources win when present, in this order: a positional token, `-` (one
line of stdin, so secrets stay out of process listings), `--session`,
`AT_SESSION`, then the worktree file. A `claim` subprocess cannot set
`AT_SESSION` in its parent shell: hosts that run the agent elsewhere
capture the JSON and inject the token.

Inside a task worktree (branch `at/<task-id>`) the token must belong to
that task; a token for another task is refused with `INVALID_SESSION`
before any write, so a mis-exported `AT_SESSION` cannot act on the wrong
task.

**Takeover.** A worktree whose attempt never ended (the lease ran out
while a process may still be alive) is quarantined when the next attempt
claims: the directory is moved to `<path>.stale-<seq>` with HEAD
detached, and a fresh worktree on the same branch appears at the task
path with the new token. The stale process keeps its files, its own Git
directory and its own expired token, so it can neither act on the task
nor reach the branch or the new attempt's files; whatever it still
commits lands on a detached HEAD. The claim reports the quarantine as
`quarantined_workspace` (with `workspace_dirty` when uncommitted edits
are in there) so the new attempt or a human can salvage them. A clean
handoff (`release`, completion) reuses the worktree without quarantine.
A lost token is a stuck task until the lease expires; that is by design.

## The tiny agent prompt

```
Implement the claimed task's outcome and acceptance criteria.
Use the supplied workspace and session. You do not set task status.
Use `at log` for progress/discoveries.
Use `at verify task` and `at verify regression` while iterating.
When ready, commit the final code revision and call `at verify complete`.
It runs BOTH suites again and establishes completion only if all gates pass.
If new prerequisite work is needed, add it as a blocker of the current
claimed task (`at add "..." --blocks <task> --check "id: cmd"`; your
AT_SESSION authorises it), log the handoff, and release the claim
(`at claim release`).
Every `at` command renews your lease; `at log` at least every half hour.
Stop editing immediately if claim renewal/authorization fails.
```
