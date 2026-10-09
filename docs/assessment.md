# Specification assessment, package map, semantics, and plan

This document is the "first action" deliverable: an adversarial reading of the
specification, the final package map, the exact state and completion
semantics, the transaction boundaries and schema, the milestone plan, and the
rationale for every deviation. It is kept current as milestones land.

## 1. Adversarial assessment of the specification

### Contradictions and ambiguities found

1. **`Take(ctx, task *TaskID)` has nowhere to put the project.** `Take(nil)`
   must select from *some* project; the signature cannot express that.
   Resolved with `Take(ctx, TakeRequest{Project, Task, Lease})`.

2. **Who owns the regression policy?** `TaskSpec.Verification` carries a
   `Regression` list, but the spec also says the project owns "project-wide
   verification policy". Both are allowed: a task may carry its own checks;
   the project's regression checks are merged in at submission time and the
   *merged* policy is digested into the submission. Task-level definitions
   win on ID collision so a task can tighten (not loosen) a project check.

3. **Milestone 1 has no verification, yet dependencies require "complete"
   prerequisites.** Without a completion fact nothing with a dependency could
   ever become available. Resolved by implementing `finish` fully in
   Milestone 1 with one honest rule: a submission whose effective policy
   requires *nothing* is vacuously verified, and under integration policy
   `none` the task is complete. A submission with checks is recorded as
   `awaiting_verification` and the task stays unreleased until Milestone 2
   executes the checks. No agent claim ever completes a task.

4. **"Automatic reassignment should be conservative" vs. "another process can
   resume after a crash".** Resolved: lease expiry is the only signal we
   have, so an expired, unfinished attempt is `interrupted` and takeable
   (including by automatic selection, which prefers it). The lease length is
   therefore the conservatism knob. The risk (an expired-but-alive process
   still writing files) is real and documented in `docs/limitations.md`; it
   cannot be closed by a database.

5. **Expired-but-unsuperseded sessions.** Should the original agent be able
   to renew after expiry if nobody else took over? We say no: expiry is a
   hard boundary (`LEASE_EXPIRED`); the agent re-takes the task and receives
   its own handoff. This keeps "expired tokens cannot log or submit" exact
   and makes the authority rule one sentence.

6. **Error-code list is open-ended ("such as").** We define the full set in
   `docs/agent-contract.md` and treat it as a stable contract.

7. **"Do not expose direct mutation of derived statuses" vs. "policy changes
   require a separate operation".** Policy edits are configuration, not
   status; they exist (`tasks project --regression-json`, task policy edit in
   Milestone 2) and are not reachable through a session token.

8. **ID alphabet.** The example `task-k7p4m2x9q6r8v5z1` contains `1` and
   `9`, which RFC 4648 base32 lacks. We use Crockford's 32-character set in
   lower case (digits + letters minus i, l, o, u): 16 chars = 80 bits.

### Missing invariants the spec did not state

- **One submission per attempt**, enforced by a UNIQUE constraint; `finish`
  is idempotent per token.
- **Generation advance is a conditional write** (`WHERE current_attempt_seq
  = ?`), so even a bug that bypassed status checks could not create two
  current attempts.
- **Status precedence** must be total. It is (see `docs/invariants.md`).
- **Candidate selection must agree with the domain rule.** The SQL
  pre-filter for takeable tasks is re-checked in Go before the claim; a
  disagreement is an internal error, never a wrong claim.
- **Dependency added to an in-progress task** does not evict the attempt; it
  blocks the *next* take.
- **Removing a dependency never un-completes anything.** Eligibility is
  derived, so removal needs no recomputation step.

### Where the proposal could be simpler (and was simplified)

- No separate `leases` table: the lease is two columns on the attempt row.
- No `acceptance_criteria` normalisation beyond one table; no separate
  `verification_policies`/`verification_checks` tables. A policy is an
  immutable JSON value with a content digest; it is stored where it is used
  (task row, submission row). Digest equality is the version check.
- No repository interfaces, no generic services. The store exposes typed
  queries on a transaction handle; the application calls them.
- No `integration` or `workspace` packages yet: nothing consumes them.
- No `checkexec` package yet: Milestone 2 introduces it with its caller.
- No `types.go` duplicating internal types: the root package aliases them.

### Where the spec was right and we kept the constraint even though it cost

- Verification is bound to an immutable revision and a policy digest even in
  Milestone 1 where no checks run; the columns exist and are filled.
- Tokens are stored only as SHA-256 digests.
- Status is never stored; `completed_at` is a *fact* written in the same
  transaction as the final gate, not a user-editable flag.

## 2. Final package map

```
tasks.go                 public facade (aliases + Open)
cmd/tasks/main.go
internal/fault           stable error codes (leaf)
internal/verification    Policy, CheckSpec, canonical JSON, digest       -> fault
internal/project         project ID, record, resolution rules           -> fault, verification
internal/task            task ID, Spec/Task, validation, status rules   -> fault, project, verification
internal/dependency      Edge, Validate (DAG rules), Graph read model   -> fault, task
internal/execution       Token, Attempt, Authorize, LogEntry, Handoff   -> fault, project, task
internal/sqlite          schema, migrations, typed queries              -> all domain packages
internal/app             Engine: use cases + transaction boundaries     -> sqlite + domain
internal/cli             parsing, envelopes, rendering                  -> app + domain types
test/e2e                 subprocess tests against the built binary
```

Arrows are the only allowed import directions. Domain packages never import
`sqlite`, `app`, or `cli`. `sqlite` returns facts; it decides nothing.

`internal/checkexec` (argv execution, bounded output) and
`internal/workspace` (Git revision inspection) arrived with Milestone 2,
each with one real consumer. `integration` and the worktree lifecycle are
deferred to Milestone 3.

## 3. State and completion semantics

Facts (stored): dependencies; current attempt and its lease; submissions;
verification runs; `completed_at`.

Derived status precedence:

```
complete > awaiting_integration > awaiting_verification > in_progress
        > blocked > verification_failed > interrupted > available
```

Takeable: `available`, `interrupted`, `verification_failed`. Automatic
selection order: interrupted, then verification_failed, then available;
oldest first within a group.

Completion (Milestone 1): `completed_at` is set iff the newest submission's
newest verification run is `passed` **and** the project's integration policy
is `none`. Under policy `promote` (Milestone 3) a passed run leaves the task
`awaiting_integration` until the revision is promoted; only then is it
complete and only then are dependents released. This is the explicit split
between "verified on a branch" and "available to downstream work".

A dependency is satisfied iff the prerequisite is complete. There is no
weaker prerequisite kind in V1.

## 4. Transaction boundaries and schema

Every write below is one `BEGIN IMMEDIATE` transaction; nothing external runs
inside it:

| Operation | Reads then writes |
|---|---|
| Add | project exists → insert task + criteria |
| AddDependencies | both tasks, same project, duplicate?, reachability CTE → insert (per edge, all-or-nothing) |
| RemoveDependency | delete |
| Take | record + facts → status takeable? → conditional generation advance, supersede old attempt, insert attempt → read handoff |
| Log | attempt by token digest + task generation → Authorize → insert |
| Renew | same authorisation → update lease |
| Finish | same authorisation (or idempotent replay) → insert submission, end attempt, insert run, maybe `completed_at` |

Reads (`Show`, `List`, `Graph`, `History`) use one deferred read transaction
so multi-query views are consistent.

Schema: `projects`, `tasks`, `acceptance_criteria`, `task_dependencies`,
`execution_attempts`, `task_log_entries`, `submissions`, `verification_runs`
(one per judgement, carrying the effective policy), `verification_results`
(evidence). See `internal/sqlite/migrations/0001_init.sql`.

Verification adds the one multi-transaction operation: begin run (tx) →
per check: reuse lookup (read tx), execute (no tx), insert evidence (tx) →
judge (tx). Each boundary leaves a recoverable state (see
`docs/verification.md`).

## 5. Milestones and tests

- **M1 (done):** projects, tasks, IDs, SQLite, DAG + cycles, availability,
  atomic take, leases, fencing, append-only log, show/history, JSON CLI.
  Tests: unit (every domain rule), engine integration (100-way contended
  take, racing edge inserts, crash/resume across engines, durability after
  close), subprocess e2e (exit codes, env defaults, token transports, real
  lease expiry, 24 concurrent processes, linked Git worktree resolution).
- **M2 (done):** `checkexec` (argv, cwd, timeout, bounded output),
  verification runs executed outside transactions with per-check evidence
  commits, `tasks verify --retry/--again`, `tasks evidence`, `tasks policy`
  as a separate versioned operation, Git revision capture at `finish` with
  clean-tree enforcement, evidence reuse by (check digest, revision).
  Tests: required vs optional, missing verifier, timeout, regression skip,
  reuse across tasks/revisions/policies, tree mismatch, dirty submission,
  interrupted run recovery, nested database writes from inside a check.
- **M3:** `workspace` interface, Git and Worktrunk adapters, task-named
  branches, clean-revision capture at finish, integration policy `promote`.
- **M4:** explicit integration candidates, shared regression once per batch,
  guarded promotion, resource limits.

### Findings from the independent adversarial review (fixed)

1. `verify` could complete a task from an older submission while a repair
   attempt was open; now refused (`TASK_ALREADY_TAKEN`) and a new submission
   always withdraws completion.
2. A policy change during an open attempt hid the attempt behind
   `awaiting_verification`; now the change simply applies to the attempt's
   next submission.
3. CLI `--` protected only the first following positional; fixed.
4. SQL take priority disagreed with `Derive` for a failed task with an
   expired attempt; order aligned.
5. `verify --again` always reused evidence; `--no-reuse` added.
6. `finish` reported `WORKSPACE_DIRTY` before `SESSION_SUPERSEDED`; authority
   is now checked first.
7. `list --available` omitted recoverable takeable work; `--takeable` added.

## 6. Deviations from the specification, with rationale

| Spec | Implementation | Why |
|---|---|---|
| `Take(ctx, *TaskID)` | `Take(ctx, TakeRequest)` | project is required for automatic selection |
| `Add` returns `(Task, error)` | returns `(Task, []string warnings, error)` | atomicity hints must reach the caller without blocking creation |
| `Session.Workspace` | absent | no workspace boundary until M3 |
| `Finish` returns only a result | `Finish(ctx, token, FinishOptions)`; a failed verdict is returned as `VERIFICATION_FAILED` with the result in `Details` | agents need both the facts and a non-zero exit |
| Verification in an immutable checkout | M2 verifies in the project tree after confirming it is clean and at the submitted revision | detached worktrees arrive with the workspace boundary (M3); the check is honest about the window |
| `VerificationPolicy.Version` field | computed digest `Policy.Digest()` | a user-settable version can lie; content hash cannot |
| `renew-task-lease` only | also `renew` alias | shorter for agents; both documented |
| Lease bounds unspecified | 1s..24h, default 1h | tests need real expiry; agents need a sane default |
| `leases` table | columns on `execution_attempts` | one active attempt per task; no join needed |
| `verification_policies`, `verification_checks` tables | JSON value + digest on task/submission | immutable value object; no referential churn |
| `types.go` in root | aliases in `tasks.go` | one definition per concept |
| `list` default | open tasks only; `--all` adds complete | the agent-facing default should be actionable work |

## Addendum: the `at` handoff (October 2026)

The second specification ("Agent Task (`at`) implementation handoff")
replaced several earlier decisions. Deviations from it, with reasons:

| Handoff | Implementation | Why |
|---|---|---|
| `Engine` as a Go interface with eight methods | `at.Engine` interface plus `*at.Store` concrete type; bootstrap methods on the concrete type | an interface documents the everyday surface; bootstrap stays trusted and separate |
| `Release(ctx, token, note) error` | `Release(ctx, token, ReleaseOptions{Note, Failed}) (TaskView, error)` | the host needs to record a recoverable agent failure for retry bookkeeping, and sees the resulting state |
| `Log` returns `error` | returns the recorded entry | the sequence number is useful to agents |
| `Show(ctx, id)` | `Show(ctx, project, ref, full)` | keys need a project; full history is opt-in |
| Human-gated tasks retained or attestation path | removed; documented unsupported | an empty-check completion loophole is worse than no feature |
| Parallel task/regression suites when isolated | sequential always | isolation cannot be proven from a declaration |
| Priority field | none | as decided |

Hazards 1–10 from the handoff's section 10 are each closed or documented
in `docs/status.md`.
