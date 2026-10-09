# ADR 0001: Go, not Rust

**Status:** accepted

**Context.** The engine is domain logic, SQLite transactions, and external
command execution, packaged as many short-lived CLI processes.

**Decision.** Go 1.24, standard library plus one SQLite driver.

**Rationale.** The workload has no tight memory or latency budget that Rust
would serve; Go's concurrency is sufficient for parallel check execution;
a single static binary is easy to install beside any agent harness; the
team can read the whole codebase quickly.

**Consequences.** We make no claim that Go is faster than Rust. Startup and
query latency are measured (`docs/benchmarks.md`) rather than asserted.
