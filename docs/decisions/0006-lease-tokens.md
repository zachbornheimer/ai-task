# ADR 0006: Session tokens as execution authority, not task IDs

**Status:** accepted

**Decision.** `take` creates an execution attempt with a fencing generation and
returns a secret token once. Only its SHA-256 digest is stored. `log`,
`renew`, and `finish` authorise by token inside the mutating transaction:
attempt is current, not ended, lease unexpired.

**Rationale.** A task ID is public; if it conferred write authority, any stale
or duplicate agent could overwrite a newer attempt's state. Generations make
superseded attempts inert even if their lease would still be valid.

**Consequences.** Agents must keep the token (env var or stdin recommended);
an expired agent re-takes the task and receives its own handoff.
