<!-- atask:begin -->

**Track verifiable objectives with `atask`.** An objective is work with a specific outcome and meaningful completion criteria. Run `atask context` to learn the workflow. Ensure an objective has a task before doing or delegating its work. The worker claims the task, follows its handoff (`atask context ID`), and requests verification with `atask verify complete`. The engine determines whether completion requirements are satisfied.

<!-- atask:end -->

## Engineering guidelines

Edit this section for the repository; the block above is owned by `atask init`.

- Prefer the smallest complete change that satisfies the task; follow the
  existing architecture and conventions; avoid unrelated refactoring,
  formatting, or dependency changes.
- Handle errors explicitly; preserve existing behaviour and public
  contracts unless the task requires otherwise.
- Test observable behaviour, including failure paths, with deterministic
  tests; fix defects rather than weakening assertions.
- Read the code and documentation the task needs, not the repository by
  default; keep documentation consistent when a documented contract changes.
- Report results and blockers concisely; distinguish verified results from
  assumptions.

## This repository

`atask` is developed here; the block above is what `atask init` installs into
any repository. Planning verbs (`atask add`, `atask update`, `atask show`,
`atask list`) are the planner's; `docs/agent-contract.md` is the full
reference, `docs/integrations.md` says how each harness is wired, and
`examples/embedded_runner` shows the host loop. Run `go test ./...`
before `atask verify complete`; `gofmt` and `go vet` are regression checks.
