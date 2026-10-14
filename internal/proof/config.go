package proof

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// Injectable boundaries for locating the config file.
var (
	lookupEnv   = os.LookupEnv
	userHomeDir = os.UserHomeDir
)

// builtinNames are the preset names the contract reserves, whether or not
// this build registers them yet.
var builtinNames = []string{
	"run", "ci", "lint", "build", "go_test", "py_test", "js_test", "rs_test",
	"playwright_run", "file_exists", "file_absent", "file_blake3", "grep",
	"http_ok", "pr_merged",
}

var placeholder = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// ConfigPath returns $XDG_CONFIG_HOME/atask/config.toml, falling back to
// ~/.config/atask/config.toml. It is empty when neither base is known.
func ConfigPath() string {
	if base, ok := lookupEnv("XDG_CONFIG_HOME"); ok && base != "" {
		return filepath.Join(base, "atask", "config.toml")
	}
	home, err := userHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "atask", "config.toml")
}

type configFile struct {
	// Sandbox mode and extra_write land separately; the table is shared.
	Sandbox struct {
		ExtraEnv []string `toml:"extra_env"`
	} `toml:"sandbox"`
	Presets map[string]struct {
		Command []string `toml:"command"`
		Options []string `toml:"options"`
		Size    string   `toml:"size"`
	} `toml:"presets"`
}

// LoadConfig reads custom presets from a TOML file. A missing file yields
// none. A preset named like a built-in is rejected.
func LoadConfig(path string) ([]Preset, error) {
	var cf configFile
	if _, err := toml.DecodeFile(path, &cf); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fault.Wrap(err, fault.CodeInvalidInput, "config %s", path)
	}
	names := make([]string, 0, len(cf.Presets))
	for n := range cf.Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	presets := make([]Preset, 0, len(names))
	for _, name := range names {
		raw := cf.Presets[name]
		p, err := newCustomPreset(name, raw.Command, raw.Options, raw.Size)
		if err != nil {
			return nil, fault.Wrap(err, fault.CodeInvalidInput, "config %s", path)
		}
		presets = append(presets, p)
	}
	return presets, nil
}

// LoadExtraEnv returns the variable names `[sandbox] extra_env` lets
// through to checks. A missing file yields none.
func LoadExtraEnv(path string) ([]string, error) {
	var cf configFile
	if _, err := toml.DecodeFile(path, &cf); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fault.Wrap(err, fault.CodeInvalidInput, "config %s", path)
	}
	return cf.Sandbox.ExtraEnv, nil
}

// RegisterCustom adds custom presets beside the built-ins; none may shadow
// a name already registered or reserved.
func RegisterCustom(reg *Registry, presets []Preset) error {
	for _, p := range presets {
		if _, taken := reg.Lookup(p.Name()); taken || slices.Contains(builtinNames, p.Name()) {
			return fault.New(fault.CodeInvalidInput, "custom preset %q shadows a built-in preset", p.Name())
		}
		reg.Register(p)
	}
	return nil
}

// CustomPreset is a preset defined in config: an argv template whose
// {subject} and {option} placeholders fill from the ProofSpec.
type CustomPreset struct {
	name    string
	command []string
	options []string
	size    Size
}

var _ Preset = CustomPreset{}

func newCustomPreset(name string, command, options []string, size string) (CustomPreset, error) {
	if !presetName.MatchString(name) {
		return CustomPreset{}, fmt.Errorf("preset name %q must match [a-z][a-z0-9_]*", name)
	}
	if slices.Contains(builtinNames, name) {
		return CustomPreset{}, fmt.Errorf("preset %q shadows a built-in preset", name)
	}
	if len(command) == 0 {
		return CustomPreset{}, fmt.Errorf("preset %q needs a non-empty command", name)
	}
	sz := Size(size)
	switch sz {
	case "":
		sz = SizeSmall
	case SizeSmall, SizeMedium, SizeLarge, SizeEternal:
	default:
		return CustomPreset{}, fmt.Errorf("preset %q: size %q must be small, medium, large or eternal", name, size)
	}
	return CustomPreset{name: name, command: command, options: options, size: sz}, nil
}

func (p CustomPreset) Name() string      { return p.name }
func (p CustomPreset) Options() []string { return p.options }
func (p CustomPreset) DefaultSize() Size { return p.size }

// Validate rejects a spec that omits an option the command template uses.
func (p CustomPreset) Validate(spec ProofSpec) error {
	_, err := p.Argv(spec, Env{})
	return err
}

func (p CustomPreset) Argv(spec ProofSpec, _ Env) ([]string, error) {
	var missing []string
	argv := make([]string, len(p.command))
	for i, arg := range p.command {
		argv[i] = placeholder.ReplaceAllStringFunc(arg, func(m string) string {
			key := m[1 : len(m)-1]
			if key == "subject" {
				return spec.Subject
			}
			v, ok := spec.Options[key]
			if !ok && slices.Contains(p.options, key) {
				missing = append(missing, key)
			}
			if !ok {
				return m
			}
			return v
		})
	}
	if len(missing) > 0 {
		return nil, fault.New(fault.CodeInvalidInput, "%s needs option(s): %s", p.name, strings.Join(missing, ", "))
	}
	return argv, nil
}
