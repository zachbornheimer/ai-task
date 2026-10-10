# Working with `at` (for coding agents)

`at` hands you one claimed task at a time, remembers what the previous
attempt learned, and decides completion from fresh verification, never from
your word. It works the same from Claude Code, Codex CLI, OpenCode, Grok,
or a shell.

Your host sets `AT_OUTPUT=json` and starts you inside the task's own Git
worktree (branch `at/<id>`). The session token lives in that worktree, so
`at` commands run there need no token; `AT_SESSION` is only needed from
elsewhere. Every `at` command prints one JSON envelope; read `error.details`
when one fails.

```
at context                                           your task: outcome, checks, workspace, state, handoff
at log --done "..." --next "..." --learned "..."     record progress; next is what the next attempt reads first
at verify task                                       run this task's checks (diagnostic, repeatable)
at verify regression                                 run the project's regression checks (diagnostic)
at verify complete                                   commit first; runs BOTH suites fresh and completes only if all pass
at add "prereq" --blocks <task> --check "id: cmd"    discovered prerequisite: then log and release
at claim release                                     hand the task back; keeps your handoff
at claim renew                                       extend the lease (every command does too)
```

## Agent execution contract

Complete one independently verifiable task at a time.

Start

- Run `at context` before working. Review the handoff, the existing changes and `git status` before editing; preserve useful prior work.
- Work only in the assigned workspace. The session token is stored there, so `at` commands run there need no token.
- Do not implement other tasks.

Execute

- Implement the outcome and satisfy its acceptance criteria.
- The checks are the contract: never weaken, skip or bypass a check or the tests it runs.
- Iterate with `at verify task` and `at verify regression` (diagnostic; they never complete anything).
- Record progress with `at log --done "..." --next "..." --learned "..."`; `--next` is what the next attempt reads first.
- The lease lasts 30 minutes and every `at` command renews it; log before it runs out.
- On LEASE_EXPIRED or SESSION_SUPERSEDED, stop editing immediately. The next `at claim <task>` returns your handoff.

Complete

- Commit before `at verify complete` (`wt step commit` where Worktrunk is installed, plain git otherwise). The tree must be clean; nothing is committed for you.
- You never set status. Only a passing `at verify complete` completes the task: it runs every required task check and regression check fresh on the committed revision, then promotes the branch to the target. When the target had moved it merges for you and reports `integrated_revision`.
- On VERIFICATION_FAILED, read `error.details`, fix the cause, commit, verify again. Your claim stays live.
- On INTEGRATION_FAILED, the target moved against you: `wt step rebase <target>` (or `git merge <target>`), resolve `error.details.conflicts`, commit, verify again. It is not counted against you.
- If completion depends on work that is not yours: `at add "prereq" --blocks <task> --check "id: cmd"`, `at log` the handoff, `at claim release`. Do not implement the blocking task.

Exit

- `at claim --wait` ends with DONE (everything is complete) or STALLED (a planner is needed); both are normal exits for a worker loop.
- Never claim success without a passing final verification.
Planning verbs (`at add`, `at update`, `at show`, `at list`) are for the
planner; `docs/agent-contract.md` has the full reference, and
`examples/embedded_runner` shows the host loop.
