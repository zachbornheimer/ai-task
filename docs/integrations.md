# Using `atask` from AI systems

The goal: every request an agent receives in a repository is routed
through `atask`, so work is planned with proofs, done in isolated worktrees,
completed only by fresh verification, and remembered across sessions and
harnesses. This page says how each harness is wired. The engine never
calls a model; the harness does.

## Three layers

| Layer                | Holds                                                               | Where it lives                                                                                                                                        |
| -------------------- | ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| Bootstrap            | identity, the `atask` CLI, JSON envelopes, stop on lost authority   | the harness's system prompt where it can be set (`claude -p --append-system-prompt`, a bare model's system message); text in `docs/agent-contract.md` |
| Project instructions | how work gets done here, the verbs, the execution contract          | `AGENTS.md` block installed by `atask init`; `CLAUDE.md` imports it                                                                                   |
| Task turn            | outcome, constraints, acceptance, proofs, workspace, state, handoff | `atask context ID` (worker handoff); `atask show` for the current claim; `atask list` for the plan                                                    |

The project file is the lever: it says that any change starts with the
plan, that questions are not tasks, and that the target branch is never
edited directly.

## Setup, once per repository

```sh
cd repo
atask init                                           # AGENTS.md block + CLAUDE.md import
atask project --target-branch main
atask doctor                                         # repository, target, identity, regression green on the target
git add AGENTS.md CLAUDE.md && git commit -m "Route agent work through atask"
```

## Claude Code

Interactive: open the repository. The agent runs `atask context`
once for the contract, then `atask list` / `atask context ID`, claims,
works in the workspace, commits, `atask verify complete`.

Headless, one worker:

```sh
examples/hosts/claude-code.sh        # loops: claim --wait, context ID | claude -p, release if not completed
```

The script passes `--append-system-prompt` with the bootstrap and allows
`Bash(atask *)`, `Bash(git *)` and the project's own build commands; widen
the allow list for the repository's tools or run in a sandbox with
`--dangerously-skip-permissions`. Several copies run in parallel; the
engine serialises claims.

## Codex CLI

Codex reads `AGENTS.md` natively and has no system-prompt flag, so the
project instructions are the whole contract. Interactive sessions behave
as above. Headless: `examples/hosts/codex.sh` (`codex exec
--full-auto` with `atask context ID` as the prompt). Commit messages: Worktrunk's
`wt step commit` if installed, else git.

## OpenCode, Grok, anything with a shell

OpenCode reads `AGENTS.md`; `examples/hosts/opencode.sh` drives `opencode
run`. Grok reads `AGENTS.md`; `examples/hosts/grok.sh` drives
`grok --always-approve --output-format text` with `atask context ID` as the
prompt. For any other harness, `examples/hosts/custom.sh` takes
`AGENT_CMD`: the command receives the worker handoff (`atask context ID`) on stdin with
`ATASK_SESSION` exported and must work in the printed workspace. For a bare
model behind an API, send the bootstrap as the system message, `atask
context` as the operating contract, and `atask context ID` as the task turn.

## Planners

A planning agent writes the plan as a file and applies it atomically:

```yaml
tasks:
  - key: store
    title: Add a token store
    outcome: Package store exposes Open(path) backed by SQLite with Put/Get
    constraints: ["Use modernc.org/sqlite (pure Go)"]
    acceptance: ["Get on an unknown key returns ErrNotFound"]
    proof: "run: go test ./store/..."
    regression: "run: go test ./..."
  - key: docs
    title: Document the store package
    requires: [store]
    proof: "grep: README.md pattern='## Store'"
    regression: "run: go test ./..."
```

`atask add -f plan.yaml` applies it; a bad item rejects the whole file.
Rules that the pilots showed matter: the regression suite must pass on
the target before tasks are added (`atask doctor`); tasks that share one
dispatch site need a scaffolding task the others require; one task is one independently verifiable outcome.

## Small changes

`atask add "Fix the typo in README" --proof "grep: README.md pattern=Fixed" --regression "run: go test ./..."`
then `atask claim` prints the workspace. The cost of a task is
a worktree and two proofs; for a repository with a slow suite that
is the price of a verified change, and the right fix is a faster
regression suite, not a bypass.

## What is enforced and what is advice

Enforced by the engine everywhere: nobody sets status; completion needs
fresh passing proofs; a claim is a lease; completion never moves the
target branch. Advice only, in every harness: that a request becomes
tasks at all. Keep the instruction block short so it keeps being read.
