// Package must holds test helpers that turn an ignored error or an unchecked
// type assertion into a visible test failure.
package must

import (
	"fmt"
	"testing"
)

// OK fails the test immediately when err is non-nil.
func OK(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Value returns v and fails the test immediately when err is non-nil.
func Value[T any](t testing.TB, v T, err error) T {
	t.Helper()
	OK(t, err)
	return v
}

// As asserts that v holds a T and returns it. It panics with the actual
// type otherwise, which the test runner reports as a failure of that test.
func As[T any](v any) T {
	x, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf("must.As: got %T (%v), want %T", v, v, x))
	}
	return x
}
