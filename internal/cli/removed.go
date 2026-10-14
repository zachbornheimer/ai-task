package cli

import "regexp"

// anyCommand keys the removed flags every command used to accept.
const anyCommand = "*"

const (
	sessionHint     = "the session token comes from the task worktree, ATASK_SESSION, or a positional TOKEN on `claim renew` and `claim release`"
	checkHint       = "give the task `--proof \"PRESET: SUBJECT\"` and `--regression \"PRESET: SUBJECT\"`"
	cohortHint      = "cohorts were removed; use --group"
	projectHoldsNo  = "a project holds no checks; give each task its own with `atask add --regression`"
	projectBudgets  = "budgets come from the proof size; set it on the task's --regression"
	initWritesBlock = "`atask init` installs only the AGENTS.md block and the CLAUDE.md import"
)

// removedVerbs maps a verb outside docs/contract.md to its contract alternative.
var removedVerbs = map[string]string{
	"hook": "`atask context` prints the operating contract; a host runs it from its own session start",
}

// removedFlags maps command -> flag -> the contract alternative, for every
// flag docs/contract.md does not list. A removed flag fails as INVALID_INPUT
// naming what to use instead.
var removedFlags = map[string]map[string]string{
	anyCommand: {
		"output": "use --json or ATASK_OUTPUT=json",
		"pretty": "output is one compact line under --json; pipe it through jq to indent",
	},
	"init": {
		"claude":          initWritesBlock,
		"no-instructions": initWritesBlock,
	},
	"add": {
		"check":           checkHint,
		"optional-check":  checkHint,
		"cohort":          cohortHint,
		"pin":             "state a limit as a `--constraint`",
		"plan":            "use -f PLAN (yaml or json; - for stdin)",
		"policy-json":     checkHint,
		"expect-rev":      "not a CLI flag; set ChangeSet.ExpectedPlanRev through the Go API",
		"idempotency-key": "not a CLI flag; set ChangeSet.IdempotencyKey through the Go API; adding an open title again returns the existing task",
		"session":         sessionHint,
		"claim":           "add the task, then `atask claim TASK_ID`",
	},
	"update": {
		"check":           checkHint,
		"optional-check":  checkHint,
		"cohort":          cohortHint,
		"pin":             "state a limit as a `--constraint`",
		"set-requires":    "use --requires to add and --remove-requires to remove",
		"session":         sessionHint,
		"expect-rev":      "not a CLI flag; set ChangeSet.ExpectedPlanRev through the Go API",
		"idempotency-key": "not a CLI flag; set ChangeSet.IdempotencyKey through the Go API",
		"title":           "a task's title is fixed; `--archive --reason TEXT` it and add the new one",
	},
	"claim":   {"lease": "the lease is 30 minutes; extend it with `atask claim renew`", "session": sessionHint},
	"log":     {"session": sessionHint},
	"verify":  {"session": sessionHint},
	"project": projectFlagHints(),
}

func projectFlagHints() map[string]string {
	return map[string]string{
		"name":              "`atask project` sets only --target-branch and --attempts; register a repo with `atask init --name`",
		"retry-cooldown":    "`atask project` sets only --target-branch and --attempts",
		"regression-check":  projectHoldsNo,
		"regression-json":   projectHoldsNo,
		"regression-file":   projectHoldsNo,
		"regression-budget": projectBudgets,
		"budget-small":      projectBudgets,
		"budget-medium":     projectBudgets,
		"budget-large":      projectBudgets,
		"integration":       "completion then `--on-complete` actions is the only path; see docs/migration.md to move a promote project to an on_complete action",
	}
}

var undefinedFlag = regexp.MustCompile(`flag provided but not defined: -{1,2}([A-Za-z0-9-]+)`)

// removedFlagError turns the flag package's "not defined" error for a removed
// flag into a usage error that names the alternative; any other parse error
// passes through unchanged.
func removedFlagError(command string, err error) error {
	m := undefinedFlag.FindStringSubmatch(err.Error())
	if m == nil {
		return &usageErr{err}
	}
	name := m[1]
	hint, ok := removedFlags[command][name]
	if !ok {
		hint, ok = removedFlags[anyCommand][name]
	}
	if !ok {
		return &usageErr{err}
	}
	return usage("--%s was removed: %s", name, hint)
}

// removedVerbError answers an unknown command, naming the contract
// alternative when the verb was removed.
func removedVerbError(name string) error {
	if hint, ok := removedVerbs[name]; ok {
		return usage("%q was removed: %s", name, hint)
	}
	return usage("unknown command %q", name)
}

// removedVerifyMode answers `verify groups`.
func removedVerifyMode(mode string) error {
	if mode == "groups" {
		return usage("`verify groups` was removed: use `atask verify complete GROUP`")
	}
	return nil
}
