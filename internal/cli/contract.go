package cli

import "strings"

// AgentContract is the operating contract `atask context` prints. It is
// static: no database, project, or session is required. Keep it between
// 300 and 1000 words so an agent can ingest it once.
const AgentContract = `atask tracks verifiable objectives. An objective belongs here when it has a specific outcome and meaningful criteria for whether it was achieved: fixing software, researching a competitor, writing a report, changing infrastructure, finishing an operational task. Do not create a task for every conversation, tool call, or implementation step. Track outcomes, not activity.

atask context with no arguments is this contract. atask context ID is the worker handoff for that task: the objective, acceptance criteria, checks, blockers, worktree, and how completion is decided. If the task is already complete, the handoff reports that and issues no new work. atask help is the full command syntax. atask version prints the build.

Discover work with atask list. The default list is every non-archived task, including completed ones. Filters: ready, blocked, complete, archived, open. Global --project NAME scopes one repo. atask show with no argument is the current claim when a worktree or session can resolve one. atask show ID is any task. Outside a resolvable claim, show needs an ID.

File an objective with atask add "title" --proof "run: cmd" --regression "run: cmd". A task needs both: --proof shows the task works, --regression shows nothing else broke. Missing one makes it a draft: never claimable; atask update ID with both proofs makes it ready. A second add of the same open title returns the existing id. atask update ID changes plan fields. atask update ID --archive --reason "..." removes a task from active planning.

atask claim with no id takes the oldest claimable task. atask claim ID takes that one. The claim prints a session token and a workspace. The token is claim JSON field result.token. Present it by running in the task worktree or the directory you claimed from (stored there), ATASK_SESSION, or one positional TOKEN (- reads stdin) on log, verify, claim renew, claim release; log and verify take no task id, so quote flag values. An expired or unauthorized claim cannot complete a task.

Work only on the claimed task. Hard dependencies: B --requires A means B cannot be claimed until A is complete. Cycles and edges between tasks sharing no project are rejected. A task may name several projects with repeated --project; the first is its home. To delegate, ensure the objective has a task, then have a worker claim it and follow atask context ID. The host launches, heartbeats, cancels, and recovers workers.

Record progress with atask log --done "..." --next "..." --learned "..." --note "...". Next is what the following attempt reads first. Log requires a live session.

atask verify proof and atask verify regression are diagnostics. atask verify complete is the only completion path: commit first, then required checks run fresh. The engine decides. Completion is recorded on the task branch; verify complete does not merge or fast-forward the target branch, landing is the host's step (wt merge). Do not assume complete from a green unit test, a log line, or your own summary.

If verification fails, fix the cause, log, and verify again. A timeout is not a strike: narrow the check with -run, or set size=medium|large on the check. If the lease expires, atask claim renew with the token, or atask claim ID. To hand work back: atask log --next "..." then atask claim release --note "...".
`

func agentContractWordCount() int {
	return len(strings.Fields(AgentContract))
}
