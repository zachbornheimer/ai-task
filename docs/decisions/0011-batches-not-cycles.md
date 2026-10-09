# ADR 0011: Verification batches, not dependency cycles

**Status:** accepted (implementation in Milestone 4)

**Decision.** Tasks that must be verified together form an explicit
integration candidate: a combined revision that is the verification target.
The dependency graph stays a DAG; the batch is a separate record.

**Rationale.** A cycle says "neither can start", which is false for two
independently implementable changes that only compile together. If A truly
cannot be implemented before B and vice versa, that is a design problem a batch
cannot fix; the documentation says so.
