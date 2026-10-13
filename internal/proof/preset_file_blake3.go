package proof

import (
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lukechampine.com/blake3"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

const blake3HexLen = 64

func init() { registerBuiltin(func() Preset { return fileBlake3Preset{} }) }

// fileBlake3Preset passes when the blake3 digest of PATH equals hash=HEX.
type fileBlake3Preset struct{}

func (fileBlake3Preset) Name() string      { return "file_blake3" }
func (fileBlake3Preset) Options() []string { return []string{"hash"} }
func (fileBlake3Preset) DefaultSize() Size { return SizeSmall }

func (fileBlake3Preset) Argv(ProofSpec, Env) ([]string, error) {
	return nil, fault.New(fault.CodeInvalidInput, "file_blake3 evaluates in-process and has no argv")
}

func (fileBlake3Preset) Evaluate(spec ProofSpec, workdir string) (bool, string, error) {
	want := strings.ToLower(spec.Options["hash"])
	if len(want) != blake3HexLen {
		return false, "", fault.New(fault.CodeInvalidInput, "file_blake3 needs hash=HEX of %d hex chars, got %q", blake3HexLen, want)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return false, "", fault.New(fault.CodeInvalidInput, "file_blake3 hash %q is not hex", want)
	}
	path, err := insideWorkdir(workdir, spec.Subject)
	if err != nil {
		return false, "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, "", fault.Wrap(err, fault.CodeInvalidInput, "file_blake3 open %q", spec.Subject)
	}
	defer f.Close()
	h := blake3.New(32, nil)
	if _, err := io.Copy(h, f); err != nil {
		return false, "", fault.Wrap(err, fault.CodeInvalidInput, "file_blake3 read %q", spec.Subject)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return false, "blake3 mismatch for " + spec.Subject + ": got " + got + ", want " + want, nil
	}
	return true, "blake3 match for " + spec.Subject, nil
}

// insideWorkdir resolves rel under workdir and rejects paths that leave it.
func insideWorkdir(workdir, rel string) (string, error) {
	path := filepath.Join(workdir, rel)
	back, err := filepath.Rel(workdir, path)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fault.New(fault.CodeInvalidInput, "path %q is outside the worktree", rel)
	}
	return path, nil
}
