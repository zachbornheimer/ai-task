package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// SandboxMode says whether a command is confined.
type SandboxMode string

const (
	// SandboxStrict confines the command, and fails the run when it cannot.
	SandboxStrict SandboxMode = "strict"
	// SandboxNone runs the command unconfined.
	SandboxNone SandboxMode = "none"
)

// SandboxEnvVar marks a process that already runs confined. A confined
// process cannot confine its children again, so they inherit the
// confinement instead.
const SandboxEnvVar = "ATASK_SANDBOX"

const sandboxExecBinary = "sandbox-exec"

// ErrSandboxUnavailable is the reason a strict run could not be confined.
var ErrSandboxUnavailable = errors.New(`sandbox unavailable: install sandbox-exec or set [sandbox] mode = "none" in ~/.config/atask/config.toml`)

// Sandbox is the confinement policy of one run: writes under Home are
// denied except beneath Writable, and reads beneath Unreadable are denied.
// A nil *Sandbox in Spec means no confinement (internal tooling).
type Sandbox struct {
	Mode       SandboxMode
	Home       string
	Writable   []string
	Unreadable []string
	// Readable re-allows reads beneath an Unreadable path (the check's own
	// directory lives inside one).
	Readable []string
	// LookPath finds sandbox-exec; nil means exec.LookPath.
	LookPath func(string) (string, error)
	// GOOS names the platform; empty means the running one.
	GOOS string
}

func (s *Sandbox) goos() string {
	if s.GOOS != "" {
		return s.GOOS
	}
	return runtime.GOOS
}

// Active reports whether a run under s is confined by wrapping its argv.
func (s *Sandbox) Active() bool {
	return s != nil && s.Mode == SandboxStrict && s.goos() == "darwin"
}

// Inherited reports whether the current process already runs confined.
func Inherited() bool { return os.Getenv(SandboxEnvVar) != "" }

// Check fails closed: a strict sandbox whose tool is missing is an error.
func (s *Sandbox) Check() error {
	if !s.Active() || Inherited() {
		return nil
	}
	_, err := s.sandboxExec()
	return err
}

func (s *Sandbox) sandboxExec() (string, error) {
	look := s.LookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(sandboxExecBinary)
	if err != nil {
		return "", ErrSandboxUnavailable
	}
	return path, nil
}

// Profile renders the Seatbelt profile. Later rules win, so the order is
// allow-all, deny home writes, re-allow the writable paths, deny secret reads,
// re-allow the reads the run itself needs.
func (s *Sandbox) Profile() string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	fmt.Fprintf(&b, "(deny file-write* %s)\n", subpaths(s.Home))
	for _, p := range s.Writable {
		fmt.Fprintf(&b, "(allow file-write* %s)\n", subpaths(p))
	}
	for _, p := range s.Unreadable {
		fmt.Fprintf(&b, "(deny file-read* %s)\n", subpaths(p))
	}
	for _, p := range s.Readable {
		fmt.Fprintf(&b, "(allow file-read* %s)\n", subpaths(p))
	}
	return b.String()
}

// subpaths matches path and, when it is a symlink, its target: Seatbelt
// compares real paths.
func subpaths(path string) string {
	forms := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		forms = append(forms, resolved)
	}
	parts := make([]string, len(forms))
	for i, f := range forms {
		parts[i] = fmt.Sprintf("(subpath %q)", f)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(require-any " + strings.Join(parts, " ") + ")"
}

// wrap writes the profile under dir and returns the argv that runs argv
// confined by it.
func (s *Sandbox) wrap(argv []string, dir string) ([]string, error) {
	tool, err := s.sandboxExec()
	if err != nil {
		return nil, err
	}
	profile := filepath.Join(dir, "profile.sb")
	if err := os.WriteFile(profile, []byte(s.Profile()), 0o600); err != nil {
		return nil, fmt.Errorf("write sandbox profile: %w", err)
	}
	return append([]string{tool, "-f", profile}, argv...), nil
}
