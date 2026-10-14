package cli

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestParseInterleavedAndDoubleDash(t *testing.T) {
	cases := []struct {
		args []string
		pos  []string
		note string
	}{
		{[]string{"a", "--note", "1h", "b"}, []string{"a", "b"}, "1h"},
		{[]string{"--note", "1h", "a", "b"}, []string{"a", "b"}, "1h"},
		{[]string{"--", "-a", "-b"}, []string{"-a", "-b"}, ""},
		{[]string{"x", "--", "-a", "-b"}, []string{"x", "-a", "-b"}, ""},
		{[]string{"--note", "2h", "--", "-a", "--note"}, []string{"-a", "--note"}, "2h"},
		{[]string{}, nil, ""},
	}
	for _, c := range cases {
		ctx := &ctxt{fs: flag.NewFlagSet("t", flag.ContinueOnError)}
		ctx.fs.SetOutput(io.Discard)
		note := ctx.fs.String("note", "", "")
		if err := ctx.parse(c.args); err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if !reflect.DeepEqual(ctx.args, c.pos) || *note != c.note {
			t.Errorf("%v: pos=%v note=%q", c.args, ctx.args, *note)
		}
	}
	bad := &ctxt{fs: flag.NewFlagSet("t", flag.ContinueOnError)}
	bad.fs.SetOutput(io.Discard)
	if err := bad.parse([]string{"-nope"}); err == nil {
		t.Fatal("undefined flag must be a usage error")
	}
}
