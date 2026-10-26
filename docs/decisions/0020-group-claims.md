# ADR 0020: Group-scoped claims

**Status:** accepted

**Decision.** `atask claim --group REF [--wait]` (`ClaimRequest.GroupID`) claims
the oldest claimable task among the live descendants of a group, at any depth.
Children share their group's project, so the group's project is the project of
every candidate. REF is an id or key of a group;
a non-group, or `--group` beside a task reference, is `INVALID_INPUT`. The
candidate query is the project query plus `t.id IN <descendants>`: one
eligibility rule, one recursive CTE over `parent_id`.

DONE, STALLED, and NOT_READY are answered for the group's subtree with the same
codes as `claim --wait`, with or without `--wait`:

- `DONE`: every descendant is complete and every group proof in the subtree
  (the group's own included) passed. A group with a required proof and no
  executable task beneath it can never run that proof, so it is never DONE.
- `STALLED`: open members remain, or a proof group in the subtree has no
  executable task beneath it (the reason names that group), and nothing beneath
  the group is claimable, in flight, or cooling. A member required by an
  incomplete task outside the group adds a reason naming that task: a group run
  does not pull in prerequisites beyond its group.
- `NOT_READY` with details `{group, group_proofs_pending, active}`.
  `group_proofs_pending` is true when a group in the subtree has every member
  complete and no verifier running, including while its proof waits out a retry
  backoff: the host runs `atask verify complete GROUP`, which also verifies
  the ready proof groups nested in GROUP, deepest first, before GROUP itself.

`--wait` runs pending group proofs only inside the subtree (never forced, so a
failed group proof is not repeated until its inputs change) and then waits
like a project claim. Worktree bases are unchanged: stacked bases already merge
each task's completed requirements.

**Rationale.** A host that files a DAG under one group can drive it with the
loop it already has for a project (`claim`, work, `verify complete`, until
DONE or STALLED) and land the group once. Nothing runs inside atask; the
group is only a selection scope.

**Consequences.** The group summary reuses the project summary over the
subtree, so both stay in step. The SQL attempt limit of the group's project
applies to every candidate. No migration.
