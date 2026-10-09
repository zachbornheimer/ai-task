# ADR 0009: Revision-bound evidence, not trusted exit codes

**Status:** accepted

**Decision.** A verification run records the policy digest, check ID/version,
the immutable revision (Milestone 3 captures it), timestamps, exit code,
outcome, bounded output, and whether it was reused. Reuse requires identical
policy digest, check version, and revision. A zero exit code on an unknown
revision is not evidence.

**Rationale.** "Tests passed" on a mutable worktree says nothing about what is
being promoted; binding evidence to inputs is what makes completion mean
something downstream.
