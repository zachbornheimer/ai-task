# atask contract

## CLI

```text
atask
atask help [COMMAND]
atask version
atask context [TASK_ID]
atask context --hook [--changed]

atask list [all|open|ready|blocked|draft|complete|archived]
atask show [TASK_ID] [--full]
atask status

atask add "TITLE" [FLAGS]
atask add -f PLAN            # yaml or json; - for stdin
atask update TASK_ID [FLAGS]

atask claim [TASK_ID] [--wait]
atask claim --group REF [--wait]
atask claim renew [TOKEN|-]
atask claim release [TOKEN|-] [--note TEXT] [--failed]

atask log [--done TEXT] [--next TEXT] [--learned TEXT] [--note TEXT]

atask verify proof
atask verify regression
atask verify complete [TASK_ID] [--reverify]

atask init [--name NAME] [--path PATH]
atask project add NAME
atask projects
atask project [FLAGS]
atask doctor
atask prune
```

### Task flags (`add` and `update`)

```text
--key KEY
--group
--parent TASK_ID
--outcome TEXT
--constraint TEXT
--accept TEXT
--requires TASK_ID
--blocks TASK_ID
--proof "PRESET: SUBJECT [k=v]"        proves this task works, small (30 s) unless size= says otherwise
--regression "PRESET: SUBJECT [k=v]"   proves nothing else broke, small unless size= says otherwise
--on-complete "PRESET: SUBJECT [k=v]"  run after completion, waited, large (900 s) unless size= says otherwise
--on-complete-detach "PRESET: ..."     run after completion, not waited on

--remove-requires TASK_ID
--archive --reason TEXT
--reset-attempts
```

### Project flags (`project`, human-configured; a project holds no checks)

```text
--target-branch NAME
--attempts N
```

### Global flags

```text
--project NAME|ID      default: general; names match ignoring case and punctuation; repeatable on add and update, the first is the home
--db PATH
--json                 also ATASK_OUTPUT=json
```

### Presets

Every task declares two proofs: `--proof` proves this task works, `--regression`
proves it broke nothing else. Every command is `"PRESET: SUBJECT [key=value ...]"`.
The subject is the one thing the preset is about; options are `key=value`.
`run` is the preset for a raw command. Presets run in the task worktree, inside
`mise exec` when the repo has a mise config. `atask help add` lists built-ins and
custom presets with their subjects, options and default sizes. `atask show` prints both
proofs verbatim.

| Preset                                           | Passes when                                                                                                            | Size   |
| ------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- | ------ |
| `run: CMD [expect=REGEX]`                        | argv exits 0 and stdout matches `expect`; `sh -c` only for shell syntax; args capped at 300 chars, no encoded payloads | small  |
| `ci`                                             | `mise run ci`, else `make ci`, exits 0                                                                                 | large  |
| `lint`                                           | `mise run lint`, else `make lint`, exits 0                                                                             | medium |
| `build`                                          | `mise run build`, else `make build`, exits 0                                                                           | medium |
| `go_test: PKG... [name=REGEX]`                   | `go test -count=1 -json PKG...` passes and, with `name`, a matching test ran and passed                                | small  |
| `py_test: PATH [name=EXPR]`                      | `pytest PATH -k EXPR` passes and collected at least one test                                                           | small  |
| `js_test: PATH [name=REGEX]`                     | package-manager test runner passes with at least one test run                                                          | small  |
| `rs_test: CRATE [name=REGEX]`                    | `cargo test` passes with at least one test run                                                                         | medium |
| `<lang>_test: PATH [name=..]`                    | that language's canonical runner (pl, php, rb, swift, sh), same rule                                                   | small  |
| `playwright_run: SPEC [grep=REGEX project=NAME]` | `playwright test SPEC` passes with at least one test run                                                               | medium |
| `file_exists: PATH`                              | PATH exists                                                                                                            | small  |
| `file_absent: GLOB [depth=N]`                    | nothing matches GLOB within N directory levels (default: any)                                                          | small  |
| `file_blake3: PATH hash=HEX`                     | blake3(PATH) equals hash                                                                                               | small  |
| `grep: PATH pattern=REGEX [absent=true]`         | REGEX matches in PATH (or does not, with `absent`)                                                                     | small  |
| `http_ok: URL [status=CODE body=REGEX]`          | GET returns the status (default 2xx) and body matches                                                                  | small  |
| `pr_merged: NUMBER\|URL`                         | the PR is merged into the target branch                                                                                | small  |

Custom presets live in `~/.config/atask/config.toml` and are listed by
`atask help add` beside the built-ins. `{subject}` and `{option}` substitute
into the argv; a missing required option is `INVALID_INPUT` at add.

```toml
[presets.e2e_console]
command = ["npx", "playwright", "test", "{subject}", "--reporter=json"]
options = ["grep"] # optional keys the preset accepts
size = "medium"
```

Every check spec takes `size=small|medium|large|eternal` as its last option:
30 s, 300 s, 900 s, or no budget (`budget_ms` is `null`; the run still
heartbeats and the orphan reaper still applies). `size=` overrides a preset's
`size`; any other value is `INVALID_INPUT`. Without `size=`, a `--proof` is
small, a `--regression` takes its preset's default size, and an action is
large. For example `--regression 'run: go test ./... size=large'`. Rejected at add: `run: true` and other no-ops, a
small `go_test` on a whole tree with no `name`, and `run` arguments that embed
encoded code. A `*_test` that runs zero tests fails at verify; exit 0 alone
never passes a `*_test` preset.

Size belongs to the check alone: a task or group has no size of its own, `show` and `list` print none, a group's budget is its proof's `size=`, and a plan file's `size` key is `INVALID_INPUT` ("size belongs on the check: add size=... to the proof or regression").

### Run result

Every proof, regression, and action run must finish inside its budget. Checks and actions inherit only PATH, HOME, USER, LOGNAME, SHELL, LANG, LC_*, TERM, TMPDIR, GO*, GIT_*, MISE_*, NODE_*, NPM_CONFIG_*, PLAYWRIGHT_*, UV_*, CI and ATASK_* from the parent (never a session token); `[sandbox] extra_env = ["NAME", ...]` in `config.toml` adds names. A run
that exceeds it is killed and fails. Each run records one shape, shown by
`atask show --full` and returned by `verify`:

```json
{
  "check_type": "proof | regression | on_complete",
  "preset": "go_test",
  "subject": "./internal/cli",
  "options": { "name": "TestListHonorsProjectFlag" },
  "started_at": "2026-10-13T13:40:00Z",
  "elapsed_ms": 1840,
  "budget_ms": 30000,
  "timed_out": false,
  "return_code": 0,
  "passed": true,
  "output": "last 64 KiB of combined stdout and stderr",
  "sandboxed": true
}
```

| Flag                   | Budget                                        |
| ---------------------- | --------------------------------------------- |
| `--proof`              | its `size=`: 30 s unless set                  |
| `--regression`         | its `size=`: 30 s unless set or the preset's  |
| `--on-complete`        | its `size=`: 900 s unless set                 |
| `--on-complete-detach` | its `size=` (900 s unless set), not waited on |

### Sandbox

On macOS every proof, regression and `--on-complete` command runs under a
generated Seatbelt profile (`sandbox-exec`): writes under `$HOME` are denied
except to the check's worktree and its git directory, the Go, mise and
tool caches, and `TMPDIR`; reads of `~/.ssh`, `~/.aws`, `~/.gnupg`,
`~/.config/op`, `~/.config/gh`, `~/Library/Keychains`, `~/.netrc`, `~/.docker`
and `~/.local/state/atask` are denied. Run results report `"sandboxed": true`.
A check run by an already confined `atask` inherits that confinement.
Rootless projects (no repository, including `general`) run every check and
action unconfined: their claim workspace is just the directory the task was
claimed from, and their proofs routinely reach other checkouts and config
paths on the machine. Those runs report `"sandboxed": false`.

```toml
# ~/.config/atask/config.toml
[sandbox]
mode = "strict" # default; "none" runs checks unconfined
extra_write = ["~/scratch"]
```

Strict mode fails closed: without `sandbox-exec` the run fails `INVALID_INPUT`
("sandbox unavailable: ...") before any check starts. On other platforms
checks run unconfined and report `"sandboxed": false`.

### Output and errors

Every command prints `{"ok":true,"result":...}` or
`{"ok":false,"error":{"code":CODE,"message":TEXT}}` under `--json`. Codes:
`INVALID_INPUT`, `NOT_FOUND`, `CONFLICT`, `INVALID_SESSION`, `NOT_READY`,
`BASE_CONFLICT`, `CHECK_FAILED`, `IDEMPOTENCY_CONFLICT`, `DONE` (claim --wait
with an empty board, or `claim --group` on a finished group), `STALLED` (claim --wait
with only blocked tasks). `claim --group` answers `NOT_READY` with `details`
`{group, group_proofs_pending, active}` when nothing is claimable yet.

Lease is 30 minutes; `claim renew` extends it by 30 minutes from now and never
shortens it. Default attempt budget is 3 per task.

## Public Code API

```go
type Engine interface {
    // Planning
    Apply(ctx context.Context, set ChangeSet) (ApplyResult, error)
    List(ctx context.Context, q ListQuery) (PlanSnapshot, error)
    Show(ctx context.Context, id TaskID) (TaskView, error)

    // Execution
    Claim(ctx context.Context, q ClaimRequest) (Session, error)
    Renew(ctx context.Context, token SessionToken) (time.Time, error)
    Release(ctx context.Context, q ReleaseRequest) error
    Log(ctx context.Context, token SessionToken, entry LogEntry) error

    // Verification, completion, action retries
    Verify(ctx context.Context, q VerifyRequest) (VerifyResult, error)
}
```

### Core types

```go
type ChangeSet struct {
    ProjectID       ProjectID
    ExpectedPlanRev uint64
    IdempotencyKey  string
    Operations      []Change
}

type TaskDefinition struct {
    Key         string
    Kind        TaskKind
    Title       string
    Parent      TaskID
    Outcome     string
    Constraints []string
    Acceptance  []string
    Projects    []ProjectID          // ordered; the first is the home
    Requires    []TaskID
    Proof       ProofSpec   // proves this task works, 30 s
    Regression  ProofSpec   // proves nothing else broke, sized
    OnComplete  []ActionSpec
}

type ProofSpec struct {
    Preset  string            // "run", "ci", "go_test", ...
    Subject string
    Options map[string]string
    Size    Size              // from size=; zero value = the preset default
}

type ActionSpec struct {
    Preset  string
    Subject string
    Options map[string]string
    Detach  bool
    Size    Size   // from size=; zero value = SizeLarge
}

type RunResult struct {
    CheckType  CheckType          // proof, regression, on_complete
    Spec       ProofSpec
    StartedAt  time.Time
    Elapsed    time.Duration
    Budget     time.Duration
    TimedOut   bool
    ReturnCode *int               // nil when detached or killed
    Passed     bool
    Output     string             // last 64 KiB
    Sandboxed  bool               // ran inside the Seatbelt sandbox
}

type ActionResult struct {
    Spec     ActionSpec
    Status   ActionStatus
    Attempts int
    Run      *RunResult           // nil until the action has run
}

type ClaimRequest struct {
    ProjectID ProjectID
    TaskID    *TaskID
    Wait      bool
}

type Session struct {
    Task       TaskView
    Token      SessionToken
    LeaseUntil time.Time
    Workspace  string
    Handoff    string      // what `atask context TASK_ID` prints
}

type ReleaseRequest struct {
    Token  SessionToken
    Note   string
    Failed bool          // counts an attempt against the task
}

type VerifyRequest struct {
    TaskID   TaskID
    Token    SessionToken
    Mode     VerifyMode
    Reverify bool
}
```

### Constants

```go
const (
    VerifyProof      VerifyMode = "proof"
    VerifyRegression VerifyMode = "regression"
    VerifyComplete   VerifyMode = "complete"
)

const (
    KindTask  TaskKind = "task"
    KindGroup TaskKind = "group"
)

const (
    SizeSmall   Size = "small"    // 30 s
    SizeMedium  Size = "medium"   // 300 s
    SizeLarge   Size = "large"    // 900 s
    SizeEternal Size = "eternal"  // no budget
)

const (
    CheckProof      CheckType = "proof"
    CheckRegression CheckType = "regression"
    CheckOnComplete CheckType = "on_complete"
)

const (
    ActionPending   ActionStatus = "pending"
    ActionRunning   ActionStatus = "running"
    ActionDetached  ActionStatus = "detached"
    ActionSucceeded ActionStatus = "succeeded"
    ActionFailed    ActionStatus = "failed"
)

const (
    FilterAll      ListFilter = "all"
    FilterOpen     ListFilter = "open"
    FilterReady    ListFilter = "ready"
    FilterBlocked  ListFilter = "blocked"
    FilterDraft    ListFilter = "draft"
    FilterComplete ListFilter = "complete"
    FilterArchived ListFilter = "archived"
)
```

### Contract semantics

- `Apply`: atomic creation, updates, parent relationships, dependencies, both
  proofs, and `on_complete` configuration. A task filed without both proofs is a draft: listed under its own glyph, never claimable, no attempt budget. Giving it both proofs makes it ready. Vacuity rules apply once proofs are supplied. A task missing any required field is a draft; the required fields are proof and regression (the title is required at add). Adding an open title that already exists
  returns the existing task.
- `List` / `Show`: task state, hierarchy, history, every `RunResult`, and
  action results. When the proof, regression, or on-complete actions of a task that has ever been claimed change, the change is recorded, never refused: `Show` returns `check_history` (time, field, old and new spec, actor, cwd; the actor is `attempt:<id>` for a session token and `planner` otherwise), and `atask show` prints it under CHECK HISTORY. `List` marks such a task with `△` right after its status glyph (a draft is `◌`). `List` without `--project` shows every project's non-archived tasks with the project name on each row (the board); `--project NAME` scopes to one project. `general` is the project for tasks filed outside a registered repo. The list is board-wide by default. A task belongs to one or more projects: the first is its home, which decides its repo, worktree, target branch and base and is fixed after the first claim, and the rest are memberships; `list --project X` includes every task naming X, `--requires` needs one shared project, and a group's children share the group's home.
- `Claim`: atomic eligibility and exclusive leased assignment; isolated
  worktree for Git tasks. The token is discoverable from the task worktree;
  a rootless claim's token is also stored under the state dir keyed by the claim directory, so log, verify, renew and release work from that directory;
  elsewhere it is passed explicitly or via `ATASK_SESSION`. A rootless claim's
  workspace is the claiming directory, not the directory the task was filed
  from: its proof, regression and on-complete actions run there, and a
  re-claim after release moves the workspace to the new claiming directory.
  Its checks get a private `TMPDIR` (`docs/verification.md`, "Check TMPDIR");
  a claim from a directory where that cannot be created and written is
  refused with `INVALID_INPUT`.
- `Renew` / `Release` / `Log`: session-fenced. `Release` with `Failed` counts
  an attempt; without it the attempt is not counted.
- `VerifyProof` / `VerifyRegression`: fresh diagnostic run, no completion.
- `VerifyComplete` also returns `checks_changed_after_claim` (true when the task's checks changed since the current attempt began) and, when true, those changes as `check_changes`.
- `VerifyComplete`: fresh proof, fresh regression, then atomically complete the
  task and record its `on_complete` actions as pending. atask then runs each
  action: waited actions record a `RunResult`; detached actions are spawned
  and recorded as `detached`.
- `VerifyComplete` on a completed task: retry pending or failed actions without
  repeating succeeded ones.
- `Reverify: true`: fresh verification against the recorded revision; append
  evidence without undoing completion or re-running succeeded actions.
- Dependencies: a task is ready when every required task is complete. At claim
  its branch starts at the target tip with every required task's branch tip
  merged in (a requirement already contained in the target adds nothing). A
  conflicting merge fails the claim with `BASE_CONFLICT` and the task stays
  blocked with the conflict named.
- Groups: a group's proofs run on the merge of every child's completed
  revision, and the group completes only after every child is complete. A
  child whose completed revision no longer merges onto the target (the target
  integrated and changed it) counts as landed when its own proof passes on the
  target tip; otherwise the group fails `CHECK_FAILED` naming the child.
  Parents own no integration branch.
- Projects: `general` exists by default and holds tasks filed outside a
  registered repo. `init` registers a repo (it refuses the home directory and its ancestors). `project` sets target branch and
  attempt budget only; a project holds no checks. Agents do not run it.
  `project add NAME` creates a project with no repository, stored like
  `general` (no root, no target branch, so `project --target-branch` on it is
  `INVALID_INPUT`); adding a name that matches an existing project (same
  letters and digits, ignoring case and punctuation) returns it. A name
  lookup that matches more than one project is `INVALID_INPUT`; use the id.
  Its tasks have no
  worktree, and their checks run in the directory the task was claimed from.
- Agent execution, worker pools, planning loops, and model calls stay outside
  atask.
