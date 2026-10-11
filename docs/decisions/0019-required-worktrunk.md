# ADR 0019: Worktrunk required; task worktrees via wt switch

**Status:** accepted (supersedes [ADR-0010](0010-optional-worktrunk.md))

**Decision.** `atask` requires Worktrunk (`wt` on PATH). Live task worktrees
are created and located only with:

```
wt switch [--create --base <base>] --no-cd --no-hooks --yes --format json atask/<id>
```

`--create` is used only when the branch does not exist. The path is whatever
Worktrunk's `worktree-path` template resolves to, including env, per-project,
and user config. `atask` does not copy that template. Detached snapshots and
promotion scratch trees stay as `git worktree add --detach` (Worktrunk's
`wt step relocate` skips detached HEAD). `atask doctor` is unhealthy when
`wt` is missing.

**Rationale.** One layout owner. Relocate, layout-guard, and `atask` then
agree. POSIX `at` is why the CLI is `atask`.

**Consequences.** Hosts without Worktrunk cannot claim. Tests inject a
Switcher facade; e2e uses `wt`. State is `~/.local/state/atask/atask.db`
(XDG_STATE_HOME).
