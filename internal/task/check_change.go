package task

import "time"

// CheckField names which part of a task's checks a change touched.
type CheckField string

const (
	CheckFieldProof            CheckField = "proof"
	CheckFieldRegression       CheckField = "regression"
	CheckFieldOnComplete       CheckField = "on_complete"
	CheckFieldOnCompleteDetach CheckField = "on_complete_detach"
)

// CheckActorPlanner is the actor of a change made without a session token.
const CheckActorPlanner = "planner"

// CheckChangedGlyph marks a listed task whose checks changed after it was
// first claimed.
const CheckChangedGlyph = "△"

// CheckChange is one recorded change to a claimed task's checks.
type CheckChange struct {
	ChangedAt time.Time  `json:"changed_at"`
	Field     CheckField `json:"field"`
	OldSpec   string     `json:"old_spec"`
	NewSpec   string     `json:"new_spec"`
	// Actor is "attempt:<id>" for a session token, else "planner".
	Actor string `json:"actor"`
	Cwd   string `json:"cwd"`
}
