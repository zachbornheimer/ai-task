# ADR 0004: Random base32 IDs, not UUIDs or content hashes

**Status:** accepted

**Decision.** `task-` + 16 characters from Crockford's lower-case base32
alphabet (80 random bits); `proj-` likewise; `sess-` + 32 characters (160
bits). Collisions are detected by UNIQUE constraints and retried. IDs are never
derived from descriptions and never reused.

**Rationale.** Content hashes change when a description is edited; UUIDs are
36 characters of mixed case that agents and humans mistype; 80 bits is ample
for a per-user store and the alphabet avoids `i/l/o/u` ambiguity.
