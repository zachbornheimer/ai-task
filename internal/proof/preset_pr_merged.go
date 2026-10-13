package proof

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

var (
	prURLPath = regexp.MustCompile(`^/[^/]+/[^/]+/pull/\d+/?$`)
	_         = Judger(prMergedPreset{})
)

func init() { registerBuiltin(func() Preset { return prMergedPreset{} }) }

// prMergedPreset passes when gh reports the pull request merged into the
// target branch.
type prMergedPreset struct{ target string }

// ForEnv binds the target branch the PR must have merged into.
func (p prMergedPreset) ForEnv(env Env) Preset { p.target = env.TargetBranch; return p }

func (prMergedPreset) Name() string      { return "pr_merged" }
func (prMergedPreset) Options() []string { return nil }
func (prMergedPreset) DefaultSize() Size { return SizeSmall }

func (prMergedPreset) Argv(spec ProofSpec, _ Env) ([]string, error) {
	s := strings.TrimSpace(spec.Subject)
	if !validPRSubject(s) {
		return nil, fault.New(fault.CodeInvalidInput, "pr_merged needs a positive PR number or a GitHub PR URL, got %q", spec.Subject)
	}
	return []string{"gh", "pr", "view", s, "--json", "state,mergedAt,baseRefName"}, nil
}

func validPRSubject(s string) bool {
	if n, err := strconv.Atoi(s); err == nil {
		return n > 0 && strconv.Itoa(n) == s
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && prURLPath.MatchString(u.Path)
}

// Judge reads gh's JSON. A non-zero gh exit is always a failure.
func (p prMergedPreset) Judge(output []byte, exitCode int) (bool, string) {
	if exitCode != 0 {
		line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
		return false, fmt.Sprintf("gh failed (exit %d): %s", exitCode, line)
	}
	var pr struct {
		State       string `json:"state"`
		BaseRefName string `json:"baseRefName"`
	}
	start := bytes.IndexByte(output, '{')
	if start < 0 || json.Unmarshal(output[start:], &pr) != nil {
		return false, "gh output is not the expected JSON"
	}
	if pr.State != "MERGED" {
		return false, fmt.Sprintf("PR state is %s, not MERGED", pr.State)
	}
	if p.target != "" && pr.BaseRefName != p.target {
		return false, fmt.Sprintf("PR merged into %s, not %s", pr.BaseRefName, p.target)
	}
	return true, ""
}
