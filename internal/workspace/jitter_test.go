package workspace

import (
	"testing"
	"time"
)

func TestBackoffWaitStaysWithinOneToTwoBases(t *testing.T) {
	base := 10 * time.Millisecond
	for range 200 {
		if w := backoffWait(base); w < base || w >= 2*base {
			t.Fatalf("wait %v outside [%v, %v)", w, base, 2*base)
		}
	}
}
