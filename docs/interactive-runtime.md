# Interactive rootless runtime ownership

Interactive hosts can bind rootless claims to a private runtime identity. This adds token discovery, not a new execution authority: current attempt sequence, lease expiry and terminal-state checks remain mandatory for every renewal/log/verification.

## Maintained host and protocol

Source owner: `zachbornheimer/dev-profile`, `apps/omp/extensions/atask-lease/index.ts`. Its `config/mise.toml` deploy map symlinks the directory to `~/.omp/agent/extensions/atask-lease`, where omp loads `index.ts`. The CLI registration seam is `storeRootlessToken` after the rootless attempt/workspace commit. The host never parses a token from model/tool text.

Lookup precedence is positional/stdin token, `ATASK_SESSION`, private task-worktree token, then runtime-scoped ambient discovery. If `ATASK_RUNTIME_ROOT` is declared but registration is missing/stale, ambient discovery fails closed. A validated runtime never adopts a legacy unbound claim. With no declared runtime, existing unambiguous legacy directory discovery and ambiguity refusal remain unchanged. Malformed or unsafe present registration fails closed at engine opening.

The OS account is the trust boundary: private files and process ancestry prevent accidental cross-session selection and unrelated processes, not malicious code with equivalent same-UID file/process access.

A host creates one private directory per process and exposes its absolute path as `ATASK_RUNTIME_ROOT` before spawning native shells. Each active session has `<sha256(PI_SESSION_ID)>.json` in that directory:

```
{"session":"session id","pid":123,"incarnation":"64 lowercase hex characters","capability":"64 lowercase hex characters"}
```

The host chooses incarnation once per process with a cryptographic random source and capability once per session activation. The directory is owner-only mode 0700 and files owner-only mode 0600. Never print the capability or put it in model-visible command arguments. Native bash already injects `PI_SESSION_ID` for the calling session; the ID alone is insufficient.

The CLI rejects unsafe permissions, symlinks, malformed identities, dead/unrelated owner processes and sessions whose ID does not match the registration. It hashes the capability before passing identity into the engine. Rootless token records bind to that identity. An unrelated session or a new process with the same session ID cannot select the token. Explicit token, private task-worktree and legacy unbound unambiguous-directory discovery retain their existing contracts.

`atask --json claim renew --runtime` renews the runtime's authorized rootless claims without printing tokens. It reports a renewed count, including zero after release/completion. Ordinary `atask log` and `atask verify proof|regression|complete` select one matching runtime claim without positional tokens or an `ATASK_SESSION` prefix. Multiple claims within a runtime remain ambiguous for these ordinary commands. Native subagents and detached owners do not create interactive registrations.

For a validated runtime-bound rootless claim, CLI claim acknowledgements omit the token in JSON and human output. The engine stores the real token privately; explicit/private-worktree/unregistered claim responses remain unchanged.

## Lifecycle

The profile extension `~/.omp/agent/extensions/atask-lease/index.ts` owns interactive timers. Quiet work/long tools do not suspend renewal. Session switch removes the old registration and creates a fresh capability; switching back does not adopt the previous attempt. Shutdown/hibernation that exits the process stop renewal. A reconnect to the same running process retains authority; reconnect after exit gets a new incarnation and cannot revive the old attempt. Owner death stops renewal, and inherited registrations do not authorize orphaned descendants. Engine expiry/supersession fences remain final. A renewal error revokes registration, stops renewal and emits a safe diagnostic, leaving the claim to expire rather than hiding the error.

## Exact verification

From the ai-task checkout:

```
mise run test:runtime
mise run test:lease
mise run test:engine
```

`test:lease` uses the installed profile extension host proof and the real CLI adapter in child processes, injecting one-second leases only in the test binary. Two session owners share a cwd; timer renewal spans at least three lease periods without model renew/log writes, followed by token-free log/verify and unauthorized/released/completed checks. Production leases remain 30 minutes. This is a no-model host integration, not a visual terminal test.

From the maintained dev-profile checkout, `ATASK_ENGINE_SOURCE=/path/to/ai-task mise run test:lease` runs `apps/omp/extensions/atask-lease/host-proof.ts` against this CLI helper. The proof holds the same attempt across more than three one-second leases while a real long-running child tool is in flight. Host publication follows the profile's deploy map; repository commits preserve the complete source even if an ephemeral sandbox is lost.
