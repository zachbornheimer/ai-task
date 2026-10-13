package proof

import (
	"sort"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// Env is what a preset may know about where it runs.
type Env struct {
	Workdir string
}

// Preset turns a ProofSpec into the argv that proves it.
type Preset interface {
	Name() string
	// Options lists the optional keys the preset accepts.
	Options() []string
	DefaultSize() Size
	Argv(spec ProofSpec, env Env) ([]string, error)
}

// Registry holds presets by name.
type Registry struct {
	presets map[string]Preset
}

func NewRegistry() *Registry { return &Registry{presets: map[string]Preset{}} }

// DefaultRegistry returns a registry holding the built-in presets.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(runPreset{})
	return r
}

func (r *Registry) Register(p Preset) { r.presets[p.Name()] = p }

func (r *Registry) Lookup(name string) (Preset, bool) {
	p, ok := r.presets[name]
	return p, ok
}

// List returns registered preset names, sorted.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.presets))
	for n := range r.presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func errUnknownPreset(name string) error {
	return fault.New(fault.CodeInvalidInput, "unknown preset %q", name)
}
