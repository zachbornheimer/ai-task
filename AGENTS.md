<!-- atask:begin -->

# Working with `atask` (for coding agents)

`atask` is the task engine for this repository: it hands you one claimed task
at a time, remembers what the previous attempt learned, and decides
completion from fresh verification, never from your word. It works the
same from Claude Code, Codex CLI, OpenCode, Grok, or a shell. Every `atask`
command prints one JSON envelope when `ATASK_OUTPUT=json` is set; read
`error.details` when one fails.

## How work gets done here

Everything that changes this repository goes through `atask`.

- A question, an explanation, or a review is not a task: answer it.
- Anything else starts with the plan: `atask context` (or `atask status` and
  `atask list`). If a task already covers the request, claim it. Otherwise
  add tasks, each with a check that proves it, then claim one:
  `atask add "title" --check "id: cmd"`, several at once with
  `atask add -f plan.yaml`, and `atask add "title" --check "..." --claim` for
  one small change. Run `atask doctor` before planning in a repository you
  have not worked in.
- Never edit the target branch directly; a passing `atask verify complete`
  promotes verified work there.
- One task = one independently verifiable outcome. Plan the checks before
  the code; the regression suite carries the weight, task checks prove
  the outcome, and `--pin` protects files the task must not touch.
- Checks must be fast. A task is `--size small` (30s of checks) unless
  declared medium (5m) or large (15m); the regression gate has 2m. A check
  that runs out of time is a verification-construction problem, not a
  code failure: prove only this outcome (`-run`, one package), split the
  task, or declare the size. Regression checks get `ATASK_CHANGED_FILES` to
  target affected tests; the full suite belongs in CI.
- An epic is a group with its own checks (`atask add "Epic" --group --check "e2e: ..."`,
  size large by default): the integration tier, run once on the target
  branch when every member is complete. The group is complete only when
  they pass; a failure needs follow-up member tasks, not a bigger budget.

## Verbs

```text
atask context                                           your task: outcome, checks, workspace, state, handoff (or the plan, with no task in hand)
atask claim [REF] [--wait]                              one leased task, its worktree on atask/<id>; the token is stored there
atask log --done "..." --next "..." --learned "..."     record progress; next is what the next attempt reads first
atask verify task                                       run this task's checks (diagnostic, repeatable)
atask verify regression                                 run the project's regression checks (diagnostic)
atask verify complete                                   commit first; runs BOTH suites fresh and completes only if all pass
atask add "prereq" --blocks <task> --check "id: cmd"    discovered prerequisite: then log and release
atask claim release                                     hand the task back; keeps your handoff
atask claim renew                                       extend the lease (every command does too)
```

## Agent execution contract

Complete one independently verifiable task at a time.

Start

- Run `atask context` before working. Review the handoff, the existing changes and `git status` before editing; preserve useful prior work.
- Work only in the assigned workspace. The session token is stored there, so `atask` commands run there need no token.
- Do not implement other tasks.

Execute

- Implement the outcome and satisfy its acceptance criteria.
- The checks are the contract: never weaken, skip or bypass a check or the tests it runs.
- Iterate with `atask verify task` and `atask verify regression` (diagnostic; they never complete anything).
- Record progress with `atask log --done "..." --next "..." --learned "..."`; `--next` is what the next attempt reads first.
- The lease lasts 30 minutes and every `atask` command renews it; log before it runs out.
- On LEASE_EXPIRED or SESSION_SUPERSEDED, stop editing immediately. The next `atask claim <task>` returns your handoff.

Complete

- Commit before `atask verify complete` (`wt step commit` where Worktrunk is installed, plain git otherwise). The tree must be clean; nothing is committed for you.
- You never set status. Only a passing `atask verify complete` completes the task: it runs every required task check and regression check fresh on the committed revision, then promotes the branch to the target. When the target had moved it merges for you and reports `integrated_revision`.
- On VERIFICATION_FAILED, read `error.details`, fix the cause, commit, verify again. Your claim stays live.
- On INTEGRATION_FAILED, the target moved against you: `wt step rebase <target>` (or `git merge <target>`), resolve `error.details.conflicts`, commit, verify again. It is not counted against you.
- If completion depends on work that is not yours: `atask add "prereq" --blocks <task> --check "id: cmd"`, `atask log` the handoff, `atask claim release`. Do not implement the blocking task.

Exit

- `atask claim --wait` ends with DONE (everything is complete) or STALLED (a planner is needed); both are normal exits for a worker loop.
- Never claim success without a passing final verification.

<!-- atask:end -->

## Engineering guidelines

Edit this section for the repository; the block above is owned by `atask init`.

- Prefer the smallest complete change that satisfies the task; follow the
  existing architecture and conventions; avoid unrelated refactoring,
  formatting, or dependency changes.
- Handle errors explicitly; preserve existing behaviour and public
  contracts unless the task requires otherwise.
- Test observable behaviour, including failure paths, with deterministic
  tests; fix defects rather than weakening assertions.
- Read the code and documentation the task needs, not the repository by
  default; keep documentation consistent when a documented contract changes.
- Report results and blockers concisely; distinguish verified results from
  assumptions.

## This repository

`atask` is developed here; the block above is what `atask init` installs into
any repository. Planning verbs (`atask add`, `atask update`, `atask show`,
`atask list`) are the planner's; `docs/agent-contract.md` is the full
reference, `docs/integrations.md` says how each harness is wired, and
`examples/embedded_runner` shows the host loop. Run `go test ./...`
before `atask verify complete`; `gofmt` and `go vet` are regression checks.
