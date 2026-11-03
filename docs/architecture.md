# Architecture

`at` is a durable, harness-agnostic task engine for AI coding agents: an
atomic task graph with stable keys and organizational groups, leased and
fenced execution claims, append-only handoffs, and completion that only
fresh verification can establish. It is a Go library (`at` package) with a
thin CLI (`cmd/atask`) over one SQLite file. It does not call models, own
prompts, or run a worker pool; the host does (see
`examples/embedded_runner`).

## Domain ownership

| Package        | Owns                                                                                                    | Sole reason to change                                              |
| -------------- | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `task`         | the contract (ID, key, kind, title, outcome, constraints, acceptance, proofs) and **status derivation** | meaning or validation of a task changes; status precedence changes |
| `plan`         | the closed set of plan changes, reference and patch semantics                                           | the planning contract changes                                      |
| `dependency`   | the single hard-edge direction, DAG rules, prerequisite satisfaction                                    | dependency semantics change                                        |
| `execution`    | attempts, leases, fencing generation, tokens, log entries, handoff bounds                               | authority or handoff semantics change                              |
| `verification` | proof specs, modes, order, verdict (`Judge`), evidence shape                                            | the verification contract changes                                  |
| `project`      | project identity, trusted configuration, directory → project resolution                                 | project configuration changes                                      |
| `process`      | running one argv command: cwd, timeout, bounded streams                                                 | execution mechanics change                                         |
| `workspace`    | Git: per-task worktrees (`atask/<id>`), detached snapshots, stacked task branches                       | Git interaction changes                                            |
| `app`          | use cases and transaction boundaries (`Apply`, `Claim`, `Verify`, `List`/`Show`/`Summary`)              | a use case coordinates domains differently                         |
| `sqlite`       | schema, migrations, typed queries                                                                       | persistence changes                                                |
| `cli`          | parsing, envelopes, glyph rendering                                                                     | CLI surface changes                                                |
| `fault`        | stable error codes                                                                                      | a new code is needed                                               |

Dependency direction: `cli → app → sqlite → domain`; domain packages import
only other domain packages and `fault`. The store returns facts and decides
nothing. The one SQL pre-filter that encodes a rule (claimable candidates)
is re-checked in Go before the claim.

## Authority model

- **Planning authority** (`Apply`, `atask add/update`) edits the graph. It is
  distinct from execution authority: reviewing a plan never requires a
  claim. Planner authority is the absence of a _claimed_ task's own
  session token: its proofs, size, and requires change without it and are
  refused with `INVALID_SESSION` with it;
  completed tasks are immutable (archive and re-plan).
- **Execution authority** is a session token: returned once by `Claim`,
  stored as a digest, valid while its attempt is current (fencing
  generation), unexpired, not ended, not superseded. `Renew`, `Release`,
  `Log`, and `Verify` check it in the same transaction as their write.

This is a correctness boundary within the engine, not a security boundary
against the local OS user (see `docs/limitations.md`).

## Transactions

Writes use `BEGIN IMMEDIATE` (`_txlock=immediate`), reads use deferred
transactions; WAL mode; 10 s busy timeout. Transactions never contain Git
or test execution. Multi-step operations are structured so every
boundary leaves a recoverable state:

**Verify complete:** prepare (read) → Git inspect (outside) →
record submission + running run (write) → snapshot + proofs, each evidence
row in its own fenced write → finalise (write: re-check session,
prerequisites, contract revision, evidence on the final revision; mark
complete; end claim; record the revision and pending actions) → run
`on_complete` actions (outside, each result recorded).

`reconcile` (run on every claim, list, show, summary, verify) closes final
runs whose attempt lease expired, so a dead verifier leaves no phantom
"verifying" state. A running run also records its owner (pid, host, process
start time) and a heartbeat: a run whose owner process provably no longer
exists (pid gone, or reused by a process with another start time) is
abandoned at once, and one whose heartbeat is two intervals stale is
abandoned too. An abandoned run is an `error` run whose summary begins
`abandoned:`; it is never counted toward the attempt budget, and the next
`verify complete` or `claim` proceeds.

## Derived status

Nothing stores a status. `task.Derive(Facts)` combines archive, completion,
submission/run state, lease state, unmet prerequisites, failure count and
cooldown with a fixed precedence (`docs/invariants.md`). `completed_at` is
a fact written only by the finalising transaction.

## Workspaces

Every project is a Git repository (`atask init` runs `git init` and makes an
initial commit when the directory is not one). Every task has one
worktree on branch `atask/<task-id>`, created at the first claim from the target branch plus a merge of every required task's completed revision in this repository (`BASE_CONFLICT` when those merges conflict; a revision from another repository is not merged) and reused by every later attempt, so retries continue from
the previous attempt's commits. The claim stores the session token in
the worktree's private Git directory, so `atask` commands run there need no
`ATASK_SESSION`. When the previous attempt never ended cleanly, the claim
first moves its worktree to `<path>.stale-<seq>` with HEAD detached and
creates a fresh one on the branch: a stale process keeps only its own
expired token and cannot reach the branch or the new files. `atask init`
writes no Git hooks; it installs the agent instruction files (AGENTS.md
block, CLAUDE.md import).
Final proofs run in a detached snapshot of the submitted commit, never in
the editable worktree. Completion records the revision and never moves the target branch; an
`on_complete` action does that (for example `run: wt merge main`).
Concurrent `git worktree add` calls retry briefly on Git's lock files.
Worktrees are never deleted by `at`; prune them with your worktree
tooling.

## State location

`$XDG_STATE_HOME/at/at.db` (Linux), `~/Library/Application Support/at/`
(macOS), `%LOCALAPPDATA%\at\` (Windows), or `ATASK_DB`. Worktrees live beside
the database. One file holds every
project. Network filesystems are unsupported.

`ATASK_DB_URL` selects the same store on PostgreSQL (`postgres://…`): the
`sqlite` package owns one dialect layer (placeholder rebinding,
`RETURNING id` for allocated ids, and the two engine-specific query
fragments) so the rest of the engine runs unchanged. Local SQLite files
keep today's DSN pragmas exactly. `atask sync` copies the local state into
the remote database; it is the only writer of the remote one.
