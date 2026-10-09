# ADR 0007: Structured append-only log, not comments

**Status:** accepted

**Decision.** `tasks log` records up to four fields with fixed meaning:
`done`, `next`, `learned`, `note`. Entries are append-only, tied to attempt and
task, globally ordered. `take` returns the latest `next`, bounded learnings,
and the last 10 entries; `history` pages everything.

**Rationale.** Free-text comments force the next agent to read a transcript to
find the next step. Typed fields let the engine hand over exactly what matters
without an LLM summariser.
