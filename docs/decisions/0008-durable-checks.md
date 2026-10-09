# ADR 0008: Durable check specifications, not Go callbacks

**Status:** accepted

**Decision.** A check is an argv array, working directory, timeout (stored as
`timeout_ms`), and `required` flag, serialised as canonical JSON and
identified by digest. Embedding applications that need Go-level verifiers
must register them by name and version (Milestone 2); a missing registration
fails explicitly.

**Rationale.** Function values cannot be stored or run by another process; a
CLI invoked from four different harnesses must be able to execute a check
defined months earlier.
