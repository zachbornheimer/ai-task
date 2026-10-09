# ADR 0002: SQLite via a pure-Go driver, not Beads/Dolt/JSONL

**Status:** accepted

**Context.** We need atomic multi-process claims, transactional history,
concurrent reads, and no daemon. Beads uses Dolt; Beads Rust mirrors SQLite to
JSONL; Taskmaster uses a JSON file.

**Decision.** One SQLite file in WAL mode with `BEGIN IMMEDIATE` writers and a
10 s busy timeout, through `modernc.org/sqlite` (CGO-free).

**Driver tradeoff.** `mattn/go-sqlite3` (CGO) is the reference binding: fastest,
widest extension support, but needs a C toolchain per target and complicates
cross-compilation for agents on macOS/Windows/Linux. `modernc.org/sqlite` is a
transpiled SQLite: pure Go, cross-compiles trivially, measurably slower on
heavy scans but our queries are small and index-backed (see
`docs/benchmarks.md`). It also honours `_txlock=immediate` for write
transactions while leaving read-only transactions deferred, which is exactly
the locking shape we need. Switching drivers later touches one import and the
DSN builder.

**Rejected.** Dolt (a database server and sync protocol for a local task
list); a JSONL mirror (two authoritative-looking representations and a
reconciliation protocol); a JSON file (no atomic claims across processes).

**Consequences.** No multi-machine sync in V1; WAL on network filesystems is
unsupported.
