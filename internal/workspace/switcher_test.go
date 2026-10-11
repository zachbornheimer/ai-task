package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseSwitchPathJSONLine(t *testing.T) {
	out := []byte("{\"action\":\"created\",\"branch\":\"atask/x\",\"path\":\"/tmp/ws\"}\n○ Switched\n")
	path, err := parseSwitchPath(out)
	if err != nil || path != "/tmp/ws" {
		t.Fatalf("%q %v", path, err)
	}
}

func TestParseSwitchPathMissing(t *testing.T) {
	if _, err := parseSwitchPath([]byte("not json\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestWorktrunkSwitcherRequiresWt(t *testing.T) {
	s := WorktrunkSwitcher{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	_, err := s.Ensure(context.Background(), t.TempDir(), "atask/x", "main", true)
	if err == nil || !strings.Contains(err.Error(), "Worktrunk") {
		t.Fatalf("%v", err)
	}
}

func TestWorktrunkSwitcherArgs(t *testing.T) {
	var got []string
	s := WorktrunkSwitcher{
		LookPath: func(string) (string, error) { return "/bin/wt", nil },
		Run: func(_ context.Context, wt, dir string, args []string) ([]byte, error) {
			got = append([]string{wt, dir}, args...)
			return []byte("{\"action\":\"created\",\"branch\":\"atask/x\",\"path\":\"/tmp/out\"}\n"), nil
		},
	}
	path, err := s.Ensure(context.Background(), "/repo", "atask/x", "main", true)
	if err != nil || path != "/tmp/out" {
		t.Fatalf("%q %v", path, err)
	}
	want := []string{"/bin/wt", "/repo", "switch", "--no-cd", "--no-hooks", "--yes", "--format", "json", "--create", "--base", "main", "atask/x"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v want %v", got, want)
	}
}
