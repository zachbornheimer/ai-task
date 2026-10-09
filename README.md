# tasks

A minimal, harness-agnostic task engine for AI coding agents.

`tasks` manages durable work for agents independently of the harness running
them: discover available work, atomically take a task, receive its complete
context, log progress and discoveries, resume after a crash, and submit an
implementation for verification whose completion is derived from evidence.

It is a Go library and CLI over one SQLite file. No daemon, no API keys, no
LLM calls.

## Install

```sh
go install github.com/zachbornheimer/ai-task/cmd/tasks@latest
```

## Five-minute tour

```sh
export TASKS_OUTPUT=json                      # stable envelopes for agents (omit for text)
cd my-repo && tasks init                      # register the repository as a project
A=$(tasks add "Parse tokens" | jq -r .result.id)
B=$(tasks add "Reject expired access tokens" --accept "expired -> 401" \
      --check "unit: go test ./auth/..." | jq -r .result.id)
tasks deps add $B --requires $A               # B requires A
tasks list --available                        # only A
S=$(tasks take | jq -r .result.token)         # atomic claim of A, returns the session token
tasks log $S --done "parser written" --next "add padding tests" --learned "tokens are base64url"
tasks finish $S                               # A complete (no checks); B becomes available
tasks take $B                                 # B's handoff, contract, and token
tasks graph --format dot                      # visualise
```

Dependency direction is always `tasks deps add B --requires A` = "B requires A".

## What is implemented (Milestones 1 and 2)

- Projects with stable IDs; any Git worktree resolves to its project.
- Tasks with description, outcome, constraints, acceptance criteria, and a
  durable, digested verification policy.
- Dependency DAG with atomic cycle/duplicate/self/cross-project rejection,
  edges at creation (`--requires`, `--blocks`), discovered blockers with
  `release`, manual human-gate tasks, available/blocked listing, graph
  output (text, JSON, DOT, edge list).
- Machine-only execution: `take --wait`, `status` (`done`/`stuck`), and a
  reference driver in `examples/executor.sh`.
- Atomic `take` with leases, fencing generations, and unguessable session
  tokens stored as digests; automatic selection prefers interrupted work.
- Append-only structured log (`done/next/learned/note`) and bounded handoff.
- `finish` records a submission (a clean Git commit when the project is a
  repository), then runs the effective policy: task checks plus project
  regression, as argv commands with timeouts and bounded output. Evidence
  per check is bound to the revision and policy digest; identical checks at
  the same revision are reused. No agent claim completes a task.
- `verify --retry` recovers an interrupted run; `policy` edits are a
  separate operation that invalidates stale evidence; `evidence` shows
  every run.
- JSON envelopes, stable error codes, token via argument/stdin/environment.

Not yet: Git/Worktrunk per-task worktrees and detached verification
checkouts (M3), integration policy `promote`, batched regression and guarded
promotion (M4). See `docs/assessment.md` for the plan and
`docs/limitations.md` for the risks.

## Library

```go
eng, _ := tasks.Open(ctx, tasks.Config{})
proj, _ := eng.InitProject(ctx, "svc", "/path/to/repo")
t, warnings, _ := eng.Add(ctx, tasks.TaskSpec{ProjectID: proj.ID, Description: "Reject expired access tokens"})
sess, _ := eng.Take(ctx, tasks.TakeRequest{Project: proj.ID})
eng.Log(ctx, sess.Token, tasks.LogEntry{Done: "..."})
res, _ := eng.Finish(ctx, sess.Token)
```

## Development

```sh
go test ./...                 # unit, engine integration, subprocess e2e
go test -race ./...
go test ./internal/app -bench . -benchtime 2s
go test ./test/e2e -bench . -run XXX
```

Docs: `docs/architecture.md`, `docs/invariants.md`, `docs/verification.md`,
`docs/agent-contract.md`, `docs/alternatives.md`, `docs/limitations.md`,
`docs/benchmarks.md`, `docs/decisions/`. Agent onboarding: `AGENTS.md`.
