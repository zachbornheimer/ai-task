# Architecture

`tasks` is a durable task engine for AI coding agents: a Go library and CLI
over one SQLite file. It is not an orchestrator, an issue tracker, a workflow
runtime, or a CI server.

## Domain ownership

| Package | Owns | Sole reason to change |
|---|---|---|
| `task` | the contract (ID, description, outcome, constraints, acceptance, policy) and **status derivation** | meaning or validation of a task changes; status precedence changes |
| `dependency` | the edge direction, DAG invariants, prerequisite satisfaction, graph read model | dependency semantics or graph algorithms change |
| `execution` | attempts, leases, fencing generation, session tokens, log entries, handoff bounds | authority or handoff semantics change |
| `verification` | check specifications, policy canonical form and digest (M2: planner, evidence) | the verification contract changes |
| `project` | project identity, configuration, directory → project resolution | project identity or configuration changes |
| `app` | use cases and transaction boundaries | a use case coordinates domains differently |
| `sqlite` | schema, migrations, SQL | persistence changes |
| `checkexec` | running one external command: argv, cwd, timeout, bounded streams | execution mechanics change |
| `workspace` | Git working-tree inspection (revision, cleanliness); M3: worktree lifecycle | Git interaction changes |
| `cli` | argument parsing, envelopes, rendering | CLI surface changes |
| `fault` | stable error codes | a new code is needed |

Dependency direction: `cli → app → sqlite → domain`; domain packages import
only other domain packages and `fault`. The store returns facts and never
decides eligibility, authority, or completion. The one place SQL encodes a
domain rule, the takeable pre-filter, is re-checked in Go before any claim.

## The two identities

- **Task ID** (`task-…`) identifies the work. It is random, stable, never
  reused, and will name the task's branch/worktree in Milestone 3.
- **Session token** (`sess-…`) identifies temporary write authority over one
  execution attempt. It is returned once, by `take`, and stored only as a
  SHA-256 digest.

Each task has a fencing generation (`current_attempt_seq`). Attempt *n* has
authority iff *n* equals the task's generation, the attempt has not ended,
and its lease has not expired. All three are checked in the transaction that
performs the mutation.

## Transactions

Writes use `BEGIN IMMEDIATE` (the driver's `_txlock=immediate`), so writers
queue on SQLite's lock with a 10 s busy timeout instead of failing on a
deferred-to-write upgrade. Transactions are short and never contain Git or
test execution. Reads use deferred transactions for consistent multi-query
views. WAL mode lets readers proceed during writes.

Crash model: every mutation is one transaction, so a crash leaves either the
old or the new state. A crash after `take` but before the agent does
anything leaves an attempt that ages out; a crash after a `log` acknowledge
leaves the entry committed. Verification is the one multi-step operation:
the run is recorded before any check starts, each check's evidence commits
on its own, and `tasks verify --retry` closes a run whose process died and
resumes with the committed evidence.

## Derived status

Nothing stores a status. `task.Derive(Facts)` combines the completion fact,
unmet-dependency count, lease state, and the newest submission's run state
with a fixed precedence (see `docs/invariants.md`). `completed_at` is a fact
written in the same transaction as the final gate, never by a user command.

## Completion and integration

A task is complete when its newest submission has a passed verification run
and the project's integration policy is satisfied. Milestone 1 implements
policy `none` (verified ⇒ complete). Policy `promote` (Milestone 3) keeps a
verified task at `awaiting_integration` until its revision is promoted, so a
branch that passed in isolation never releases a dependent whose worktree
lacks the change.

## Trust model (V1)

The engine distinguishes *claims* from *evidence*: log entries and `finish`
are claims; verification runs bound to a policy digest and (M2+) a revision
are evidence. An agent cannot complete a task or weaken a policy through its
session token. This is a correctness boundary, not a security boundary: the
same OS user can open the SQLite file or edit the tests. Stronger guarantees
need a protected service or CI, which is out of V1 scope. See
`docs/limitations.md`.

## State location

The database lives outside any repository: `$XDG_STATE_HOME/tasks/tasks.db`
(Linux, default `~/.local/state`), `~/Library/Application Support/tasks/` on
macOS, `%LOCALAPPDATA%\tasks\` on Windows, or `TASKS_DB` / `--db`. One file
holds every project. Network filesystems are not supported for WAL mode.
