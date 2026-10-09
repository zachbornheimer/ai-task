# ADR 0005: Derived status, not a mutable status column

**Status:** accepted

**Decision.** No status is stored. `task.Derive` computes it from the
completion fact, unmet dependencies, the current attempt's lease, and the
newest submission's run state, with a fixed precedence. `completed_at` is
written only by the engine in the same transaction as the final gate.

**Rationale.** A settable status lets an agent (or a bug) assert "done"
without evidence and drift from the facts. Derivation also removes an entire
class of transition commands and their validation.

**Consequences.** Listing filters derive status per row; a SQL pre-filter
handles the common `--available` case and is re-checked in Go.
