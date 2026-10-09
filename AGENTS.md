# Working with `tasks` (for coding agents)

`tasks` hands you one unit of work at a time, remembers what the previous
attempt learned, and decides completion from evidence, not from your word.
It works the same from Claude Code, Codex CLI, OpenCode, Grok, or a shell.

## Setup once per shell

```sh
export TASKS_OUTPUT=json          # every command prints one JSON envelope
tasks init                        # once per repository (any worktree of it resolves to the same project)
```

## The loop

1. **Take work.** `tasks take` picks the best task (interrupted work first);
   `tasks take <task-id>` picks a specific one. The result contains the
   contract (`result.task.task`), prerequisites, and the `handoff`:
   `latest_next`, `learnings`, `recent_logs`. Save `result.token`
   (export it as `TASKS_SESSION` so later commands need no argument).
2. **Work in the repository** using your own tools.
3. **Log as you go.** `tasks log --done "..." --next "..." --learned "..."
   --note "..."` (at least one field). `next` is what the following attempt
   will read first; `learned` is for reusable discoveries.
4. **Renew if long-running.** Leases default to 1 hour:
   `tasks renew-task-lease`.
5. **Finish.** Commit your work (the tree must be clean; nothing is committed
   for you), then `tasks finish`. It records the submission and runs the
   task's checks. `ok: true` with `result.status` `complete` means done.
   `VERIFICATION_FAILED` means the submission is recorded but a required
   check failed: read `error.details` or `tasks evidence <task-id>`, then
   `tasks take <task-id>` again to repair. Finishing never marks a task
   complete on its own word.

## When things go wrong

- `LEASE_EXPIRED`: run `tasks take <task-id>` again; you get a new token and
  your own handoff.
- `SESSION_SUPERSEDED`: another attempt owns the task. Stop editing files.
- `TASK_BLOCKED`: `tasks deps list <task-id>` shows what must finish first.
- `WORKSPACE_DIRTY`: commit or remove uncommitted files, then finish again.
- `VERIFICATION_RUNNING`: a previous run died; `tasks verify <task-id> --retry`.
- Lost context: `tasks whoami <token>` or `tasks show <task-id>`.

## Dependencies and blockers

`tasks deps add B --requires A` means **B requires A**. Create tasks with
their edges: `tasks add "Reject expired access tokens" --requires <parser-task>
--accept "expired token -> 401" --check "unit: go test ./auth/..."`.

Found something that must happen first while working on task C? Record it
and hand C back:

```sh
tasks add "Add the token clock interface" --blocks C
tasks log --learned "..." --next "resume once the interface exists"
tasks release          # C is blocked until the new task completes, then available with your handoff
```

Something only a human can do: `tasks add "Approve API key" --manual
--blocks C`. Machines never auto-take it; `tasks status` reports `stuck`
with `manual_pending` until a person finishes it.

One task = one independently verifiable outcome; if you would write "and"
in the outcome, make two tasks.

## Driving a whole graph

`tasks take --wait 10m` blocks until something is takeable;
`tasks status` says `done` or `stuck`. See `examples/executor.sh`.

## Inspect

`tasks list --available` (fresh work), `tasks list --takeable` (also
interrupted and failed work), `tasks list --blocked`, `tasks show <id>`,
`tasks history <id>`, `tasks graph --format dot`.

Full reference: `docs/agent-contract.md`.
