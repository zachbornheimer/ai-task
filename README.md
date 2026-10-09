# at — agent task engine

A small, durable, harness-agnostic task/dependency/claim/verification
engine for AI coding agents. An external application turns a prompt into a
plan, adversarially revises it, and runs N model workers; `at` stores the
graph, atomically hands out eligible work, fences workers with leased
session tokens, preserves handoffs, and establishes completion only from
fresh verification. It does not call models.

Go library (`github.com/zachbornheimer/ai-task`, package `at`) with a thin
CLI (`cmd/at`) over one SQLite file. No daemon, no API keys.

## Install

```sh
go install github.com/zachbornheimer/ai-task/cmd/at@latest
```

## Tour

```sh
export AT_OUTPUT=json
cd my-repo && at init                                  # Git project: target branch + promote policy
at project --regression-check "test: go test ./..."    # trusted project gate
at add "Identity Core" --group --key identity
at add "Confirm OAuth API compatibility" --key compat --parent identity --check "unit: go test ./auth/..."
at add "Implement token store" --key store --parent identity --requires compat --check "unit: go test ./store/..."
at list                                                # ○ ◐ ● hierarchy
S=$(at claim | jq -r .result.token)                    # atomic leased claim; private worktree on at/<task>/1
AT_SESSION=$S at log --done "..." --next "..."
AT_SESSION=$S at verify task                           # diagnostic
# commit in the worktree, then:
AT_SESSION=$S at verify complete                       # both suites fresh on a snapshot, promote, complete
at claim --wait                                        # next eligible task, or DONE / STALLED
```

Go:

```go
eng, _ := at.Open(ctx, at.Config{})
proj, _ := eng.InitProject(ctx, "svc", "/path/to/repo")
eng.Apply(ctx, at.ChangeSet{ProjectID: proj.ID, Operations: []at.Change{
    at.AddTask{Key: "parse", Title: "Parse tokens", TaskChecks: []at.CheckSpec{{ID: "unit", Command: []string{"go", "test", "./auth/..."}, Required: true}}},
}})
s, _ := eng.Claim(ctx, at.ClaimRequest{ProjectID: proj.ID, Wait: true})
res, err := eng.Verify(ctx, s.Token, at.VerifyComplete)
```

`examples/embedded_runner` is the full host flow with a fake planner,
reviewer and coding agent (no provider needed); its test drains a
seven-task graph, including a coupled-verification cohort, with four
workers.

## What it guarantees

- One atomic plan revision per `Apply`; stable keys; idempotent replays;
  optimistic concurrency; a DAG of hard prerequisites; organizational
  groups that never imply edges.
- One leased attempt per task; tokens stored as digests; stale tokens are
  inert; oldest-eligible deterministic claims; `DONE` / `STALLED` for
  waiting workers.
- Completion only from `verify complete`: both check categories fresh on a
  detached snapshot of a clean commit, guarded promotion to the target
  branch, and a finalising transaction that re-checks everything. Nothing
  completes with missing checks, unmet prerequisites, a changed contract,
  or a moved target.
- Coupled verification for tasks that can only be proven together,
  without dependency cycles, recoverable after crashes.

## Docs

`docs/agent-contract.md` (CLI and JSON), `docs/architecture.md`,
`docs/invariants.md`, `docs/verification.md`, `docs/status.md` (what passes
and what is unsupported), `docs/migration.md` (from the earlier `tasks`
CLI), `docs/limitations.md`, `docs/decisions/`. Agent onboarding:
`AGENTS.md`.

## Development

```sh
go test ./...                       # unit, engine, CLI goldens, subprocess e2e, example runner
go test -race ./internal/app/ ./test/e2e/
go test ./internal/cli/ -update     # regenerate golden files
```
