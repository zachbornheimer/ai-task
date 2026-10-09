package plan

import (
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestValidate(t *testing.T) {
	ok := ChangeSet{ProjectID: "proj-0000000000000000", Operations: []Change{AddGroup{Key: "g", Title: "G"}, AddTask{Key: "a", Title: "A", Parent: "g"}, UpdateTask{Target: "a", Title: strptr("A2")}, ArchiveTask{Target: "a"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cs   ChangeSet
		code fault.Code
	}{
		{ChangeSet{Operations: []Change{AddTask{Title: "x"}}}, fault.CodeInvalidInput},
		{ChangeSet{ProjectID: "p"}, fault.CodeInvalidInput},
		{ChangeSet{ProjectID: "p", Operations: []Change{AddTask{Key: "k", Title: "a"}, AddTask{Key: "k", Title: "b"}}}, fault.CodeDuplicateKey},
		{ChangeSet{ProjectID: "p", Operations: []Change{AddTask{Title: " "}}}, fault.CodeInvalidInput},
		{ChangeSet{ProjectID: "p", Operations: []Change{AddTask{Title: "a", Requires: []Ref{""}}}}, fault.CodeInvalidInput},
		{ChangeSet{ProjectID: "p", Operations: []Change{UpdateTask{}}}, fault.CodeInvalidInput},
		{ChangeSet{ProjectID: "p", Operations: []Change{UpdateTask{Target: "a", Title: strptr(" ")}}}, fault.CodeInvalidInput},
		{ChangeSet{ProjectID: "p", Operations: []Change{ArchiveTask{}}}, fault.CodeInvalidInput},
		{ChangeSet{ProjectID: "p", Operations: []Change{AddGroup{Key: "bad key", Title: "x"}}}, fault.CodeInvalidInput},
	}
	for i, c := range cases {
		if got := fault.CodeOf(c.cs.Validate()); got != c.code {
			t.Errorf("case %d: %q", i, got)
		}
	}
	if !Ref("at-abc123").IsID() || Ref("key").IsID() {
		t.Fatal("ref")
	}
	if p := Replace([]string{"x"}); !p.Set || len(p.Value) != 1 {
		t.Fatal("patch")
	}
}

func strptr(s string) *string { return &s }
