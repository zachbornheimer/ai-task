# ADR 0012: Single-task completion vs integrated completion

**Status:** accepted

**Decision.** Projects declare an integration policy. `none`: a verified
submission completes the task. `promote` (Milestone 3): a verified revision
must be promoted into the target branch first; until then the task is
`awaiting_integration` and does not release dependents.

**Rationale.** A passing isolated branch must not unblock a downstream task
whose fresh worktree lacks the change. Making the policy explicit per project
keeps non-Git projects simple and Git projects honest.
