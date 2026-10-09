# ADR 0013: Stable keys, organizational groups, and atomic Apply

**Status:** accepted

**Decision.** Tasks carry an optional caller-supplied key, unique per
project, usable wherever an ID is. Groups are a task kind that cannot be
claimed, cannot be an edge endpoint, and derives progress from members.
All plan writes go through one `Apply(ChangeSet)` with a closed set of
typed changes, optimistic plan revision, and an idempotency key.

**Rationale.** A planner that splits A into B and C and repoints
dependents makes one logical change; three separate writes could be
interleaved by a claim. Keys make retries and re-imports idempotent and let
proposals reference tasks the model named. Groups give the hierarchy
humans want without inventing a second dependency kind.

**Rejected.** Dotted hierarchical IDs; epic taxonomies; auto-completing
groups; auto-converting membership into edges.
