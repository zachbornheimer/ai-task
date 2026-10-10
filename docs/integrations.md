# Using `at` from AI systems

The goal: every request an agent receives in a repository is routed
through `at`, so work is planned with checks, done in isolated worktrees,
completed only by fresh verification, and remembered across sessions and
harnesses. This page says how each harness is wired. The engine never
calls a model; the harness does.

## Three layers

| Layer | Holds | Where it lives |
|---|---|---|
| Bootstrap | identity, the `at` CLI, JSON envelopes, stop on lost authority | the harness's system prompt where it can be set (`claude -p --append-system-prompt`, a bare model's system message); text in `docs/agent-contract.md` |
| Project instructions | how work gets done here, the verbs, the execution contract | `AGENTS.md` block installed by `at init`; `CLAUDE.md` imports it |
| Task turn | outcome, constraints, acceptance, checks, workspace, state, handoff | `at context`, generated per task |

The project file is the lever: it says that any change starts with the
plan, that questions are not tasks, and that the target branch is never
edited directly. Hooks make it hold when a long session forgets.

## Setup, once per repository

```sh
cd repo
at init --claude                                  # instructions + Claude Code hooks (omit --claude elsewhere)
at project --regression-check "test: go test ./..." --regression-check "vet: go vet ./..."
at doctor                                         # repository, target, identity, suite green on the target
git add AGENTS.md CLAUDE.md .claude && git commit -m "Route agent work through at"
```

## Claude Code

Interactive: open the repository; the session-start hook prints the plan
or the task in hand, the instructions route requests through `at`, the
edit guard refuses changes outside a task worktree, and the stop hook
refuses to end a turn with an unfinished claim. The agent runs `at
context`, plans or claims, `cd`s into the workspace, works, commits,
`at verify complete`.

Headless, one worker:

```sh
examples/hosts/claude-code.sh        # loops: claim --wait, context | claude -p, release if not completed
```

The script passes `--append-system-prompt` with the bootstrap and allows
`Bash(at *)`, `Bash(git *)` and the project's own build commands; widen
the allow list for the repository's tools or run in a sandbox with
`--dangerously-skip-permissions`. Several copies run in parallel; the
engine serialises claims and promotion.

## Codex CLI

Codex reads `AGENTS.md` natively and has no system-prompt flag, so the
project instructions are the whole contract. Interactive sessions behave
as above without hooks. Headless: `examples/hosts/codex.sh` (`codex exec
--full-auto` with the context as the prompt). Commit messages: Worktrunk's
`wt step commit` if installed, else git.

## OpenCode, Grok, anything with a shell

OpenCode reads `AGENTS.md`; `examples/hosts/opencode.sh` drives `opencode
run`. For any other harness, `examples/hosts/custom.sh` takes
`AGENT_CMD`: the command receives the task context on stdin with
`AT_SESSION` exported and must work in the printed workspace. For a bare
model behind an API, send the bootstrap as the system message and `at
context --with-rules` as the first user message.

## Planners

A planning agent writes the plan as a file and applies it atomically:

```yaml
tasks:
  - key: store
    title: Add a token store
    outcome: Package store exposes Open(path) backed by SQLite with Put/Get
    constraints: ["Use modernc.org/sqlite (pure Go)"]
    acceptance: ["Get on an unknown key returns ErrNotFound"]
    checks: ["unit: go test ./store/..."]
    pins: ["checks/", "go.mod"]
  - key: docs
    title: Document the store package
    requires: [store]
    checks: ["readme: grep -q '## Store' README.md"]
```

`at add -f plan.yaml` applies it; a bad item rejects the whole file.
Rules that the pilots showed matter: the regression suite must pass on
the target before tasks are added (`at doctor`); tasks that share one
dispatch site need a scaffolding task the others require; pin the files
a task must not change; one task is one independently verifiable outcome.

## Small changes

`at add "Fix the typo in README" --check "readme: grep -q Fixed README.md" --claim`
adds, claims and prints the workspace in one step. The cost of a task is
a worktree and two check suites; for a repository with a slow suite that
is the price of a verified change, and the right fix is a faster
regression suite, not a bypass.

## What is enforced and what is advice

Enforced by the engine everywhere: nobody sets status; completion needs
fresh passing checks; a claim is a lease; pinned files cannot change;
promotion is a verified fast-forward or merge. Enforced by hooks in
Claude Code: edits stay in worktrees; a turn does not end on an
unfinished claim. Advice only, in every harness: that a request becomes
tasks at all. Keep the instruction block short so it keeps being read.
