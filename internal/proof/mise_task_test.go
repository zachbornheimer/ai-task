package proof

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestPresetMiseTasks(t *testing.T) {
	sizes := map[string]Size{"ci": SizeLarge, "lint": SizeMedium, "build": SizeMedium}
	for name, size := range sizes {
		for _, withMise := range []bool{true, false} {
			dir := t.TempDir()
			want := []string{"make", name}
			if withMise {
				if err := os.WriteFile(filepath.Join(dir, "mise.toml"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				want = []string{"mise", "run", name}
			}
			p, ok := DefaultRegistry().Lookup(name)
			if !ok {
				t.Fatalf("preset %q not registered", name)
			}
			got, err := p.Argv(ProofSpec{Preset: name}, Env{Workdir: dir})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s mise=%v: argv %v err %v, want %v", name, withMise, got, err, want)
			}
			if p.DefaultSize() != size {
				t.Errorf("%s size %s, want %s", name, p.DefaultSize(), size)
			}
			_, err = p.Argv(ProofSpec{Preset: name, Subject: "x"}, Env{Workdir: dir})
			if !fault.Is(err, fault.CodeInvalidInput) {
				t.Errorf("%s with subject: err %v, want INVALID_INPUT", name, err)
			}
		}
	}
}
