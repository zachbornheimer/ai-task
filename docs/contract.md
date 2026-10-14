# atask contract

## CLI

```text
atask
atask help [COMMAND]
atask version
atask context [TASK_ID]

atask list [all|open|ready|blocked|draft|complete|archived]
atask show [TASK_ID] [--full]
atask status

atask add "TITLE" [FLAGS]
atask add -f PLAN            # yaml or json; - for stdin
atask update TASK_ID [FLAGS]

atask claim [TASK_ID] [--wait]
atask claim renew [TOKEN|-]
atask claim release [TOKEN|-] [--note TEXT] [--failed]

atask log [--done TEXT] [--next TEXT] [--learned TEXT] [--note TEXT]

atask verify proof
atask verify regression
atask verify complete [TASK_ID] [--reverify]

atask init [--name NAME] [--path PATH]
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
--proof "PRESET: SUBJECT [k=v]"        proves this task works, 30 s
--regression "PRESET: SUBJECT [k=v]"   proves nothing else broke, --size budget
--size small|medium|large              60/300/900 s, regression only
--on-complete "PRESET: SUBJECT [k=v]"  run after completion, waited, 900 s
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
--project NAME|ID      default: general
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

A preset's default size applies to `--regression` when `--size` is absent;
`--proof` is always 30 s. Rejected at add: `run: true` and other no-ops, a
small `go_test` on a whole tree with no `name`, and `run` arguments that embed
encoded code. A `*_test` that runs zero tests fails at verify; exit 0 alone
never passes a `*_test` preset.

### Run result

Every proof, regression, and action run must finish inside its budget. A run
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
  "output": "last 64 KiB of combined stdout and stderr"
}
```

| Flag                   | Budget                 |
| ---------------------- | ---------------------- |
| `--proof`              | 30 s                   |
| `--regression`         | `--size`: 60/300/900 s |
| `--on-complete`        | 900 s                  |
| `--on-complete-detach` | none, not waited on    |

### Output and errors

Every command prints `{"ok":true,"result":...}` or
`{"ok":false,"error":{"code":CODE,"message":TEXT}}` under `--json`. Codes:
`INVALID_INPUT`, `NOT_FOUND`, `CONFLICT`, `INVALID_SESSION`, `NOT_READY`,
`BASE_CONFLICT`, `CHECK_FAILED`, `IDEMPOTENCY_CONFLICT`, `DONE` (claim --wait
with an empty board), `STALLED` (claim --wait with only blocked tasks).

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
    Requires    []TaskID
    Proof       ProofSpec   // proves this task works, 30 s
    Regression  ProofSpec   // proves nothing else broke, sized
    OnComplete  []ActionSpec
}

type ProofSpec struct {
    Preset  string            // "run", "ci", "go_test", ...
    Subject string
    Options map[string]string
    Size    Size              // regression only; zero value = the preset default
}

type ActionSpec struct {
    Preset  string
    Subject string
    Options map[string]string
    Detach  bool
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
    SizeSmall  Size = "small"   // 60 s
    SizeMedium Size = "medium"  // 300 s
    SizeLarge  Size = "large"   // 900 s
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
  action results. `List` without `--project` shows every project's non-archived tasks with the project name on each row (the board); `--project NAME` scopes to one project. `general` is the project for tasks filed outside a registered repo. The list is board-wide by default.
- `Claim`: atomic eligibility and exclusive leased assignment; isolated
  worktree for Git tasks. The token is discoverable from the task worktree;
  elsewhere it is passed explicitly or via `ATASK_SESSION`.
- `Renew` / `Release` / `Log`: session-fenced. `Release` with `Failed` counts
  an attempt; without it the attempt is not counted.
- `VerifyProof` / `VerifyRegression`: fresh diagnostic run, no completion.
- `VerifyComplete`: fresh proof, fresh regression, then atomically complete the
  task and record its `on_complete` actions as pending. atask then runs each
  action: waited actions record a `RunResult`; detached actions are spawned
  and recorded as `detached`. On a Git-backed task whose worktree holds no
  commit beyond its base it returns `NOT_READY` ("no commits since claim;
  commit the work first (or release the claim)") before any check runs and
  counts nothing against the attempt budget.
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
  registered repo. `init` registers a repo. `project` sets target branch and
  attempt budget only; a project holds no checks. Agents do not run it.
- Agent execution, worker pools, planning loops, and model calls stay outside
  atask.
