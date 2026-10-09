package cli

import (
	"reflect"
	"testing"
)

func TestSplitWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{"go test ./...", []string{"go", "test", "./..."}, false},
		{`sh -c 'echo "hi there"'`, []string{"sh", "-c", `echo "hi there"`}, false},
		{`a\ b c`, []string{"a b", "c"}, false},
		{`"x y" z`, []string{"x y", "z"}, false},
		{"  spaced   out ", []string{"spaced", "out"}, false},
		{`unterminated "quote`, nil, true},
		{"", nil, false},
	}
	for _, c := range cases {
		got, err := splitWords(c.in)
		if (err != nil) != c.err {
			t.Errorf("%q: err=%v", c.in, err)
			continue
		}
		if !c.err && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}
