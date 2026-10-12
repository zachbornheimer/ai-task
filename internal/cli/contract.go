package cli

import "strings"

// AgentContract is the operating contract `atask context` prints. It is
// static: no database, project, or session is required. Keep it between
// 300 and 500 words so an agent can ingest it once.
const AgentContract = `atask is the task engine. It owns the plan, claims, leases, progress logs, and completion. You never mark work done. atask verify complete does, after required checks pass on the committed revision.

Start here. atask context is this contract. It needs no project and no database. atask help is the full command reference. atask version prints the build.

Discover work with atask list. The default list is every non-archived task, including completed ones, so the whole plan is visible. Filters: ready (claimable now), blocked, complete, archived, open (not yet complete). Pass a project name to scope one enrolled repo. atask show with no argument is the task this session currently holds. atask show ID is any task. Outside an active claim, show without an id is an error.

File work with atask add "title" --check "id: cmd". Every task needs a check. The check is not run at add time. A second add of the same open title returns the existing id. atask update ID changes plan fields. atask update ID --archive --reason "..." removes a task from active planning. Claimed or completed tasks refuse archive.

atask claim with no id takes the oldest claimable task. atask claim ID takes that one. The claim prints a session token and a workspace. Pass the token as --session, ATASK_SESSION, or a positional argument on log, verify, claim renew, and claim release. Inside the task worktree the token is already stored.

Work only on the claimed task. Hard dependencies: B --requires A means B cannot be claimed until A is complete. Cycles and cross-project edges are rejected.

Record progress with atask log --done "..." --next "..." --learned "..." --note "...". Next is what the following attempt reads first. Log requires a live session.

atask verify task and atask verify regression are diagnostics you may repeat. atask verify complete is the only completion path: commit first, then both suites run fresh. Passing is the proof. Do not assume complete from a green unit test, a log line, or your own summary.

If verification fails, fix the cause, log, and verify again. A timeout is not a strike: narrow the check with -run so it proves only this outcome, or set --size medium|large. If the lease expires, atask claim renew with the token, or atask claim ID to take the task again. To hand work back: atask log --next "..." then atask claim release --note "...".
`

func agentContractWordCount() int {
	return len(strings.Fields(AgentContract))
}
