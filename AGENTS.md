# Working with `at` (for coding agents)

`at` hands you one claimed task at a time, remembers what the previous
attempt learned, and decides completion from fresh verification, never from
your word. It works the same from Claude Code, Codex CLI, OpenCode, Grok,
or a shell. You need five verbs; two more for exceptions.

Your host sets `AT_OUTPUT=json` and starts you inside the task's own Git
worktree (branch `at/<id>`). The session token lives in that worktree, so
`at` commands run there need no token; `AT_SESSION` is only needed from
elsewhere.

```
at log --done "..." --next "..." --learned "..."     record progress; next is what the next attempt reads first
at verify task                                       run this task's checks (diagnostic, repeatable)
at verify regression                                 run the project's regression checks (diagnostic)
at verify complete                                   commit first; runs BOTH suites fresh and completes only if all pass
at add "prereq" --blocks <task> --check "id: cmd"    discovered prerequisite: then log and release
at claim release                                     hand the task back; keeps your handoff
at claim renew                                       extend the lease (every command does too)
```

Rules of the road:

- You never set status. `verify complete` is the only path to completion
  and it re-runs everything; `VERIFICATION_FAILED` leaves your claim live
  with the evidence in `error.details`. Fix, commit, verify again.
- Commit before `verify complete`; the tree must be clean and nothing is
  committed for you.
- Your lease is 30 minutes and every `at` command renews it; `at log`
  progress at least that often.
- Read the handoff the claim printed: previous attempts' next step and
  learnings, learnings inherited from prerequisites, and the last failed
  checks with their output.
- `LEASE_EXPIRED` or `SESSION_SUPERSEDED`: stop editing immediately. The
  next `at claim <task>` returns your handoff.
- One task = one independently verifiable outcome.

Planning verbs (`at add`, `at update`, `at show`, `at list`) are for the
planner; `docs/agent-contract.md` has the full reference, and
`examples/embedded_runner` shows the host loop.
