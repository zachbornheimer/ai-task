package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestIDs(t *testing.T) {
	id := NewID()
	if _, err := ParseID(string(id)); err != nil {
		t.Fatalf("fresh id does not parse: %v", err)
	}
	for _, bad := range []string{"", "proj-", "proj-short", "task-0123456789abcdefg", "proj-0123456789abcdefI"} {
		if _, err := ParseID(bad); !fault.Is(err, fault.CodeInvalidInput) {
			t.Errorf("ParseID(%q) = %v, want INVALID_INPUT", bad, err)
		}
	}
	if NewID() == NewID() {
		t.Fatal("ids must be random")
	}
}

func TestLocateRootMainAndLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	base, _ = CanonicalRoot(base)
	main := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "feat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(main, "sub", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "repo.feat")
	if err := os.MkdirAll(filepath.Join(linked, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := "gitdir: " + filepath.Join(main, ".git", "worktrees", "feat") + "\n"
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte(gitFile), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{main, filepath.Join(main, "sub", "deeper"), linked, filepath.Join(linked, "pkg")} {
		got, err := LocateRoot(dir)
		if err != nil {
			t.Fatalf("LocateRoot(%s): %v", dir, err)
		}
		if got != main {
			t.Errorf("LocateRoot(%s) = %q, want %q", dir, got, main)
		}
	}
	plain := filepath.Join(base, "plain")
	os.MkdirAll(plain, 0o755)
	got, err := LocateRoot(plain)
	if err != nil || got != "" {
		t.Fatalf("non-git dir: %q %v", got, err)
	}
}

func TestMatchRoot(t *testing.T) {
	sep := string(filepath.Separator)
	roots := []string{sep + "a", sep + "a" + sep + "b", sep + "c", ""}
	cases := map[string]string{
		sep + "a" + sep + "b" + sep + "x": sep + "a" + sep + "b",
		sep + "a" + sep + "y":             sep + "a",
		sep + "a":                         sep + "a",
		sep + "ab":                        "",
		sep + "d":                         "",
	}
	for dir, want := range cases {
		if got := MatchRoot(dir, roots); got != want {
			t.Errorf("MatchRoot(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Project{ID: NewID(), Name: "x", Integration: IntegrationNone}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Project{
		{Name: " ", Integration: IntegrationNone},
		{Name: "x", Integration: "weird"},
		{Name: "x", Integration: IntegrationNone, RootPath: "relative"},
	}
	for _, p := range bad {
		if err := p.Validate(); !fault.Is(err, fault.CodeInvalidInput) {
			t.Errorf("%+v: %v", p, err)
		}
	}
}
