# ADR 0014: Every verification is fresh; no evidence reuse

**Status:** accepted (supersedes the reuse rule in ADR 0009)

**Decision.** `verify task`, `verify regression` and each new `verify
complete` execute their checks. The evidence table is history, not a
cache. Checks run with `GOFLAGS=-count=1` and in a detached snapshot.

**Rationale.** Reuse keyed on (check digest, revision) was correct in
theory but moved the proof of completion away from the moment of
completion; environment drift, shared services, and anything a check reads
outside the repository are invisible to that key. The cost (re-running
checks) is the cost of knowing.

**Consequences.** Cohort verification runs the regression suite once per
candidate and records it under every member; a replay of an
already-committed completion returns the stored result without a run.
