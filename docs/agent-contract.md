# Agent contract: CLI and JSON

The same binary serves Claude Code, Codex CLI, OpenCode, Grok, and a human
shell. No API keys, no daemon.

## Output

Set `TASKS_OUTPUT=json` once (or pass `--json`/`--output json`) and every
command prints exactly one JSON envelope on stdout and nothing else there:

```json
{"ok": true,  "result": {...}}
{"ok": false, "error": {"code": "TASK_BLOCKED", "message": "..."}}
```

Human mode prints text on stdout and `error [CODE]: message` on stderr.
Warnings (never fatal) go to stderr in both modes. Exit status: `0` success,
`1` any coded failure, `2` usage error (envelope still emitted in JSON mode).

## Error codes (stable)

| Code | Meaning | Typical action |
|---|---|---|
| `INVALID_INPUT` | malformed argument, ID, or spec | fix the call |
| `NOT_FOUND` | task/project/edge does not exist | check the ID |
| `NO_PROJECT` | no project for this directory | `tasks init` or `--project` |
| `SELF_DEPENDENCY`, `DUPLICATE_DEPENDENCY`, `CROSS_PROJECT_DEPENDENCY`, `DEPENDENCY_CYCLE` | edge rejected (nothing written) | redesign the edge |
| `TASK_BLOCKED` | prerequisites incomplete | work on them first |
| `TASK_ALREADY_TAKEN` | another attempt holds a live lease | pick another task |
| `TASK_AWAITING_VERIFICATION` / `TASK_AWAITING_INTEGRATION` / `TASK_COMPLETE` | not takeable in that state | nothing to do |
| `NO_AVAILABLE_TASK` | automatic take found nothing | wait or add work |
| `INVALID_SESSION` | token unknown or malformed | re-take |
| `LEASE_EXPIRED` | this attempt's lease ran out | `tasks take <task>` to resume |
| `SESSION_SUPERSEDED` | a newer attempt owns the task | stop; do not write files |
| `SESSION_FINISHED` | this attempt already submitted | re-take if more work is needed |
| `VERIFICATION_FAILED` | checks ran and a required one did not pass; `error.details` holds the result | read `tasks evidence <task>`, fix, re-take |
| `VERIFICATION_RUNNING` | the newest run is marked running | if that process is gone: `tasks verify <task> --retry` |
| `NOTHING_TO_VERIFY` | no submission, or it already passed | `--again` to re-verify |
| `WORKSPACE_DIRTY` | uncommitted changes in the project tree | commit (nothing is committed for you) |
| `WORKSPACE_UNAVAILABLE` | not a Git repository with a commit, or Git missing | |
| `SUBMISSION_NOT_INTEGRATED` | reserved for Milestone 3 | |
| `INTERNAL` | bug or I/O failure | report |

## Commands

```
tasks init [--name NAME] [--path DIR] [--no-dir]      register a project (cwd by default)
tasks projects                                        list projects
tasks project [--name N] [--integration none|promote] [--regression-json JSON|--regression-file F]
tasks add "description" [--outcome ..] [--constraint ..]* [--accept ..]* [--check "[id:] cmd"]* [--optional-check ..]* [--policy-json JSON]
tasks show <task>
tasks list [--available|--blocked|--in-progress|--interrupted|--awaiting-verification|--verification-failed|--awaiting-integration|--complete|--all|--status a,b]
tasks deps add <task> --requires <prerequisite>       <task> requires <prerequisite>
tasks deps remove <task> --requires <prerequisite>
tasks deps list <task>
tasks graph [--format text|json|dot|edges] [--around <task> --depth N]
tasks take [<task>] [--lease 1h]                      atomic claim; prints the session token once
tasks log <token> [--done ..] [--next ..] [--learned ..] [--note ..]
tasks renew-task-lease <token> [--lease 1h]           (alias: renew)
tasks finish <token> [--no-verify]                    submit; runs the checks unless --no-verify
tasks verify <task> [--retry] [--again]               run (or re-run) the checks for the newest submission
tasks evidence <task> [--run N] [--full]              verification runs and per-check evidence
tasks policy <task> [--check ..]* [--optional-check ..]* [--policy-json ..] [--clear]
tasks history <task> [--limit N] [--offset N]
tasks whoami <token>                                  which task does this token belong to
tasks version
```

Global flags may appear before or after the command: `--db PATH`,
`--project ID|NAME`, `--json`, `--output text|json`. Environment:
`TASKS_DB`, `TASKS_PROJECT`, `TASKS_OUTPUT`, `TASKS_SESSION`.

### Dependency direction

`tasks deps add B --requires A` means **B requires A**: A must be complete
before B can be taken. `tasks graph --format edges` prints `B requires A`.
DOT output draws an arrow from B to A. There is no other direction.

### Token transport

`<token>` may be: a positional argument; `-` to read one line from stdin;
or omitted to use `TASKS_SESSION`. Positional secrets can show up in `ps`
and shell history; prefer the environment variable or stdin where other
users share the machine. Tokens are never printed by `show`, `list`,
`history`, `graph`, or `deps`.

## The `take` result

```json
{
  "task_id": "task-k7p4m2x9q6r8v5z1",
  "token": "sess-...",
  "attempt": 2,
  "lease_expires_at": "2026-03-01T10:00:00Z",
  "resumed": true,
  "task": {
    "task": {"id": "...", "description": "...", "outcome": "...", "constraints": [], "acceptance": [...], "verification": {...}},
    "status": "in_progress",
    "requires": [{"id": "...", "status": "complete", "description": "..."}],
    "required_by": [...],
    "attempt": {"seq": 2, "lease_expires_at": "...", "lease_active": true},
    "submission": {"id": 1, "verification": "failed", ...},
    "policy_digest": "sha256:..."
  },
  "handoff": {
    "latest_next": "Test malformed expiry values",
    "learnings": ["Expiry validation is shared by two middleware paths"],
    "recent_logs": [{"seq": 7, "attempt": 1, "at": "...", "done": "...", "next": "..."}],
    "total_logs": 7,
    "warnings": []
  }
}
```

`handoff.recent_logs` holds at most 10 entries, newest first; `learnings`
at most 50; `history` returns everything, paged.

## `finish` and verification

`finish` records the submission first (ending the session), then runs the
effective policy (task checks + project regression) in the project
directory. If the directory is a Git repository the tree must be clean and
HEAD is recorded as the submission's revision; checks refuse to run on a
tree that no longer matches it. Success returns `ok: true` with
`result.verification` and `result.status`. A failed check returns
`ok: false`, code `VERIFICATION_FAILED`, and the same result under
`error.details`; the submission, evidence, logs and learnings are all kept
and the task becomes `verification_failed` (takeable for repair). With
`--no-verify` the task waits at `awaiting_verification` until `tasks verify`.

Evidence for a check is reused instead of re-executed when an identical
check already passed at the same revision.

## Statuses

`blocked`, `available`, `in_progress`, `interrupted`,
`awaiting_verification`, `verification_failed`, `awaiting_integration`,
`complete`. They are derived on every read; there is no command to set one.

## Recommended agent loop

```
S=$(tasks take --json | jq -r .result.token)   # or: tasks take <task>
# read .result.task and .result.handoff; work in the repo
tasks log "$S" --done "..." --next "..." --learned "..."
tasks renew-task-lease "$S"                     # if working longer than the lease
tasks finish "$S"                               # submits; status tells you what happened
```

On any `LEASE_EXPIRED`: run `tasks take <task>` again, read the handoff, and
continue. On `SESSION_SUPERSEDED`: stop; another attempt owns the task.
