package proof

import (
	"slices"
	"testing"
)

func TestPresetPRMerged(t *testing.T) {
	p := prMergedPreset{}
	for _, bad := range []string{"", "0", "-3", "abc", "012", "https://example.com/a/b/pull/1", "https://github.com/a/b/issues/1"} {
		if _, err := p.Argv(ProofSpec{Subject: bad}, Env{}); err == nil {
			t.Errorf("subject %q accepted", bad)
		}
	}
	argv, err := p.Argv(ProofSpec{Subject: "42"}, Env{})
	want := []string{"gh", "pr", "view", "42", "--json", "state,mergedAt,baseRefName"}
	if err != nil || !slices.Equal(argv, want) {
		t.Fatalf("argv = %v, %v", argv, err)
	}
	if _, err := p.Argv(ProofSpec{Subject: "https://github.com/o/r/pull/7"}, Env{}); err != nil {
		t.Errorf("URL rejected: %v", err)
	}
	merged := []byte(`{"state":"MERGED","mergedAt":"2026-01-01T00:00:00Z","baseRefName":"main"}`)
	open := []byte(`{"state":"OPEN","mergedAt":null,"baseRefName":"main"}`)
	cases := []struct {
		name   string
		target string
		out    []byte
		code   int
		pass   bool
	}{
		{"merged to target", "main", merged, 0, true},
		{"merged, no target", "", merged, 0, true},
		{"merged elsewhere", "release", merged, 0, false},
		{"open", "main", open, 0, false},
		{"gh missing", "main", []byte("gh: command not found\nmore"), 127, false},
		{"gh auth", "", []byte(`{"state":"MERGED"}`), 1, false},
		{"garbage", "", []byte("nope"), 0, false},
	}
	for _, c := range cases {
		j := p.ForEnv(Env{TargetBranch: c.target}).(Judger)
		pass, reason := j.Judge(c.out, c.code)
		if pass != c.pass || (!pass && reason == "") {
			t.Errorf("%s: pass=%v reason=%q", c.name, pass, reason)
		}
	}
}
