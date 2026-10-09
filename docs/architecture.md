# Architecture

`at` is a durable, harness-agnostic task engine for AI coding agents: an
atomic task graph with stable keys and organizational groups, leased and
fenced execution claims, append-only handoffs, and completion that only
fresh verification can establish. It is a Go library (`at` package) with a
thin CLI (`cmd/at`) over one SQLite file. It does not call models, own
prompts, or run a worker pool; the host does (see
`examples/embedded_runner`).

## Domain ownership

| Package | Owns | Sole reason to change |
|---|---|---|
| `task` | the contract (ID, key, kind, title, outcome, constraints, acceptance, checks, cohort) and **status derivation** | meaning or validation of a task changes; status precedence changes |
| `plan` | the closed set of plan changes, reference and patch semantics | the planning contract changes |
| `dependency` | the single hard-edge direction, DAG rules, prerequisite satisfaction | dependency semantics change |
| `execution` | attempts, leases, fencing generation, tokens, log entries, handoff bounds | authority or handoff semantics change |
| `verification` | check specs, policy digest, modes, planner order, verdict (`Judge`), evidence shape | the verification contract changes |
| `project` | project identity, trusted configuration, directory → project resolution | project configuration changes |
| `checkexec` | running one argv command: cwd, timeout, bounded streams | execution mechanics change |
| `workspace` | Git: per-task worktrees (`at/<id>`), detached snapshots, two-phase guarded promotion | Git interaction changes |
| `app` | use cases and transaction boundaries (`Apply`, `Claim`, `Verify`, cohorts, `List`/`Show`/`Summary`) | a use case coordinates domains differently |
| `sqlite` | schema, migrations, typed queries | persistence changes |
| `cli` | parsing, envelopes, glyph rendering | CLI surface changes |
| `fault` | stable error codes | a new code is needed |

Dependency direction: `cli → app → sqlite → domain`; domain packages import
only other domain packages and `fault`. The store returns facts and decides
nothing. The one SQL pre-filter that encodes a rule (claimable candidates)
is re-checked in Go before the claim.

## Authority model

- **Planning authority** (`Apply`, `at add/update`) edits the graph. It is
  distinct from execution authority: reviewing a plan never requires a
  claim. Edits to a *claimed* task's eligibility need that task's session
  token or `--planner`; contract edits to claimed tasks need `--planner`;
  completed tasks are immutable (archive and re-plan).
- **Execution authority** is a session token: returned once by `Claim`,
  stored as a digest, valid while its attempt is current (fencing
  generation), unexpired, not ended, not superseded. `Renew`, `Release`,
  `Log`, and `Verify` check it in the same transaction as their write.
- **Verifier authority** for cohorts is a job lease owned by a fresh token
  the engine mints; a member's consumed session token never becomes a
  cohort credential.

This is a correctness boundary within the engine, not a security boundary
against the local OS user (see `docs/limitations.md`).

## Transactions

Writes use `BEGIN IMMEDIATE` (`_txlock=immediate`), reads use deferred
transactions; WAL mode; 10 s busy timeout. Transactions never contain Git
or test execution. The two multi-step operations are structured so every
boundary leaves a recoverable state:

**Verify complete (single task):** prepare (read) → Git inspect (outside) →
record submission + running run (write) → snapshot + checks, each evidence
row in its own fenced write → prepare merge + verify candidate + CAS
promote (outside, bounded retry on a moved base) → finalise (write:
re-check session, prerequisites, contract revision, regression policy,
evidence on the final revision; mark complete; end claim; integration
record).

**Cohort job:** claim job + member runs (write) → assemble candidate +
checks (outside, fenced evidence writes, job lease heartbeat) → promote →
finalise all members (write) or send failing members back.

`reconcile` (run on every claim, list, show, summary) closes final runs
whose attempt lease expired and resets member runs of expired jobs to
pending, so a dead verifier leaves no phantom "verifying" state.

## Derived status

Nothing stores a status. `task.Derive(Facts)` combines archive, completion,
submission/run state, lease state, unmet prerequisites, failure count and
cooldown with a fixed precedence (`docs/invariants.md`). `completed_at` is
a fact written only by the finalising transaction.

## Workspaces and integration

Every project is a Git repository (`at init` runs `git init` and makes an
initial commit when the directory is not one). Every task has one
worktree on branch `at/<task-id>`, created from the target branch at the
first claim and reused by every later attempt, so retries continue from
the previous attempt's commits. The session token is returned once to
the caller of `Claim` and written nowhere; the host injects it into the
agent process (`AT_SESSION`). `at init` writes nothing into the
repository: no hooks, no files in the tree. Final checks run in a detached snapshot of the submitted commit, never in
the editable worktree. Integration policy `promote` (the default) merges
the verified revision onto the target branch in a scratch worktree,
re-verifies the merge when it changed content, and advances the target
with a compare-and-swap (fast-forwarding a clean checked-out target in
place, or `update-ref` with the expected old value). Fast-forwards into a
checkout are serialized through a lock file in the Git directory
(`at-promote.lock`), because Git updates the working tree before the ref
and two at once would leave the loser's files ahead of HEAD. Concurrent
`git worktree add` calls retry briefly on Git's lock files. Policy `none`
skips promotion. Worktrees are never deleted by `at`; prune them with
your worktree tooling.

## State location

`$XDG_STATE_HOME/at/at.db` (Linux), `~/Library/Application Support/at/`
(macOS), `%LOCALAPPDATA%\at\` (Windows), or `AT_DB`. Worktrees live beside
the database unless the project sets `workspace_root`. One file holds every
project. Network filesystems are unsupported.
