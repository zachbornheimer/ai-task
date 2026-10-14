package workspace

import (
	"crypto/rand"
	"math/big"
	"time"
)

// backoffWait is base plus a random extra of up to base, so concurrent
// retriers spread out instead of colliding again. Falls back to base alone
// if the system randomness source fails.
func backoffWait(base time.Duration) time.Duration {
	extra, err := rand.Int(rand.Reader, big.NewInt(int64(base)))
	if err != nil {
		return base
	}
	return base + time.Duration(extra.Int64())
}
