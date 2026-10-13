package cli

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/contract"
	"github.com/zachbornheimer/ai-task/internal/project"
)

const (
	contractDoc   = "../../docs/contract.md"
	faultSource   = "../fault/fault.go"
	cliCallBudget = 20 * time.Second
)

type testEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// faultCodeValues returns the string value of every exported Code constant
// declared in internal/fault/fault.go.
func faultCodeValues(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), faultSource, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", faultSource, err)
	}
	var values []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != "Code" {
			return true
		}
		for _, v := range spec.Values {
			if lit, ok := v.(*ast.BasicLit); ok {
				s, _ := strconv.Unquote(lit.Value)
				values = append(values, s)
			}
		}
		return true
	})
	return values
}

// atask runs the CLI in-process against one database and working directory,
// failing the test if the call does not return in time.
func atask(t *testing.T, db, cwd string, args ...string) (int, testEnvelope) {
	t.Helper()
	type outcome struct {
		code int
		out  string
	}
	done := make(chan outcome, 1)
	go func() {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: func(k string) string {
			if k == "ATASK_DB" {
				return db
			}
			return ""
		}})
		done <- outcome{code, out.String()}
	}()
	select {
	case o := <-done:
		var env testEnvelope
		if err := json.Unmarshal([]byte(o.out), &env); err != nil {
			t.Fatalf("atask %v: stdout is not a JSON envelope: %v\n%s", args, err, o.out)
		}
		return o.code, env
	case <-time.After(cliCallBudget):
		t.Fatalf("atask %v did not return within %s", args, cliCallBudget)
		return 0, testEnvelope{}
	}
}

func wantErrorCode(t *testing.T, env testEnvelope, want string, args ...string) {
	t.Helper()
	if env.OK || env.Error == nil || env.Error.Code != want || env.Error.Message == "" {
		t.Fatalf("atask %v: want error %s with a message, got %+v", args, want, env)
	}
}

func TestErrorCodesMatchContract(t *testing.T) {
	listed, err := contract.ErrorCodes(contractDoc)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range faultCodeValues(t) {
		if !slices.Contains(listed, code) {
			t.Errorf("fault code %s is not in the contract's code list %v", code, listed)
		}
	}
	for _, code := range listed {
		if !slices.Contains(faultCodeValues(t), code) {
			t.Errorf("contract code %s has no fault.Code constant", code)
		}
	}

	t.Run("claim wait on an empty board is DONE", func(t *testing.T) {
		db, cwd := filepath.Join(t.TempDir(), "at.db"), t.TempDir()
		args := []string{"--json", "claim", "--wait"}
		_, env := atask(t, db, cwd, args...)
		wantErrorCode(t, env, "DONE", args...)
	})

	t.Run("claim wait with only blocked tasks is STALLED", func(t *testing.T) {
		db, cwd := filepath.Join(t.TempDir(), "at.db"), t.TempDir()
		_, added := atask(t, db, cwd, "--json", "add", "first", "--proof", "run: false", "--regression", "run: true")
		var first struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		}
		if err := json.Unmarshal(added.Result, &first); err != nil || first.Task.ID == "" {
			t.Fatalf("add first: %v %+v", err, added)
		}
		atask(t, db, cwd, "--json", "add", "second", "--requires", first.Task.ID, "--proof", "run: true", "--regression", "run: true")
		for range project.DefaultMaxAttempts {
			_, claimed := atask(t, db, cwd, "--json", "claim", first.Task.ID)
			var session struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(claimed.Result, &session); err != nil || session.Token == "" {
				t.Fatalf("claim first: %v %+v", err, claimed.Error)
			}
			atask(t, db, cwd, "--json", "verify", "complete", session.Token)
			if _, released := atask(t, db, cwd, "--json", "claim", "release", session.Token); !released.OK {
				t.Fatalf("release: %+v", released.Error)
			}
		}
		args := []string{"--json", "claim", "--wait"}
		_, env := atask(t, db, cwd, args...)
		wantErrorCode(t, env, "STALLED", args...)
	})

	t.Run("failed verify is CHECK_FAILED", func(t *testing.T) {
		db, cwd := filepath.Join(t.TempDir(), "at.db"), t.TempDir()
		_, add := atask(t, db, cwd, "--json", "add", "doomed", "--proof", "run: false", "--regression", "run: true", "--claim")
		var res struct {
			Session struct {
				Token string `json:"token"`
			} `json:"session"`
		}
		if err := json.Unmarshal(add.Result, &res); err != nil || res.Session.Token == "" {
			t.Fatalf("add --claim: %v %+v", err, add)
		}
		args := []string{"--json", "verify", "complete", res.Session.Token}
		_, env := atask(t, db, cwd, args...)
		wantErrorCode(t, env, "CHECK_FAILED", args...)
	})

	for _, args := range [][]string{{"--json", "help"}, {"--json"}, {"--json", "version"}} {
		t.Run("envelope for "+strings.Join(args, " "), func(t *testing.T) {
			_, env := atask(t, filepath.Join(t.TempDir(), "at.db"), t.TempDir(), args...)
			if !env.OK || len(env.Result) == 0 {
				t.Fatalf("atask %v: want {ok:true,result}, got %+v", args, env)
			}
		})
	}
}
