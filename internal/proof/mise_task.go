package proof

import (
	"os"
	"path/filepath"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

var miseConfigs = []string{"mise.toml", ".mise.toml", filepath.Join(".config", "mise", "config.toml")}

// miseOrMake returns the argv that runs task through mise when the worktree
// has a mise config, else through make.
func miseOrMake(task string, env Env) []string {
	for _, c := range miseConfigs {
		if _, err := os.Stat(filepath.Join(env.Workdir, c)); err == nil {
			return []string{"mise", "run", task}
		}
	}
	return []string{"make", task}
}

// miseTaskPreset is a subject-less preset that runs one named repo task.
type miseTaskPreset struct {
	name string
	size Size
}

func (p miseTaskPreset) Name() string      { return p.name }
func (miseTaskPreset) Options() []string   { return nil }
func (p miseTaskPreset) DefaultSize() Size { return p.size }

func (p miseTaskPreset) Argv(spec ProofSpec, env Env) ([]string, error) {
	if spec.Subject != "" {
		return nil, fault.New(fault.CodeInvalidInput, "%s takes no subject, got %q", p.name, spec.Subject)
	}
	return miseOrMake(p.name, env), nil
}
