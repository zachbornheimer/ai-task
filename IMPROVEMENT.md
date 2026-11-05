# Improvement: a check that needs a host credential is runnable or impossible to file

## Intent

Two tasks could never complete because their proof named a command the check
sandbox always refuses: `at-mrvft3` (`ssh -J mini ...`) and `at-wxe778`
(`mise run e2b:up`, which does an `op read`).

## Observations

- ssh: `Operation not permitted` reading `~/.ssh/known_hosts` / the identity.
  The cause is a **read** denial (`unreadableUnderHome` in
  `internal/app/sandbox.go`), not a missing `extra_write` grant. No config
  setting re-allows a read.
- op: with `OP_SERVICE_ACCOUNT_TOKEN` already passed through `extra_env`,
  `op` still failed at once: `cannot create directory ~/.config/op ... file
  exists` (`~/.config/op` is denied). With `OP_CONFIG_DIR` pointing at a
  writable 0700 directory the same token reads the item through the sandbox.
  `e2b.py` retries three times with 5 s sleeps, which is the 15.7 s failure.

## Decision

| Need | Choice                                                                                                                                                   | Cost                                                                      |
| ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| ssh  | refuse at filing, naming `~/.ssh` (`proof.SandboxRefusal`)                                                                                               | a proof cannot log in; prove from a local checkout or archive             |
| op   | run: a check handed the token also gets a private `OP_CONFIG_DIR` in its TMPDIR (`opConfigEnv`); refuse at filing only while `extra_env` lacks the token | none beyond the grant the owner already made; `~/.config/op` stays denied |

Neither widens the sandbox. Refusal applies only to checks that run confined
(not rootless projects, not `mode = "none"`).

## Owner decision left open

Whether a check may ever see `~/.ssh`: (1) keep denying (current; ssh proofs
are refused), (2) add an `extra_read` knob (a check can read every key in the
granted path, so a worker-authored proof can exfiltrate it), (3) `mode =
"none"` (unconfines every check). No setting was changed.

## Proof

`go test ./...` green. New tests are red against the neutralised behaviour and
green with it: `internal/proof/sandbox_refusal_test.go`,
`internal/app/sandbox_refusal_test.go`, `internal/app/checkenv_op_test.go`.

## Not proven

- The installed `atask` is a release build; this change is not deployed.
- Indirect callers (`mise run`, scripts) are not refused; they fail at run time.
