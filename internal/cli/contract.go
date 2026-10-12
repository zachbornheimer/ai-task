package cli

import "strings"

// AgentContract is the operating contract `atask context` prints. It is
// static: no database, project, or session is required. Keep it between
// 300 and 500 words so an agent can ingest it once.
const AgentContract = `atask tracks verifiable objectives. File one when the outcome is specific and checkable: a software fix, research, a report, infrastructure, or an operational task. Do not file conversations, tool calls, or implementation steps. Track outcomes, not activity.

atask context with no arguments is this contract. It needs no environment, project, database, or claim. atask context ID is the worker handoff: objective, checks, blockers, workspace, and how completion is decided. A complete task reports done and issues no work. atask help is command syntax. atask version prints the build.

Discover work with atask list. The default is every non-archived task, including completed ones. Filters: ready, blocked, complete, archived, open. Pass a project name to scope one enrolled repo. atask show with no argument is the current live claim. atask show ID is any task. Outside a live claim, show needs an ID.

File with atask add "title" --check "id: cmd". Every task needs a check. The check is not run at add. A second add of the same open title returns the existing id. Do not invent a trivial check. If the outcome cannot be verified automatically, say so. atask update ID changes plan fields. atask update ID --archive --reason "..." removes a task from active planning.

atask claim with no id takes the oldest claimable task. atask claim ID takes that one. The claim prints a session token and a workspace. Inside a git worktree the token is stored. Outside it, pass --session, ATASK_SESSION, or the token on log, verify, claim renew, and claim release. An expired claim cannot complete a task.

Work only on the claimed task. B --requires A means B cannot be claimed until A is complete. Cycles and cross-project edges are rejected. To delegate, file the objective, then have a worker claim it and follow atask context ID. The host launches, heartbeats, cancels, and recovers workers.

Record progress with atask log --done "..." --next "..." --learned "..." --note "...". Next is what the following attempt reads first. Log requires a live session.

atask verify task and atask verify regression are diagnostics. atask verify complete is the only completion path. On a git claim, commit first; checks run on that revision. On an unscoped claim, checks run in the live working directory and prove the command passed, not the factual quality of research or writing. The engine decides. Do not assume complete from a unit test, a log line, or your own summary.

If verification fails, fix the cause, log, and verify again. A timeout is not a strike: narrow the check with -run, or set --size medium|large. If the lease expires, atask claim renew with the token, or atask claim ID. To hand work back: atask log --next "..." then atask claim release --note "...".
`

func agentContractWordCount() int {
	return len(strings.Fields(AgentContract))
}
