package at_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	at "github.com/zachbornheimer/ai-task"
)

func TestPublicGoAPI(t *testing.T) {
	wantTypes := []string{
		"ActionID", "ActionKind", "ActionResult", "ActionSpec", "ActionStatus",
		"ApplyResult", "Change", "ChangeSet", "CheckSpec", "ClaimRequest",
		"Engine", "Handoff", "ListQuery", "ListFilter", "LogEntry",
		"PlanSnapshot", "ProjectID", "Session", "SessionToken", "TaskDefinition",
		"TaskID", "TaskKind", "TaskView", "VerifyMode", "VerifyRequest", "VerifyResult",
	}
	wantConsts := []string{
		"ActionBuiltin", "ActionCommand", "ActionFailed", "ActionPending",
		"ActionRunning", "ActionSucceeded",
		"FilterAll", "FilterArchived", "FilterBlocked", "FilterComplete",
		"FilterOpen", "FilterReady",
		"KindGroup", "KindTask",
		"VerifyComplete", "VerifyRegression", "VerifyTask",
	}
	wantFuncs := []string{} // Open and helpers are bootstrap, not the Engine contract
	wantVars := []string{}
	wantEngine := map[string]string{
		"Apply":   "Apply(context.Context, ChangeSet) (ApplyResult, error)",
		"List":    "List(context.Context, ListQuery) (PlanSnapshot, error)",
		"Show":    "Show(context.Context, TaskID) (TaskView, error)",
		"Claim":   "Claim(context.Context, ClaimRequest) (Session, error)",
		"Renew":   "Renew(context.Context, SessionToken) (time.Time, error)",
		"Release": "Release(context.Context, SessionToken, string) error",
		"Log":     "Log(context.Context, SessionToken, LogEntry) error",
		"Verify":  "Verify(context.Context, VerifyRequest) (VerifyResult, error)",
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	root := filepath.Dir(thisFile)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, root, func(fi os.FileInfo) bool {
		name := fi.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := pkgs["at"]
	if !ok {
		var names []string
		for k := range pkgs {
			names = append(names, k)
		}
		t.Fatalf("package at not found in %s; got %v", root, names)
	}

	var funcs, types, consts, vars []string
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil || d.Name == nil || !d.Name.IsExported() {
					continue
				}
				funcs = append(funcs, d.Name.Name)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						if spec.Name.IsExported() {
							types = append(types, spec.Name.Name)
						}
					case *ast.ValueSpec:
						for _, n := range spec.Names {
							if !n.IsExported() {
								continue
							}
							if d.Tok == token.CONST {
								consts = append(consts, n.Name)
							} else {
								vars = append(vars, n.Name)
							}
						}
					}
				}
			}
		}
	}
	assertExactNames(t, "exported types", wantTypes, types)
	assertExactNames(t, "exported consts", wantConsts, consts)
	assertExactNames(t, "exported funcs", wantFuncs, funcs)
	assertExactNames(t, "exported vars", wantVars, vars)

	engine := reflect.TypeFor[at.Engine]()
	gotMethods := map[string]string{}
	var gotNames []string
	for i := 0; i < engine.NumMethod(); i++ {
		m := engine.Method(i)
		gotMethods[m.Name] = shortMethod(m)
		gotNames = append(gotNames, m.Name)
	}
	var wantNames []string
	for n := range wantEngine {
		wantNames = append(wantNames, n)
	}
	assertExactNames(t, "Engine methods", wantNames, gotNames)
	for name, wantSig := range wantEngine {
		gotSig, ok := gotMethods[name]
		if !ok {
			continue
		}
		if gotSig != wantSig {
			t.Errorf("Engine.%s signature:\n  want %s\n  got  %s", name, wantSig, gotSig)
		}
	}
}

func shortMethod(m reflect.Method) string {
	t := m.Type
	in := make([]string, 0, t.NumIn())
	for i := 0; i < t.NumIn(); i++ {
		in = append(in, shortType(t.In(i)))
	}
	out := make([]string, 0, t.NumOut())
	for i := 0; i < t.NumOut(); i++ {
		out = append(out, shortType(t.Out(i)))
	}
	s := m.Name + "(" + strings.Join(in, ", ") + ")"
	switch len(out) {
	case 0:
		return s
	case 1:
		return s + " " + out[0]
	default:
		return s + " (" + strings.Join(out, ", ") + ")"
	}
}

func shortType(t reflect.Type) string {
	s := t.String()
	repl := [][2]string{
		{"plan.Result", "ApplyResult"},
		{"execution.Token", "SessionToken"},
		{"project.ID", "ProjectID"},
		{"verification.Mode", "VerifyMode"},
		{"task.ID", "TaskID"},
		{"app.", ""},
		{"plan.", ""},
		{"execution.", ""},
		{"project.", ""},
		{"verification.", ""},
		{"task.", ""},
		{"fault.", ""},
	}
	for _, r := range repl {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return s
}

func assertExactNames(t *testing.T, label string, want, got []string) {
	t.Helper()
	w, g := map[string]bool{}, map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	for _, s := range got {
		g[s] = true
	}
	var missing, extra []string
	for k := range w {
		if !g[k] {
			missing = append(missing, k)
		}
	}
	for k := range g {
		if !w[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return
	}
	t.Errorf("%s: missing %v extra %v", label, missing, extra)
}
