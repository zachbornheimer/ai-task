# Benchmarks

Baseline measurements for the `at` engine. One machine, one run; they
bound the order of magnitude, not a guarantee.

**Environment.** Linux 6.18 (x86-64), 4 vCPU Intel Xeon @ 2.80 GHz, Go
1.24.7, `modernc.org/sqlite` v1.46.1 (pure Go), database on local disk in
WAL mode, `go test ./internal/app -run XXX -bench . -benchtime 1s`.

**Methodology.** `internal/app/bench_test.go`. The layered plan has layers
of 10 tasks, each requiring every task of the previous layer, applied in
200-operation `Apply` batches. Each benchmark opens a fresh file-backed
engine.

| Operation | Size | Latency | Notes |
|---|---|---|---|
| `Apply` of 200 new tasks (one transaction) | | 12.5 ms | ~60 µs per task incl. key checks |
| `List(ready)` incl. summary and reconcile | 100 tasks (10 ready) | 6.5 ms | light list rows: group + prerequisites of blocked tasks only |
| | 1 000 tasks (10 ready) | 63 ms | dominated by the summary's scan of live tasks (~60 µs/row) |
| `Claim` (automatic, no edges, project without Git) | | 2.2 ms | includes reconcile, candidate query, generation advance, handoff read |
| CLI process (`at version` / `at list`) | | ~4.5 ms / ~7 ms | measured for the earlier engine; unchanged startup path |

## Findings that changed the code

- The first `List` built the full detail view (≈30 index-backed queries)
  for every row. A list row now loads only identity, status, group, and,
  for blocked tasks, its prerequisites: 39 ms → 6.5 ms at 100 tasks.
- Earlier engine findings still apply: the cycle check walks dependents of
  the edited task (empty for new tasks); bulk edge validation uses a light
  task lookup; per-query driver overhead is ~100–150 µs.

## Known costs and their paths

- `Summary` scans every live task to compute done/stalled and cohort
  readiness. At thousands of tasks a waiting claim pays this on every
  wake (bounded by the poll interval). A counts-only SQL aggregate is the
  next step if it matters.
- A Git claim adds one `git worktree add` (tens of ms); a `verify
  complete` adds a detached snapshot, the checks themselves, a scratch
  merge, and a promotion. The engine's own overhead there is a handful of
  short transactions.
