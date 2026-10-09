package cli

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestParseInterleavedAndDoubleDash(t *testing.T) {
	cases := []struct {
		args  []string
		pos   []string
		lease string
	}{
		{[]string{"a", "--lease", "1h", "b"}, []string{"a", "b"}, "1h"},
		{[]string{"--lease", "1h", "a", "b"}, []string{"a", "b"}, "1h"},
		{[]string{"--", "-a", "-b"}, []string{"-a", "-b"}, ""},
		{[]string{"x", "--", "-a", "-b"}, []string{"x", "-a", "-b"}, ""},
		{[]string{"--lease", "2h", "--", "-a", "--lease"}, []string{"-a", "--lease"}, "2h"},
		{[]string{}, nil, ""},
	}
	for _, c := range cases {
		ctx := &ctxt{fs: flag.NewFlagSet("t", flag.ContinueOnError)}
		ctx.fs.SetOutput(io.Discard)
		lease := ctx.fs.String("lease", "", "")
		if err := ctx.parse(c.args); err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if !reflect.DeepEqual(ctx.args, c.pos) || *lease != c.lease {
			t.Errorf("%v: pos=%v lease=%q", c.args, ctx.args, *lease)
		}
	}
	bad := &ctxt{fs: flag.NewFlagSet("t", flag.ContinueOnError)}
	bad.fs.SetOutput(io.Discard)
	if err := bad.parse([]string{"-nope"}); err == nil {
		t.Fatal("undefined flag must be a usage error")
	}
}
