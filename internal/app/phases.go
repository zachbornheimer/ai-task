package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// PhaseTimings is where one `verify complete` spent its time. Checks
// includes the per-check evidence writes made while they run; Record is
// the final transaction that completes the task; Prune is the cleanup of
// the task's quarantined directories after it.
type PhaseTimings struct {
	Snapshot time.Duration
	Checks   time.Duration
	Cleanup  time.Duration
	Record   time.Duration
	Prune    time.Duration
	Actions  time.Duration
}

// MarshalJSON reports every phase in whole milliseconds.
func (p PhaseTimings) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Snapshot int64 `json:"snapshot_ms"`
		Checks   int64 `json:"checks_ms"`
		Cleanup  int64 `json:"cleanup_ms"`
		Record   int64 `json:"record_ms"`
		Prune    int64 `json:"prune_ms"`
		Actions  int64 `json:"actions_ms"`
	}{p.Snapshot.Milliseconds(), p.Checks.Milliseconds(), p.Cleanup.Milliseconds(), p.Record.Milliseconds(), p.Prune.Milliseconds(), p.Actions.Milliseconds()})
}

// measure starts timing into dst and returns the function that stops it:
// p.measure(&p.Checks), then call the returned stop.
func (p *PhaseTimings) measure(dst *time.Duration) func() {
	start := time.Now()
	return func() { *dst += time.Since(start) }
}

type phasesKey struct{}

// withPhases returns ctx carrying p, so the layers a completion passes
// through report their stage without each growing a parameter.
func withPhases(ctx context.Context, p *PhaseTimings) context.Context {
	return context.WithValue(ctx, phasesKey{}, p)
}

// phasesOf is the PhaseTimings ctx carries; a path nobody is timing gets a
// scratch one, so callers never test for nil.
func phasesOf(ctx context.Context) *PhaseTimings {
	if p, ok := ctx.Value(phasesKey{}).(*PhaseTimings); ok {
		return p
	}
	return &PhaseTimings{}
}

// reportPhases stamps p onto a result and onto the result an error carries
// as its details, so a failed completion explains its time too.
func reportPhases(res *VerifyResult, err error, p *PhaseTimings) {
	if p == nil || res.Replayed {
		return
	}
	stamped := *p
	res.Phases = &stamped
	var fe *fault.Error
	if errors.As(err, &fe) {
		if d, ok := fe.Details.(VerifyResult); ok {
			d.Phases = &stamped
			fe.Details = d
		}
	}
}
