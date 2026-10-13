package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// TestWriteCancelledByContextIsNotInternal: when the caller's context
// ends during a write, database/sql rolls the transaction back on its own;
// the store must report the cancellation (so a waiting claim's deadline
// surfaces as context.DeadlineExceeded), never an internal "commit
// transaction: already committed or rolled back".
func TestWriteCancelledByContextIsNotInternal(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "at.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		name string
		make func() (context.Context, context.CancelFunc)
		want error
	}{
		{"cancel", func() (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }, context.Canceled},
		{"deadline", func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 20*time.Millisecond) }, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wctx, cancel := tc.make()
			defer cancel()
			err := s.Write(wctx, func(tx *sqlite.Tx) error {
				if tc.name == "cancel" {
					cancel()
				}
				// Give database/sql time to roll the transaction back.
				time.Sleep(100 * time.Millisecond)
				return nil
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want an error wrapping %v", err, tc.want)
			}
			if fault.IsInternal(err) {
				t.Fatalf("a canceled write must not be internal: %v", err)
			}
		})
	}
}
