# ADR 0018: Durable integration intents; quarantine on takeover; no hook

**Status:** accepted (amends ADR 0012, 0016 and 0017)

**Decision.**

- Promotion is bridged by a durable intent row written in a fenced
  transaction that re-checks session or job ownership, prerequisites,
  contract revision, regression digest, archive state, submission
  identity and stored evidence for the candidate. The target moves only
  after that, under a descriptor-owned advisory lock, and completion is
  recorded in a second transaction authorised by the intent. Reconciliation
  resolves open intents whose owner lease expired by asking Git whether
  the candidate reached the target. Plan edits, blockers, archive and
  claims are refused while an intent is open. Single tasks and cohorts
  use the same protocol.
- A takeover (the previous attempt never ended) quarantines that
  attempt's worktree: moved to `<path>.stale-<seq>`, HEAD detached, token
  left in place; a fresh worktree on the task branch appears at the task
  path with the new token. Worktree-resident tokens stay.
- The post-commit hook is withdrawn; `at init` writes no Git hooks (it
  installs agent instruction files, which carry no authority).
- Every category needs a required check; optional checks are
  informational; a policy with no required check never passes.

**Rationale.** Git and SQLite cannot share a transaction, so the
in-between state must be representable and recoverable rather than
assumed away; a pre-promotion re-check alone leaves a window after the
check and no answer for a process killed after the ref moved. The
quarantine closes the stale-process hazard the worktree token raised
without giving up the agent-first ergonomics: the stale process keeps
only its own expired token and a directory nothing else reads. The hook
depended on PATH, a global `core.hooksPath` and files in the tree; every
authenticated command renews the lease instead.

**Consequences.** One more table and one more status transition
(`awaiting_integration` is now a real durable state). A crash after the
ref moved leaves the task awaiting integration until the owner's lease
expires (at most the lease length), then completes. Quarantined
directories accumulate until pruned.
