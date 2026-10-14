package at_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	at "github.com/zachbornheimer/ai-task"
	"github.com/zachbornheimer/ai-task/internal/contract"
)

func TestPublicGoAPI(t *testing.T) {
	wantTypes := []string{"AcceptanceCriterion", "ActionResult", "ActionSpec", "ActionStatus", "AddGroup", "AddTask", "ApplyResult", "ArchiveTask", "Change", "ChangeSet", "CheckType", "ClaimRequest", "Code", "Config", "Engine", "Error", "Evidence", "GroupView", "Handoff", "ListFilter", "ListQuery", "LogEntry", "Patch", "PlanSnapshot", "ProofSpec", "Project", "ProjectID", "Ref", "ReleaseRequest", "RunResult", "Session", "SessionToken", "Size", "Status", "Store", "Summary", "Task", "TaskID", "TaskKind", "TaskView", "UpdateTask", "VerifyMode", "VerifyRequest", "VerifyResult"}
	wantConsts := []string{"ActionDetached", "ActionFailed", "ActionPending", "ActionRunning", "ActionSucceeded", "CheckOnComplete", "CheckProof", "CheckRegression", "FilterAll", "FilterArchived", "FilterBlocked", "FilterComplete", "FilterDraft", "FilterOpen", "FilterReady", "KindGroup", "KindTask", "SizeEternal", "SizeLarge", "SizeMedium", "SizeSmall", "VerifyComplete", "VerifyProof", "VerifyRegression"}
	wantFuncs := []string{"DefaultPath", "ErrorCode", "Glyph", "Open", "ParseSessionToken", "ParseTaskID", "Replace"}
	wantVars := []string{"ErrDone", "ErrStalled"}
	wantEngine := map[string]string{
		"Apply":   "Apply(context.Context, ChangeSet) (ApplyResult, error)",
		"List":    "List(context.Context, ListQuery) (PlanSnapshot, error)",
		"Show":    "Show(context.Context, TaskID) (TaskView, error)",
		"Claim":   "Claim(context.Context, ClaimRequest) (Session, error)",
		"Renew":   "Renew(context.Context, SessionToken) (time.Time, error)",
		"Release": "Release(context.Context, ReleaseRequest) error",
		"Log":     "Log(context.Context, SessionToken, LogEntry) error",
		"Verify":  "Verify(context.Context, VerifyRequest) (VerifyResult, error)",
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	root := filepath.Dir(thisFile)
	fset := token.NewFileSet()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(root, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if f.Name.Name == "at" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		t.Fatalf("package at not found in %s", root)
	}

	var funcs, types, consts, vars []string
	for _, f := range files {
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
	for m := range engine.Methods() {
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
	for in0 := range t.Ins() {
		in = append(in, shortType(in0))
	}
	out := make([]string, 0, t.NumOut())
	for out0 := range t.Outs() {
		out = append(out, shortType(out0))
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
		{"at.VerifyRequest", "VerifyRequest"},
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

var contractConst = regexp.MustCompile(`(?m)^\s+(\w+)\s+\w+\s*=\s*"([a-z_]+)"`)

// The Engine method names and every constant docs/contract.md lists exist in
// package at with the value the document gives.
func TestPublicSurfaceMatchesContract(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Dir(thisFile)
	want, err := contract.Parse(filepath.Join(root, "docs", "contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	engine := reflect.TypeFor[at.Engine]()
	var methods []string
	for method := range engine.Methods() {
		methods = append(methods, method.Name)
	}
	assertExactNames(t, "Engine methods", want.EngineMethods, methods)

	raw, err := os.ReadFile(filepath.Clean(filepath.Join(root, "docs", "contract.md")))
	if err != nil {
		t.Fatal(err)
	}
	_, goAPI, _ := strings.Cut(string(raw), "# Public Code API")
	docConsts := map[string]string{}
	for _, m := range contractConst.FindAllStringSubmatch(goAPI, -1) {
		docConsts[m[1]] = m[2]
	}
	got := map[string]string{
		"VerifyProof": string(at.VerifyProof), "VerifyRegression": string(at.VerifyRegression), "VerifyComplete": string(at.VerifyComplete),
		"KindTask": string(at.KindTask), "KindGroup": string(at.KindGroup),
		"SizeSmall": string(at.SizeSmall), "SizeMedium": string(at.SizeMedium), "SizeLarge": string(at.SizeLarge), "SizeEternal": string(at.SizeEternal),
		"CheckProof": string(at.CheckProof), "CheckRegression": string(at.CheckRegression), "CheckOnComplete": string(at.CheckOnComplete),
		"ActionPending": string(at.ActionPending), "ActionRunning": string(at.ActionRunning), "ActionDetached": string(at.ActionDetached), "ActionSucceeded": string(at.ActionSucceeded), "ActionFailed": string(at.ActionFailed),
		"FilterAll": string(at.FilterAll), "FilterOpen": string(at.FilterOpen), "FilterReady": string(at.FilterReady), "FilterBlocked": string(at.FilterBlocked), "FilterDraft": string(at.FilterDraft), "FilterComplete": string(at.FilterComplete), "FilterArchived": string(at.FilterArchived),
	}
	if !reflect.DeepEqual(docConsts, got) {
		t.Errorf("constants differ from docs/contract.md:\n  doc %v\n  got %v", docConsts, got)
	}
}
