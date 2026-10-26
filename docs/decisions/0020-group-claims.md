# ADR 0020: Group-scoped claims

**Status:** accepted

**Decision.** `atask claim --group REF [--wait]` (`ClaimRequest.GroupID`) claims
the oldest claimable task among the live descendants of a group, at any depth,
whatever project each member belongs to. REF is an id or key of a group;
a non-group, or `--group` beside a task reference, is `INVALID_INPUT`. The
candidate query is the project query plus `t.id IN <descendants>`: one
eligibility rule, one recursive CTE over `parent_id`.

DONE, STALLED, and NOT_READY are answered for the group's subtree with the same
codes as `claim --wait`, with or without `--wait`:

- `DONE`: every descendant is complete and every group proof in the subtree
  (the group's own included) passed.
- `STALLED`: open members remain and nothing beneath the group is claimable,
  in flight, or cooling. A member required by an incomplete task outside the
  group adds a reason naming that task: a group run does not pull in
  prerequisites beyond its group.
- `NOT_READY` with details `{group, group_proofs_pending, active}`.
  `group_proofs_pending` is true when a group in the subtree has every member
  complete and no verifier running: the host runs `atask verify complete GROUP`.

`--wait` runs pending group proofs only inside the subtree (never forced, so a
failed group proof is not repeated until its inputs change) and then waits
like a project claim. Worktree bases are unchanged: stacked bases already merge
each task's completed requirements.

**Rationale.** A host that files a DAG under one group can drive it with the
loop it already has for a project (`claim`, work, `verify complete`, until
DONE or STALLED) and land the group once. Nothing runs inside atask; the
group is only a selection scope.

**Consequences.** The group summary reuses the project summary over the
subtree, so both stay in step. The SQL attempt limit of the group's home
project applies to every candidate; members in other projects with a smaller
limit are still re-checked by the domain rule at claim time. No migration.
