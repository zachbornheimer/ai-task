# Agent contract: `atask` CLI and JSON

Nine everyday verbs: `add`, `update`, `claim`, `log`, `verify`, `show`,
`list`, `help`, `version`. `atask context` with no arguments is the
operating contract (no project or database). `atask context ID` is the
worker handoff for that task. Project bootstrap (`init`, `project`,
`projects`), `status`, `doctor`, `prune` and `hook` exist outside that set.
The CLI is a thin adapter over the Go engine; both apply the same rules.
`whoami`, `ready` and `comment` are not commands: each fails as an unknown command rather than aliasing `show`, `list` or `log`.

## Output and exit codes

`ATASK_OUTPUT=json` (or `--json`) makes every command print exactly one JSON
envelope on stdout and nothing else there, compact on one line (`--pretty`
indents it):

````json
{"ok":true,"result":{...}}
{"ok":false,"error":{"code":"NOT_READY","message":"...","details":{...}}}
```text

Human mode prints an acknowledgement on stdout after commit and
`error [CODE]: message` on stderr. Exit status: `0` success, `1` coded
domain failure, `2` usage error. Progress never goes to JSON stdout.
`atask <command> --help` (or `-h`) prints the command's usage and flags on
stdout (an envelope in JSON mode) and exits `0`.

## Error codes

| Code                   | Meaning |
| ---------------------- | ------- |
| `INVALID_INPUT`        | malformed argument, reference, spec, or dependency edge (cycle, self, duplicate, cross-project); `atask verify` without a mode; `claim` in a project without regression checks, or a check set emptied under a live claim |
| `NOT_FOUND`            | unknown task, key, project, or no project for this directory (`--project` or `atask init`) |
| `CONFLICT`             | the request clashes with current state: stale `--expect-rev`, edit to a completed or claimed contract, illegal archive, key reused with a different definition, task already claimed (live lease) or complete, `verify complete` still running for this attempt |
| `INVALID_SESSION`      | the token is unknown, expired, superseded, or finished: stop editing and `atask claim <task>` again |
| `NOT_READY`            | not possible right now: prerequisites incomplete, task awaiting verification or integration, nothing claimable (non-waiting claim, cooldown), uncommitted changes or missing worktree, a cancelled call, a failed doctor check, or an I/O failure; the message says which |
| `BASE_CONFLICT`        | the target branch moved or the work does not merge onto it, including a group whose children' revisions conflict (`details.conflicts` lists the files; merge the target into the task branch, commit, verify again) |
| `CHECK_FAILED`         | checks ran and a required one did not pass or ran out of its budget; `details` is the result with evidence. A failed check counts against the attempt once; a timeout (`timed_out` evidence) never does, since the verification is built too slow for the size or the code hangs |
| `IDEMPOTENCY_CONFLICT` | the idempotency key was already used for a different change set |
| `DONE`                 | every executable task is complete (waiting claim) |
| `STALLED`              | open tasks remain, nothing claimable, nothing in flight, nothing cooling down (`details` carries the summary with reasons); also one task that needs a planner: attempts exhausted or its worktree could not be prepared (`attention_reason` says which) |

## Planning (planner authority)
````

atask add "title" [--key K] [--group] [--parent REF] [--requires REF]*[--blocks REF]*
[--outcome ..] [--constraint ..]*[--accept ..]* [--proof "PRESET: SUBJECT" --regression "PRESET: SUBJECT"]
(groups: --proof and --regression, same as tasks)
[--on-complete "PRESET: SUBJECT"]*[--on-complete-detach "PRESET: SUBJECT"]*
[--pin PATH]*[--claim]
[--expect-rev N] [--idempotency-key K]
atask update REF [--title ..] [--outcome ..] [--parent REF|""]
[--requires REF]* [--remove-requires REF]*[--set-requires REF]*
[--accept ..]*[--constraint ..]* [--proof ..] [--regression ..] [--on-complete ..]*[--on-complete-detach ..]* [--pin PATH|""]*
[--archive --reason ..] [--reset-attempts] [--expect-rev N]

```text
`REF` is a task ID (`at-…`) or a key. One `add`/`update` is one atomic
plan revision; the Go `Apply` takes a whole batch; a batch that changes
nothing is not a revision. `atask add -f plan.yaml` (or `.json`, or `-` for
stdin) and `atask add --plan '…'` apply a whole plan in one revision: one
item, a list, or `{tasks: [...]}`, each item with the field names `atask
show` prints (`kind` task|group, `key`, `title`, `outcome`, `parent`,
`constraints`, `acceptance`, `requires`, `blocks`, `checks`,
`optional_checks`; a check is `"[id:] cmd args"` or `{id, command (string
or argv), dir, timeout_ms, required}`). A bad item rejects the whole
file. `--claim` on a single-task add claims it at once and prints the
session beside the plan result: the fast path for a small change.

`atask doctor` checks a project before planning or
starting workers: repository and target branch resolve, the target
checkout has no uncommitted or untracked files (a warning: checks and
worktrees run on the committed revision), a Git identity
exists for commits and integration merges (an error without one), the
workspace root is writable, quarantined worktrees, tasks needing
attention, a stalled plan, and Worktrunk's commit-message setup when
`wt` is installed. An error-severity finding makes it `NOT_READY` with
the report in `details`; JSON carries the report either way. An `--idempotency-key` is bound to the exact
request: an identical replay returns the committed result (even with a
stale `--expect-rev`), a different request under the same key is
`IDEMPOTENCY_CONFLICT`, and a new request with a stale `--expect-rev` is
`CONFLICT`. Rules:

- Every task carries both a proof (`--proof "PRESET: SUBJECT"`, proves
  the task works) and a regression (`--regression "PRESET: SUBJECT"`, proves
  nothing else broke; `--size` sets its time class). A task missing either one is a draft: `add` creates it, `list draft` shows it with `◌`, and `claim` is `NOT_READY` naming `--proof` and `--regression`. Never claim a draft; supply both with `update` and it becomes ready. `--check` on a task is
  `INVALID_INPUT` naming `--proof`. `update --proof` / `--regression`
  replace either one. `atask show` lists both under TASK CHECKS and
  `show --json` carries `proof` and `regression` objects
  (`preset`, `subject`, `options`, `size`). `run: COMMAND` is the raw-command
  preset. A trivial command (`run: true`) is the planner's own risk.
- `--on-complete "PRESET: SUBJECT"` (waited on, 900 s budget) and
  `--on-complete-detach "PRESET: SUBJECT"` (spawned, not waited on) name
  actions that run after the task completes, in the order given, in the
  task's worktree. They use the proof grammar; the vacuity rule does not
  apply. `update` with either flag replaces the whole list. Completion is
  atomic and happens first: the same transaction records each action as
  `pending`, then atask runs them. A waited action records a RunResult
  (`check_type: on_complete`) and ends `succeeded` or `failed`; a detached
  one records its start time (no return code) and ends `detached`. A
  failed action never undoes completion. `show --full` and `show --json`
  list `actions` (`spec`, `status`, `attempts`, `run`); `verify complete`
  returns them too.
- `verify complete` on an already complete task runs only the actions that
  are `pending`, `failed`, or left `running` by a crash; `succeeded` and
  `detached` actions never run again. `verify complete --reverify` runs
  the proof and regression again against the revision that completed the
  task (in snapshots), appends the new RunResults, and leaves the task
  complete; it does not run actions.
- `--size small|medium|large` (default small) declares how long the
  task's checks may take; the proof package turns it into seconds
  (60s, 300s, 900s). A project holds no budgets: `atask project`
  rejects `--regression-check`, `--integration` and the budget flags. The
  whole suite shares the budget: a check that would start after it is
  spent is recorded as `timed_out` without running. A small task whose
  check is `go test` without `-run` is rejected (`INVALID_INPUT`);
  narrow with `-run` or set `--size medium|large`. `verify` results
  carry `warnings` for checks that passed using more than half their
  budget. Checks run with `ATASK_CHANGED_FILES` (a file listing the paths the
  candidate changed against the target) and `ATASK_TARGET_BRANCH`, so a
  regression check can run only affected tests.
- `--pin PATH` names files, or directories by prefix, the task may not
  change: test files and check scripts it does not own, generated files,
  lockfiles. `verify complete` refuses a submission that touched one
  (`CHECK_FAILED`, counted as the attempt's failure, before any
  check runs), so an agent cannot pass by weakening the checks. Pins are
  part of the contract (`pins` in `atask show` and in the context); `update
  --pin ""` clears them.
- `B --requires A` means B cannot be claimed until A is complete. Hard
  edges form a DAG; cycles, self-edges, and cross-project edges are
  rejected and nothing is written.
- Groups (`--group`, `--parent`) are organizational: membership never
  implies an edge, groups cannot be claimed, and group progress is derived.
  A group may carry its own proofs (`--group --proof ... --regression ...`,
  `--size`, large by default). They run on the merge of every child's
  completed revision, built in a scratch worktree: no integration branch
  exists. `atask verify complete GROUP` runs them, or a worker waiting in
  `atask claim --wait` does. Before every child is complete it fails with
  `NOT_READY`; children whose revisions cannot be merged fail it with
  `BASE_CONFLICT`, naming the files. The group is complete (●) only after
  its proofs pass, and `status` counts a waiting group as active
  (`pending_epics`) and a failed one as a stall with the reason; nothing
  is charged to any child. Adding or moving a child into a completed
  group, or changing its proofs, reopens it. A timeout is the group's
  construction problem, reported on the group.
- `--blocks C` on a completed task is rejected.
- Completed tasks cannot be edited or archived. Planner authority is the absence of a
  claimed task's own session token: an `update` that changes its proofs,
  size, requires, or archive state succeeds without that token (no
  `ATASK_SESSION`, not inside its worktree) and fails with `INVALID_SESSION`
  ("a worker may not rewrite its own contract") with it. Another task's
  token does not count as the worker's. Such a change makes a running
  verification fail closed.
- `--archive --reason ".."` is soft deletion with a recorded reason
  (shown as `archive_reason`): rejected for claimed/completed tasks and
  when live dependents or unfinished members remain unless the same batch
  repairs them; a group whose members are all complete can be archived
  and keeps their history (`list archived` shows it). `--archive` is not
  combined with other edits, and `--group` takes only `--key`/`--parent`;
  both are usage errors otherwise.
- `--reset-attempts` restarts a retry budget and ends a dead attempt,
  returning the task to `ready`. It reports `changed: 0` when there was
  nothing to reset, and is planner authority like any contract edit.
- `add`/`update` results may carry `warnings` (keyed by task reference):
  advisory planning hints such as a check that looks like a no-op.
- There is no status flag of any kind. Status is derived.

## Execution (session authority)
```

atask context operating contract (no project or database)
atask context ID worker handoff for that task
atask help add syntax, plus every proof preset: subject, options, default size
atask show [REF] current claim, or a particular task
atask claim [REF] [--wait] [--lease 30m] one atomic leased attempt; prints the token once
atask claim renew [token|-] extend the lease (inside the worktree no token is needed)
atask claim release [token|-] [--note ..] [--failed]
atask log --done .. --next .. --learned .. --note .. (token: see Token transport)
atask verify proof | regression | complete [--reverify] (same token sources)

```text
`claim` with no REF takes the oldest claimable task (stable ID tie-break;
cooldowns, exhausted tasks and tasks whose worktree could not be prepared
excluded). Eligibility is one rule: `list ready`, `status`, automatic and
explicit claims all derive it from the same facts, and a task in retry
cooldown is not claimable however its last proof ended. `claim --wait`
treats a cooldown as something to wait for (it wakes at the earliest
expiry; `status` reports `cooling` and `next_eligible_at`) and returns
`STALLED` only when nothing can proceed without a planner. A worktree that cannot be prepared marks that task
`needs_attention` with the reason and ends the attempt; the queue moves
on to the next task. `atask claim <ref>` on such a task retries the
preparation (the repair path after fixing the worktree); `atask update <ref>
--reset-attempts` clears the mark. It refuses with
`INVALID_INPUT` while the project has no regression checks: work
that can never complete is never handed out. `--wait` blocks until work is
claimable, `DONE`, `STALLED`, or interrupted; while waiting the process
verifies any ready group it finds.

The claim result carries the task view, the token (the only place it is
ever printed), `attempt`, `lease_until`, `workspace` and `branch`,
`target_branch` with its head `target_revision` at claim time (the base
the worktree was created from on a first attempt), `regression_checks`
(the project checks `verify complete` runs after the task checks), and
the `handoff`.

Every claim gets the task's own worktree on branch `atask/<task-id>`, created
from the project's target branch on the first attempt and reused by every
later attempt, so the next attempt starts from the previous one's commits
(and sees its uncommitted edits: the claim reports `workspace_dirty`).
Worktrees are not deleted by `at`; prune them with your worktree tooling
once the task is complete.

**Lease.** The default lease is 30 minutes (minimum 5, maximum 4 hours).
Every authenticated command (`log`, `verify`, `claim renew`, a session
`add --blocks`) renews it, so an agent that logs at least every half hour
never loses its claim; the host that launched the agent renews it on the
agent's behalf (`Renew`, or `atask claim renew` with the token) while the
agent is busy. When a lease does expire the task becomes claimable again;
the old token answers `INVALID_SESSION` (or `INVALID_SESSION` once a new
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
stop the run, are reported as `last_error`, and are never a strike; so is
a timeout. Such a task is not shown as `verification_failed`/`[failed]`:
it is `interrupted` while its attempt is still open and `ready` once it is
released. Only a run charged against its attempt derives to
`verification_failed`. A
`verify complete` fired while the attempt's previous one is still running
is refused with `CONFLICT`; after completion it returns the
stored acknowledgement.

**Context.** `atask context` with no arguments prints the agent operating
contract as one block of text (JSON: `prompt`). It needs no project,
database, or session. `atask context ID` is the worker handoff for that
task: objective, acceptance, checks, blockers, worktree, and how
completion is decided. A completed task's handoff reports that it is
done and issues no new work. Discover work with `atask list`; inspect a
task with `atask show` (no argument: the current claim).

**Installing the instructions.** `atask init` registers the repository and
installs the project instruction block (`app.Instructions`: one paragraph
that points agents at `atask context`) into `AGENTS.md`
between `<!-- atask:begin -->` and `<!-- atask:end -->`, creating the file with a
default engineering-guidelines section when absent and refreshing only
the block on re-runs; it also makes sure `CLAUDE.md` imports it
(`@AGENTS.md`). When `init` creates the repository in a directory that
already has files, they stay untracked (init never commits them); the
result carries `untracked_files` and a `warning` that they must be
committed before the baseline can pass. `--no-instructions` skips that.
`atask init --claude` is optional and reversible: it merges only the
SessionStart hook into `.claude/settings.json` (`atask hook session-start`),
which injects the current claim when one exists and writes nothing
otherwise. It does not install `guard-edit` or `stop`. `atask hook
guard-edit` remains callable for a host that opts in; it only sees
Edit/Write/NotebookEdit and does not catch shell writes.
`atask hook stop` is a no-op: an open claim is not a reason to block
ending a turn. An `atask` failure inside a hook never blocks the harness.
Init never rewrites personal `AGENTS.md`/`CLAUDE.md` or global host
hooks; it only writes under the registered repository root.

**Prompt layers.** An agent runs under three layers and `atask` supplies
the third: (1) a bootstrap system prompt, set by the host where the
harness allows it (Claude Code `-p --append-system-prompt`, a bare model's
system message; Codex has none and relies on AGENTS.md); (2) the project
instruction file, AGENTS.md (or a CLAUDE.md that imports it), holding the
short atask paragraph; (3) the task turn, `atask context ID` as the
worker handoff and `atask list` for the plan. `atask context` with no
arguments is the operating contract and does not dump tasks. The bootstrap is:
```

You are a coding agent responsible for one task in an isolated Git worktree.
Use the shell and the `at` CLI to execute and manage the task; `at` is
authoritative for task state, verification and completion. ATASK_OUTPUT=json is
set: every `atask` command returns one JSON envelope; inspect error.details when
one fails. Start with `atask context` and follow the project instructions.
Commit before `atask verify complete`; only a passing verification completes
the task. If any `atask` command returns INVALID_SESSION or INVALID_SESSION,
stop editing immediately.

```text
**Handoff.** The claim result carries the previous attempts' `latest_next`,
learnings and recent log, plus `inherited` learnings recorded on the
task's direct prerequisites and `last_failure`: the failed checks of the
newest failed verification run with the tail of their output. Read it
before touching the code.

`verify proof` and `verify regression` run that category fresh in the
attempt's worktree, working tree included (the summary says so when it
has uncommitted changes; `revision` is HEAD). They are diagnostic: they
never complete, release, or integrate. `verify complete`:

1. refuses unless both categories have checks (`INVALID_INPUT`) and
   every prerequisite is complete (`NOT_READY`);
2. requires a clean worktree and records its HEAD as the immutable
   submission (nothing is committed on your behalf);
3. in `promote` projects, first merges the revision onto the target branch
   in a scratch worktree; a conflict is `BASE_CONFLICT` before any
   check runs, with `details.conflicts` naming the files;
4. runs BOTH suites fresh on a detached snapshot of that revision;
5. in `promote` projects, re-runs both suites on the merge when it changed
   content, and compare-and-swaps the target (rebuilding on a moved base
   up to three times);
6. in one transaction re-checks the session, prerequisites, contract
   revision, regression policy and the run's evidence, then records
   completion and ends the claim.

A failed `verify complete` returns `CHECK_FAILED` with the evidence
in `error.details`; the claim stays live for repair and the task's failure
count and cooldown are recorded. Evidence keeps each stream's head and
tail; the complete output is in `details.evidence[].log`, and
`details.log_dir` holds those logs and `result.json` (see
`docs/verification.md`, Run logs). Replaying `verify complete` with the same
token after a committed completion returns the stored result.

## Reading
```

atask show REF [--full] glyph line, [STATE], DESCRIPTION, OUTCOME, ACCEPTANCE, REQUIRES, BLOCKS,
TASK CHECKS, VERIFICATION, ATTEMPT, SUBMISSION, HANDOFF, HISTORY
atask [--project NAME] list hierarchy of open work (groups indented; blocked tasks show requires:)
atask list ready | blocked | all | archived
atask status counts, claimable, active, cooling, done, stalled with reasons
atask prune remove worktrees of complete/archived tasks and quarantined directories of idle tasks

```text
`ATASK_OUTPUT=json atask help` prints an envelope listing the commands; a bare
`atask` in JSON mode prints an `INVALID_INPUT` envelope (exit 2).

Glyphs are progress, not liveness: `○` never started (ready or blocked
work with no attempt yet), `◐` started but not complete (claimed,
verifying, awaiting integration, interrupted,
failed, cooling, or any task that once had an attempt), `●` complete, `◌` draft (missing proof or regression).
The bracketed annotation (`[blocked]`, `[failed]`, `[awaiting
verification]`, …) says why a task is not complete. A group shows
`(n/m complete)`: `●` only when a nonempty member set is complete, `◐`
when any member was started or completed, `○` otherwise. JSON carries
`started` on tasks and `progress.started` on groups. Tokens never appear
in any read view.

## Token transport

`claim` is the only command that prints a token, and it also stores the
token inside the task worktree's private Git directory
(`.git/worktrees/<name>/atask-session`, never in the tree, never in the
database, which keeps only a digest). Any `atask` command run with the
worktree as its working directory finds it, planning commands included,
so an agent started inside the worktree needs no token at all. Explicit
sources win when present, in this order: a positional token, `-` (one
line of stdin, so secrets stay out of process listings), `--session`,
`ATASK_SESSION`, then the worktree file. A `claim` subprocess cannot set
`ATASK_SESSION` in its parent shell: hosts that run the agent elsewhere
capture the JSON and inject the token.

Inside a task worktree (branch `atask/<task-id>`) the token must belong to
that task; a token for another task is refused with `INVALID_SESSION`
before any write, so a mis-exported `ATASK_SESSION` cannot act on the wrong
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
Quarantined directories are removed when the task completes; `atask prune`
removes them for idle tasks and removes the worktrees of complete and
archived tasks (branches are kept; a live claim's worktree is never
touched).
A lost token is a stuck task until the lease expires; that is by design.

## The tiny agent prompt
```

Implement the claimed task's outcome and acceptance criteria.
Use the supplied workspace and session. You do not set task status.
Use `atask log` for progress/discoveries.
Use `atask verify proof` and `atask verify regression` while iterating.
When ready, commit the final code revision and call `atask verify complete`.
It runs BOTH suites again and establishes completion only if all gates pass.
BASE_CONFLICT means the target branch moved against you: merge it into
your branch, resolve `details.conflicts`, commit, and verify again.
If new prerequisite work is needed, add it as a blocker of the current
claimed task (`atask add "..." --blocks <task> --proof "run: cmd" --regression "run: cmd"`; your
ATASK_SESSION authorises it), log the handoff, and release the claim
(`atask claim release`).
Every `atask` command renews your lease; `atask log` at least every half hour.
Stop editing immediately if claim renewal/authorization fails.

```text
```
