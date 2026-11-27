package workspace

import "encoding/hex"

// RuntimeIdentity binds ambient rootless authority to one registered runtime.
// CapabilityDigest is SHA-256 of the private capability, never the capability.
type RuntimeIdentity struct {
	Session          string `json:"session"`
	PID              int    `json:"pid"`
	Incarnation      string `json:"incarnation"`
	CapabilityDigest string `json:"capability_digest"`
}

// Valid rejects incomplete bindings rather than treating them as unbound.
func (r RuntimeIdentity) Valid() bool {
	incarnation, err := hex.DecodeString(r.Incarnation)
	if err != nil || len(incarnation) != 32 {
		return false
	}
	digest, err := hex.DecodeString(r.CapabilityDigest)
	return r.Session != "" && r.PID > 0 && err == nil && len(digest) == 32
}

// Allows preserves legacy discovery only for an unbound caller. A runtime
// never adopts an unbound claim; every identity component must match.
func (r *RuntimeIdentity) Allows(owner *RuntimeIdentity) bool {
	if owner == nil {
		return r == nil
	}
	return r != nil && r.Valid() && owner.Valid() && *r == *owner
}
