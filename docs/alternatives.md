# Alternatives and prior art

Researched against current READMEs and in-repo docs (October 2026). Items we
could not verify are marked. We compare documented behaviour, not marketing.

## What existing systems already solve

| Project | Verified strengths | Storage / coordination |
|---|---|---|
| **Beads (bd)** | Ready-work discovery (`bd ready`, excludes in-progress/blocked/deferred), atomic claim (`bd ready --claim`), ten dependency types (`blocks`, `tracks`, `related`, `parent-child`, `discovered-from`, `until`, `caused-by`, `validates`, `relates-to`, `supersedes`), append-only notes/comments, `bd prime` agent context dump, hash IDs, multi-writer server mode, merge slots, messaging, molecules/formulas. | Dolt (embedded or `dolt sql-server`); `issues.jsonl` is an export; sync via `bd dolt push/pull` to a Git remote. Leases/heartbeat (`bd reclaim`) appear in search snippets only: **unverified**. |
| **Beads Rust (br)** | Same issue model frozen at the "classic" SQLite + JSONL design; no daemon; `--json`/TOON; `capabilities`/`schema`/`robot-docs` self-description; optional MCP server; policy file. Deliberately dropped: auto git commits/pushes, hooks, daemon, Dolt, Linear/Jira sync, web UI. | SQLite source of truth with JSONL auto-flushed on every mutation and re-imported when changed. |
| **Taskmaster AI** | PRD parsing, AI expansion of tasks into subtasks, `next`, dependency add/remove/validate/fix, tags, MCP server for editors. | `tasks.json` per tag; `.taskmaster/state.json`; requires an LLM provider key. `testStrategy` is prose; nothing is executed. |
| **Agent Orchestrator** | Supervises fleets of coding-agent sessions (~35 harness adapters), one branch + worktree per worker, routes CI/review/conflict feedback to the owning agent, status derived at read time. | Go daemon + SQLite with only durable facts; trigger-based `change_log` streamed over SSE; Electron UI. |
| **Overstory** (archived; succeeded by "Warren") | Worktree-per-agent, SQLite WAL "mail" with typed messages, FIFO merge queue with a Merger agent role. | Bun/TypeScript CLI; SQLite coordination. |
| **Worktrunk (wt)** | `wt switch -c <branch>` create/switch worktrees; `wt list --format=json` (schema 2: `items[].branch`, `items[].worktree.path`); `wt merge` = commit → squash → rebase → **pre-merge hooks (abort on failure)** → fast-forward target → remove worktree/branch; hooks for switch/start/commit/merge/remove; TOML config; path template `{{repo_path}}/../{{repo}}.{{branch\|sanitize}}`. | Rust binary; state in Git config. |
| **Temporal** | Deterministic workflows replayed from durable event history; activities with retries/heartbeats; task queues. | Server cluster + DB; SDK-instrumented code. |
| **LangGraph** | Checkpointed graph state per thread; resume, human-in-the-loop, time travel. | Python/JS runtime with a checkpointer (SQLite dev, Postgres prod). |

## What we intentionally do differently

- **Leases with expiry and fencing are first-class.** Beads' claim is an
  assignee + status; release is manual and staleness is a `--days` scan. We
  need "exactly one authority, expired authority is inert" to be a
  transaction-level guarantee, because our agents crash.
- **Status is derived, never set.** Beads/Taskmaster have mutable status
  fields and commands to set them. We store facts (lease, submission, run,
  completion) and compute the state, so an agent cannot mark itself done.
- **Verification is executable and revision-bound.** No prior-art task tool
  binds a completion to a policy digest and an immutable revision with
  recorded evidence; Taskmaster's `testStrategy` is text, Beads has none.
  Worktrunk's pre-merge hooks run checks but record nothing durable.
- **One authoritative store, no export mirror.** Beads Rust keeps JSONL in
  the repo alongside SQLite and reconciles; Beads uses Dolt. We keep state
  outside the repo in one SQLite file. Multi-machine sync is explicitly not
  a V1 problem.
- **Four log fields, not comments.** `done/next/learned/note` is the
  handoff contract; `take` returns the latest `next`, bounded learnings and
  recent entries instead of a transcript.
- **No LLM in the engine.** Taskmaster needs a provider key to expand tasks;
  we never call a model.
- **Smaller vocabulary.** One dependency kind ("requires"), eight derived
  statuses, nine use cases.

## What we are not going to implement

Dolt or Git-synced state; JSONL mirrors; a daemon; agent spawning, messaging,
fleets, or dashboards (Agent Orchestrator, Overstory); PRD parsing or AI
decomposition (Taskmaster); epics/labels/priorities/saved queries (Beads);
a merge queue or conflict resolution (Overstory); workflow replay or graph
checkpointing (Temporal, LangGraph); an MCP server (may be added later as a
thin layer without changing semantics).

## Which existing components we are happy to reuse

- **Worktrunk** for the whole worktree lifecycle in Milestone 3: `wt switch
  -c task-<id>` to create, `wt list --format=json` to locate, `wt merge` to
  promote (never as a dry run: it advances the target and removes the
  source). Our verification should be runnable as a `pre-merge` hook.
- **Plain `git worktree`** as the fallback adapter.
- **The build tools' own caches** (Go test cache, Bazel, Nx) instead of a
  custom result cache.
- **Beads' `ready --claim` shape** (one atomic op) and `bd prime`-style
  onboarding output (`AGENTS.md`).
- **Agent Orchestrator's rule**: store minimal durable facts, derive display
  status at read time, never force-remove a dirty worktree.
- **modernc.org/sqlite** (pure Go) as the SQLite driver; see ADR 0002.

Sources: github.com/gastownhall/beads (README, docs/cli-reference,
docs/multi-agent/coordination.md, AGENTS.md); github.com/Dicklesworthstone/beads_rust;
github.com/eyaltoledano/claude-task-master (README, docs/task-structure.md,
docs/command-reference.md); github.com/OrchestratorInc/agent-orchestrator
(README, docs/architecture.md); github.com/jayminwest/overstory;
github.com/max-sixty/worktrunk (docs/AGENTS.md, skills/worktrunk/reference/*);
temporalio/documentation (understanding-temporal.mdx); langchain-ai/langgraph
and langchain-ai/docs (persistence.mdx).
