# Verification

Verification is separate from implementation. An agent's `finish` is a
claim; evidence is produced by running the task's verification contract.

## Three responsibilities

1. **Contract** (`verification.Policy`): which checks must pass.
   `TaskChecks` prove the task's own outcome; `Regression` guards the rest of
   the project and is normally inherited from the project. Each
   `CheckSpec` is an argv array, optional working directory, timeout, and a
   `Required` flag. Optional checks are recorded but never compensate for a
   failed required check.
2. **Planner** (Milestone 2): orders cheap checks first, groups compatible
   ones, reuses safe prior results, and later batches shared regression. It
   may never drop a required check.
3. **Evidence** (Milestone 2): per check, the policy digest, check ID and
   version, revision, timestamps, exit code, outcome, bounded stdout/stderr,
   the attempt, and whether it was reused.

## Durable representation

Policies are stored as canonical JSON and identified by `sha256:` digest of
that JSON. Timeouts are `timeout_ms` integers. Go function values are never
stored. Example of the durable form (also accepted by `tasks add
--policy-json` and `tasks project --regression-json` for the array part):

```json
{
  "task_checks": [
    {"id": "unit", "version": "1", "command": ["go", "test", "./auth/..."], "timeout_ms": 300000, "required": true}
  ],
  "regression": [
    {"id": "full", "command": ["make", "test"], "required": true}
  ]
}
```

The CLI shorthand `--check "unit: go test ./auth/..."` produces the same
structure with a default timeout (30 min) and `required: true`;
`--optional-check` sets `required: false`.

## Effective policy and versioning

At `finish`, the task's policy is merged with the project's regression
checks (task definitions win on ID collision) and the merged policy's
digest is stored on the submission. Any later policy edit yields a new
digest, so evidence gathered under an older contract is distinguishable and
(Milestone 2) treated as invalid for completion.

## Execution (Milestone 2)

`finish` records the submission and a `pending` run in one transaction, then
(unless `--no-verify`) calls the same code path as `tasks verify`:

1. One write transaction marks the run `running` and snapshots the
   effective policy and an environment fingerprint (OS/arch, Go version,
   host).
2. Each planned check runs **outside any transaction** in the project
   directory (`project root` + check `dir`), via argv with no shell, with
   its timeout and bounded output (first and last 16 KiB of each stream).
   Its evidence row commits in its own short transaction the moment it
   finishes. Before running, the executor confirms the tree is clean and at
   the submitted revision; otherwise the evidence is an `error`, never a
   pass.
3. One write transaction judges the run: it passes iff every required check
   has `passed` evidence. Under integration policy `none` a passed run
   records the completion fact.

Order: required task checks, optional task checks, then regression checks.
Regression checks are skipped (recorded as `skipped`) when a required task
check failed, so a broken implementation does not pay for a project-wide
run. Evidence outcomes are `passed`, `failed`, `timeout`, `error` (could
not run: missing binary, bad directory, tree mismatch, project without a
directory) and `skipped`. Only `passed` counts.

Checks run sequentially in Milestone 2; resource-aware concurrency arrives
with batching (Milestone 4).

### Reuse

Before executing a check, the engine looks for original (non-reused)
`passed` evidence with the same check digest (content hash of ID, version,
argv, dir, timeout, required) at the same non-empty revision, from any run
in the store. If found, a `reused` evidence row pointing at it is recorded
instead of executing. Failures are never reused; evidence without a revision
is never reused. Build-tool caches (Go test cache, etc.) remain the tools'
own business.

### Interruption and retry

If the process dies or is cancelled mid-run, the run stays `running` and
the evidence already committed survives. `tasks verify <task>` then reports
`VERIFICATION_RUNNING`; `--retry` closes the stuck run as `error` and starts
a new one, which reuses the committed evidence at the same revision. A
run that is superseded by a concurrent retry stops at its next evidence
write and is closed as `error`.

### Policy changes

`tasks policy <task> ...` replaces the task's checks and yields a new
digest. If the newest submission was judged (or is pending) under a
different effective digest, a `stale` run is appended, completion is
withdrawn, and the task returns to `awaiting_verification`; dependents that
were released become `blocked` on their next take (attempts already
running are not evicted; see `docs/limitations.md`). A project's regression
list applies to future runs only: re-judging every completed task in a
project on each regression edit would stall it, and `tasks verify --again`
exists for deliberate re-verification.

## Self-certification

An implementation session cannot change the policy: `tasks add --check`,
`tasks project --regression-json`, and (Milestone 2) `tasks policy set`
are separate operations that produce a new digest. This is a correctness
boundary within the engine, not a security boundary against the local OS
user (see `docs/limitations.md`).

## What a passing check proves

A zero exit status proves that the command exited zero on that revision
under that policy. It does not prove the semantic outcome: a test that
exercises nothing still passes. Ten tests for one behaviour are a fine
check; ten unrelated behaviours hidden in one script suggest the task should
be split. Acceptance criteria exist for the human reviewer precisely because
this gap cannot be closed mechanically.
