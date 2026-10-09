# ADR 0015: Coupled verification as durable cohort jobs

**Status:** accepted (refines ADR 0011)

**Decision.** A task's `cohort` names peers whose completion proof is
evaluated together. Members are claimed and implemented independently; a
member's `verify complete` records its immutable submission and ends its
claim. A verifier job, owned by a fresh leased token, assembles one
candidate from all member revisions, runs every member's task checks and
the project regression suite on it, promotes, and finalises all members
atomically. Any waiting worker runs or recovers pending jobs.

**Rationale.** Frontend and backend halves of a feature can be built in
parallel but only prove the feature together. Hard-edge cycles are invalid;
a cohort records the coupling without pretending either half must start
first. Verifier authority must outlive the agents: a consumed session
token cannot be the credential that completes peers.

**Rejected.** Group-level verification; inferring cohorts from failing
tests; a daemon to run jobs.
