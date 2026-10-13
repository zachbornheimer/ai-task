# atask — agent task engine

A small, durable, harness-agnostic task/dependency/claim/verification
engine for AI coding agents. An external application turns a prompt into a
plan, adversarially revises it, and runs N model workers; `atask` stores the
graph, atomically hands out eligible work, fences workers with leased
session tokens, preserves handoffs, and establishes completion only from
fresh verification. It does not call models.

Go library (`github.com/zachbornheimer/ai-task`, package `at`) with a thin
CLI (`cmd/atask`) over one SQLite file. No daemon, no API keys.

## Install

Needs Go 1.24 or later and Git. Linux is the tested platform; macOS and
Windows build but are untested.

```sh
go install github.com/zachbornheimer/ai-task/cmd/atask@main
export PATH="$(go env GOPATH)/bin:$PATH"   # if `atask` is not found
atask version                                 # the commit you installed, e.g. v0.0.0-20261011…-e83b0eafd593
```

There are no release tags yet, so `@main` (or `@latest`, which resolves to
the same commit) installs the newest commit on `main`. State lives in one
SQLite file, `~/.local/state/atask/atask.db` on Linux (`ATASK_DB` overrides; `at
--help` shows the default), with task worktrees and run logs beside it.

Every repository you use it in needs a Git identity (`git config
user.name` / `user.email`): agents commit with it, and `atask` records the
merges it makes when the target branch has moved as the same identity,
with an `At-Task:` trailer.

## Tour

```sh
export ATASK_OUTPUT=json
cd my-repo && atask init --claude                         # register the repo, install AGENTS.md + CLAUDE.md, and (--claude) the Claude Code hooks
git add AGENTS.md CLAUDE.md .claude && git commit -m "Install atask"   # worktrees and checks see only committed files
atask project --target-branch main --attempts 3           # a project holds a name, path, target branch and attempt budget, no checks
atask doctor                                              # repository, target, identity, worktrees
atask add -f plan.yaml                                    # a whole plan (JSON or YAML) as one atomic revision
atask add "Fix the typo" --proof "run: go test ./..." --regression "run: go test ./..." --claim   # one small change: add and claim in one step
atask add "Identity Core" --group --key identity
atask add "Confirm OAuth API compatibility" --key compat --parent identity --proof "run: go test ./auth/..." --regression "run: go test ./..."
atask add "Implement token store" --key store --parent identity --requires compat --proof "run: go test ./store/..." --regression "run: go test ./..."
atask list                                                # ○ ◐ ● hierarchy
cd "$(atask claim | jq -r .result.workspace)"             # atomic leased claim; the task's worktree on atask/<id>, token stored inside
atask context                                             # operating contract; atask context ID is the worker handoff
atask show                                                # the current claim (pins, budgets, handoff); atask show ID for any task
atask log --done "..." --next "..."                       # inside the worktree no token is needed; every command renews the lease
atask verify proof                                        # diagnostic
# commit in the worktree, then:
atask verify complete                                     # both suites fresh on a snapshot, promote, complete
atask claim --wait                                        # next eligible task, or DONE / STALLED
```

Things that surprise people the first time:

- **A check must fail before the work exists.** `go test -run TestNew`
  passes when no test matches, so a check like that proves nothing until
  the test is written; name a test the task adds, or check for the
  artefact itself.
- **`DONE` and `STALLED` exit with status 1** and a coded envelope, like
  every other coded result; a worker loop treats both as its normal end
  (`examples/hosts/common.sh` shows the loop).
- **`at` never rewrites history.** A failed attempt's commits stay on the
  task branch and reach the target with the fix; squash before `verify
  complete` (`wt step squash`, or git) if you want one commit per task.
- **Every verify writes its full output to files**: `log_dir` in the
  result (one `<check>.log` each, plus `result.json`), named in the human
  output, so a failure in the middle of a long run is never lost.

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

## Using it from your AI systems

`atask init` installs the project instruction block into AGENTS.md (read by
Codex, OpenCode and current Claude Code) and a CLAUDE.md that imports it,
so every request an agent receives in that repository is routed through
`at`: plan with checks, claim, work in the task worktree, `atask verify
complete`. `atask init --claude` optionally installs SessionStart only
(current claim, never the board). It does not install edit or stop
guards. `docs/integrations.md` has the recipe per
harness, headless and interactive, and `examples/hosts/` the worker
loops.

## What it guarantees

- One atomic plan revision per `Apply`; stable keys; idempotent replays;
  optimistic concurrency; a DAG of hard prerequisites; organizational
  groups that never imply edges.
- One leased attempt per task; tokens stored as digests; stale tokens are
  inert; oldest-eligible deterministic claims; `DONE` / `STALLED` for
  waiting workers. One worktree per task (`atask/<id>`), reused across
  attempts, carrying the session token so commands run there need none;
  an attempt that never ended has its worktree quarantined on takeover,
  so a stale process can neither act on the task nor reach the branch.
- Checks are mandatory: a task cannot be planned without task checks and
  no work is handed out until the project has regression checks.
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
