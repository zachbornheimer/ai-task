package cli

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	repoLintConfigPath      = "../../.golangci.yml"
	sharedLintConfigRelPath = ".local/share/dev-profile/golangci.yml"
	execBoundaryRuleText    = "^G204:"
)

type lintConfig struct {
	Linters struct {
		Enable     []string `yaml:"enable"`
		Exclusions struct {
			Rules []struct {
				Linters []string `yaml:"linters"`
				Text    string   `yaml:"text"`
			} `yaml:"rules"`
		} `yaml:"exclusions"`
	} `yaml:"linters"`
}

func readLintConfig(t *testing.T, path string) lintConfig {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatalf("read lint config %s: %v", path, err)
	}
	var cfg lintConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse lint config %s: %v", path, err)
	}
	return cfg
}

func TestRepoLintConfigKeepsSharedRules(t *testing.T) {
	account, err := user.Current() // real home: tests override $HOME
	if err != nil {
		t.Skipf("no account home to locate the shared lint config: %v", err)
	}
	sharedPath := filepath.Join(account.HomeDir, sharedLintConfigRelPath)
	if _, err := os.Stat(sharedPath); err != nil {
		t.Skipf("shared dev-profile lint config not installed at %s", sharedPath)
	}
	repo := readLintConfig(t, repoLintConfigPath)
	shared := readLintConfig(t, sharedPath)

	if !reflect.DeepEqual(repo.Linters.Enable, shared.Linters.Enable) {
		t.Errorf("enabled linters drifted from shared config:\nrepo   %v\nshared %v",
			repo.Linters.Enable, shared.Linters.Enable)
	}
	rules := repo.Linters.Exclusions.Rules
	if len(rules) != 1 {
		t.Fatalf("want exactly one exclusion rule, got %d", len(rules))
	}
	if !reflect.DeepEqual(rules[0].Linters, []string{"gosec"}) || rules[0].Text != execBoundaryRuleText {
		t.Errorf("exclusion must target gosec G204 only, got %+v", rules[0])
	}
}
