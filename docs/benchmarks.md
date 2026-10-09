# Benchmarks

Baseline measurements for the implemented operations. Numbers are from one
machine and one run; they bound the order of magnitude, not a guarantee.

**Environment.** Linux 6.18 (x86-64), 4 vCPU Intel Xeon @ 2.80 GHz, 15 GiB
RAM, Go 1.24.7, `modernc.org/sqlite` v1.46.1 (pure Go), database on local
disk in WAL mode, `go test -bench . -benchtime 2s -benchmem`. The CLI numbers
spawn the real binary with `os/exec`.

**Methodology.** `internal/app/bench_test.go` builds a layered DAG (layers of
10, each task requiring all 10 tasks of the previous layer: 5 000 tasks ⇒
~50 000 edges) because that shape makes availability and cycle queries do
real work. Each benchmark opens a fresh file-backed engine. Contended
benchmarks use `b.RunParallel` on 4 procs. Reproduce with:

```sh
go test ./internal/app -run XXX -bench . -benchtime 2s -benchmem
go test ./test/e2e -run XXX -bench .
```

## Results

| Operation | Size | Latency | Notes |
|---|---|---|---|
| `Available` (SQL pre-filter + derive) | 100 tasks | 0.56 ms | 10 available |
| | 1 000 tasks | 2.8 ms | one index scan of open tasks |
| | 5 000 tasks | 17.5 ms | linear in open tasks |
| `List` all with status | 1 000 | 22 ms | full record per row |
| | 5 000 | 131 ms | see "known costs" |
| `Show` (task + 10 requires + 10 dependents + handoff) | 1 000 | 3.1 ms | ~22 index-backed queries |
| `Graph` + topological order | 1 000 / 5 000 | 49 ms / 278 ms | 10k / 50k edge rows |
| `Take` (automatic selection) | ~1 600 open, all older leased | 6.0 ms | adversarial: every older task is leased and must be skipped |
| `Take` contended, one task, 4 procs | | 0.15 ms / attempt | losers fail fast with `TASK_ALREADY_TAKEN` |
| `Log`, 4 procs, distinct tasks | | 0.11 ms / entry | ~9 000 entries/s |
| `AddDependency` cycle check, chain walk | 100 / 1 000 / 5 000 | 0.7 / 3.2 / 16.6 ms | worst case: the walk visits the whole chain |
| `AddDependencies` bulk, 1 900 edges, one tx | | 0.92 s | measured before the light task lookup (see below) |
| `Add` | | 0.14 ms | |
| CLI `tasks version` (process spawn, no DB) | | 4.5 ms | |
| CLI `tasks list` (spawn + open + migrate check + query) | | 6.6 ms | |

## Findings that changed the code

- **Cycle-check direction.** The first implementation walked the
  *ancestors* of the prerequisite. Building a dense DAG in topological
  order made every insert visit the whole upstream graph (the 5 000-task
  fixture had not finished building after several minutes and was stopped). Walking the *dependents* of the task
  being edited is equivalent and empty for a freshly created task; the same
  fixture now builds in seconds. The chain benchmark above is the remaining
  worst case and is linear.
- **Candidate selection.** Probing the three priority groups separately was
  measured at 8.3 ms vs 6.0 ms for one sorted query, because each probe
  re-scans the leased rows. The single query stays.
- **Bulk edges.** Validation loaded two full task records per edge
  (~150 µs each) only to compare project IDs; a light lookup replaced it.

## Known costs and their paths

- `List` and `Graph` are linear with a ~25 µs/row constant dominated by the
  record query (four LEFT JOINs, two correlated subqueries) and JSON decode
  of constraints/policy per row. A projection without policy/constraints
  for list rows would cut this several-fold; not done because 1 000 tasks
  answer in 22 ms today.
- `Take` is linear in *leased* open tasks older than the first available
  one. Realistic projects have a handful of leases; the adversarial fixture
  leases everything.
- Per-query overhead of the pure-Go driver is ~100–150 µs. A CGO driver
  would roughly halve it; see ADR 0002 for why we accept this.
- Verification is dominated by the checks themselves; the engine's own
  overhead per check is three short transactions (reuse lookup, evidence
  insert, verdict) of ~0.3 ms total.
