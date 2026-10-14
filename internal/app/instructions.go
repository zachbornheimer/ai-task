package app

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// The block `atask init` owns inside AGENTS.md is wrapped in markers; the
// opening marker carries a hash of the block text so a changed template is
// visible and re-running init rewrites only the block. Text outside the
// markers belongs to the repository and is never touched.
const (
	InstructionsEnd   = "<!-- END atask -->"
	instructionsBegin = "<!-- BEGIN atask v:1 hash:"
	hashLen           = 12
)

var (
	beginMarkerRE = regexp.MustCompile(`<!-- (?:BEGIN atask[^>]*|atask:begin) -->`)
	endMarkerRE   = regexp.MustCompile(`<!-- (?:END atask|atask:end) -->`)
)

// InstructionsBegin is the opening marker for the current Instructions().
func InstructionsBegin() string { return beginMarker(Instructions()) }

func beginMarker(text string) string {
	sum := sha256.Sum256([]byte(text))
	return instructionsBegin + hex.EncodeToString(sum[:])[:hashLen] + " -->"
}

// Instructions is the project instruction block for coding agents: the
// fixed verb list and how work gets done through `atask`.
func Instructions() string {
	return `
**Track verifiable objectives with ` + "`atask`" + `.** An objective is work with a specific outcome and meaningful completion criteria. Run ` + "`atask context`" + ` to learn the workflow. Ensure an objective has a task before doing or delegating its work. The worker claims the task, follows its handoff (` + "`atask context ID`" + `), and requests verification with ` + "`atask verify complete`" + `. The engine determines whether completion requirements are satisfied.

Do not use markdown TODO lists for task tracking.

Verbs: ` + "`atask context`, `atask list`, `atask show`, `atask add`, `atask update`, `atask claim`, `atask log`, `atask verify`" + `.

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
	return InstallInstructionsText(root, Instructions())
}

// InstallInstructionsText installs text as the managed block; a changed
// text changes the hash in the opening marker and rewrites the block.
func InstallInstructionsText(root, text string) (InstallResult, error) {
	res := InstallResult{AgentsMD: filepath.Join(root, "AGENTS.md")}
	block := beginMarker(text) + "\n" + text + InstructionsEnd + "\n"
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
		bl, el := beginMarkerRE.FindStringIndex(s), endMarkerRE.FindStringIndex(s)
		if bl != nil && el != nil && el[0] > bl[0] {
			b, e := bl[0], el[1]
			rest := strings.TrimLeft(s[e:], "\n")
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
	if err := installClaudeHook(root); err != nil {
		return res, err
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
		if t := strings.TrimSpace(line); !beginMarkerRE.MatchString(t) && !endMarkerRE.MatchString(t) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
