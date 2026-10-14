package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// Markers delimit the block `atask init` owns inside AGENTS.md. Text outside
// the markers belongs to the repository and is never touched.
const (
	InstructionsBegin = "<!-- atask:begin -->"
	InstructionsEnd   = "<!-- atask:end -->"
)

// Instructions is the project instruction block for coding agents: how
// work gets done through `atask` (any request becomes tasks), the agent
// verbs, and the execution contract. `atask init` installs it into
// AGENTS.md between the markers and keeps it current on re-runs.
func Instructions() string {
	return `
**Track verifiable objectives with ` + "`atask`" + `.** An objective is work with a specific outcome and meaningful completion criteria. Run ` + "`atask context`" + ` to learn the workflow. Ensure an objective has a task before doing or delegating its work. The worker claims the task, follows its handoff (` + "`atask context ID`" + `), and requests verification with ` + "`atask verify complete`" + `. The engine determines whether completion requirements are satisfied.

`
}

// DefaultGuidelines is the repository section `atask init` writes into a
// new AGENTS.md outside the markers: engineering rules the repository's
// owners are expected to edit.
const DefaultGuidelines = `## Engineering guidelines

Edit this section for the repository; the block above is owned by ` + "`atask init`" + `.

- Prefer the smallest complete change that satisfies the task; follow the
  existing architecture and conventions; avoid unrelated refactoring,
  formatting, or dependency changes.
- Handle errors explicitly; preserve existing behaviour and public
  contracts unless the task requires otherwise.
- Test observable behaviour, including failure paths, with deterministic
  tests; fix defects rather than weakening assertions.
- Read the code and documentation the task needs, not the repository by
  default; keep documentation consistent when a documented contract changes.
- Report results and blockers concisely; distinguish verified results from
  assumptions.
`

// InstallResult says what InstallInstructions wrote.
type InstallResult struct {
	AgentsMD string `json:"agents_md"`           // path written or updated
	ClaudeMD string `json:"claude_md,omitempty"` // path written, if created or amended
	Created  bool   `json:"created"`             // AGENTS.md did not exist
	Updated  bool   `json:"updated"`             // the block changed
}

// InstallInstructions writes the instruction block into <root>/AGENTS.md
// between the markers (creating the file with DefaultGuidelines when it
// is absent, appending the block when the file exists without markers,
// replacing the block when it does) and makes sure CLAUDE.md imports
// AGENTS.md for harnesses that read only CLAUDE.md. Idempotent.
func InstallInstructions(root string) (InstallResult, error) {
	res := InstallResult{AgentsMD: filepath.Join(root, "AGENTS.md")}
	block := InstructionsBegin + "\n" + Instructions() + InstructionsEnd + "\n"
	existing, err := os.ReadFile(res.AgentsMD)
	var content string
	switch {
	case os.IsNotExist(err):
		content = block + "\n" + DefaultGuidelines
		res.Created, res.Updated = true, true
	case err != nil:
		return res, fault.WrapInternal(err, "read AGENTS.md")
	default:
		s := string(existing)
		b, e := strings.Index(s, InstructionsBegin), strings.Index(s, InstructionsEnd)
		if b >= 0 && e > b {
			rest := strings.TrimLeft(s[e+len(InstructionsEnd):], "\n")
			if rest != "" {
				rest = "\n" + rest // one blank line separates the block from the text after it
			}
			content = s[:b] + block + rest
		} else {
			// No complete block: a stray marker left by a hand edit would
			// otherwise survive next to the new block and make two.
			s = dropMarkerLines(s)
			content = strings.TrimRight(s, "\n") + "\n\n" + block
		}
		res.Updated = content != string(existing)
	}
	if res.Updated {
		if err := writeUnder(root, "AGENTS.md", []byte(content)); err != nil {
			return res, fault.WrapInternal(err, "write AGENTS.md")
		}
	}
	claude := filepath.Join(root, "CLAUDE.md")
	cb, err := readUnder(root, "CLAUDE.md")
	switch {
	case os.IsNotExist(err):
		if err := writeUnder(root, "CLAUDE.md", []byte("@AGENTS.md\n")); err != nil {
			return res, fault.WrapInternal(err, "write CLAUDE.md")
		}
		res.ClaudeMD = claude
	case err != nil:
		return res, fault.WrapInternal(err, "read CLAUDE.md")
	case !strings.Contains(string(cb), "@AGENTS.md"):
		if err := writeUnder(root, "CLAUDE.md", []byte(strings.TrimRight(string(cb), "\n")+"\n\n@AGENTS.md\n")); err != nil {
			return res, fault.WrapInternal(err, "amend CLAUDE.md")
		}
		res.ClaudeMD = claude
	}
	return res, nil
}

func writeUnder(root, name string, data []byte) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func readUnder(root, name string) ([]byte, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	f, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// dropMarkerLines removes lines that are only a block marker.
func dropMarkerLines(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != InstructionsBegin && t != InstructionsEnd {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
