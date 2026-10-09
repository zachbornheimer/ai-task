# Agent contract: `at` CLI and JSON

Seven everyday verbs: `add`, `update`, `claim`, `log`, `verify`, `show`,
`list`. Project bootstrap (`init`, `project`, `projects`), `status`,
`whoami`, `version` and `help` exist outside the agent's instruction set.
The CLI is a thin adapter over the Go engine; both apply the same rules.

## Output and exit codes

`AT_OUTPUT=json` (or `--json`) makes every command print exactly one JSON
envelope on stdout and nothing else there:

```json
{"ok": true,  "result": {...}}
{"ok": false, "error": {"code": "TASK_BLOCKED", "message": "...", "details": {...}}}
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
| `DUPLICATE_KEY` | key exists with a different definition |
| `DEPENDENCY_CYCLE`, `SELF_DEPENDENCY`, `DUPLICATE_DEPENDENCY`, `CROSS_PROJECT_DEPENDENCY` | hard-edge rules |
| `TASK_BLOCKED` | prerequisites incomplete (claim or complete) |
| `ALREADY_CLAIMED` | a live lease exists; on `verify`, an open attempt owns the task |
| `AWAITING_VERIFICATION`, `INTEGRATION_PENDING`, `TASK_COMPLETE` | not claimable in that state |
| `NO_ELIGIBLE_WORK` | nothing claimable now (non-waiting claim, cooldown) |
| `DONE` | every executable task is complete (waiting claim) |
| `STALLED` | open tasks remain, nothing claimable, nothing in flight; `details` carries the summary with reasons |
| `INVALID_SESSION`, `LEASE_EXPIRED`, `SESSION_SUPERSEDED`, `SESSION_FINISHED` | authority failures |
| `MISSING_VERIFICATION` | a required category has no checks; completion fails closed |
| `VERIFICATION_FAILED` | checks ran and a required one did not pass; `details` is the result with evidence |
| `INTEGRATION_FAILED` | merge conflict, moved target that kept moving, or dirty target checkout |
| `WORKSPACE_DIRTY`, `WORKSPACE_UNAVAILABLE` | uncommitted changes / no repository or worktree |
| `INTERNAL` | bug or I/O failure |

## Planning (planner authority)

```
at add "title" [--key K] [--group] [--parent REF] [--requires REF]* [--blocks REF]* [--cohort C]
       [--outcome ..] [--constraint ..]* [--accept ..]* [--check "[id:] cmd"]* [--optional-check ..]*
       [--expect-rev N] [--idempotency-key K] [--planner]
at update REF [--title ..] [--outcome ..] [--parent REF|""] [--cohort C|""]
       [--requires REF]* [--remove-requires REF]* [--set-requires REF]*
       [--accept ..]* [--constraint ..]* [--check ..]* [--clear-checks]
       [--archive] [--reset-attempts] [--expect-rev N] [--planner]
```

`REF` is a task ID (`at-…`) or a key. One `add`/`update` is one atomic
plan revision; the Go `Apply` takes a whole batch. Rules:

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
- `--archive` is soft deletion: rejected for claimed/completed tasks and
  when live dependents or members remain unless the same batch repairs
  them.
- There is no status flag of any kind. Status is derived.

## Execution (session authority)

```
at claim [REF] [--wait] [--lease 1h]     one atomic leased attempt; prints the token once
at claim renew <token|->                  token mandatory
at claim release <token|-> [--note ..] [--failed]
at log --done .. --next .. --learned .. --note ..     (AT_SESSION, --session, positional, or -)
at verify task | regression | complete               (same token sources)
```

`claim` with no REF takes the oldest claimable task (stable ID tie-break;
cooldowns and exhausted tasks excluded). `--wait` blocks until work is
claimable, `DONE`, `STALLED`, or interrupted; while waiting the process
runs any pending cohort verification job it finds. A claim in a Git
project gets a private worktree on branch `at/<task>/<n>` (continuing the
previous attempt's branch when one exists).

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

`claim` is the only command that prints a token. A `claim` subprocess
cannot set `AT_SESSION` in its parent shell: the host captures the JSON
and injects the token into the agent's environment. `-` reads one line
from stdin so secrets stay out of process listings.

## The tiny agent prompt

```
Implement the claimed task's outcome and acceptance criteria.
Use the supplied workspace and session. You do not set task status.
Use `at log` for progress/discoveries.
Use `at verify task` and `at verify regression` while iterating.
When ready, commit the final code revision and call `at verify complete`.
It runs BOTH suites again and establishes completion only if all gates pass.
If new prerequisite work is needed, add it as a blocker of the current
claimed task (`at add "..." --blocks <task>`), log the handoff, and
release the claim with its token (`at claim release -`).
Stop editing immediately if claim renewal/authorization fails.
```
