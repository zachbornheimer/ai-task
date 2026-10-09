package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestDependenciesAtCreationReleaseStatusAndWait(t *testing.T) {
	e := newEnv(t)
	e.ok("init", "--name", "g")
	a := e.ok("add", "a")["id"].(string)
	b := e.ok("add", "b", "--requires", a)["id"].(string)
	if e.ok("show", b)["status"] != "blocked" {
		t.Fatal("b must be blocked at creation")
	}
	// A cycle through --requires/--blocks creates nothing.
	e.fails("DEPENDENCY_CYCLE", "add", "cyc", "--requires", b, "--blocks", a)
	st := e.ok("status")
	if st["total"].(float64) != 2 || st["takeable"].(float64) != 1 || st["stuck"] == true || st["done"] == true {
		t.Fatalf("status: %v", st)
	}
	// Discovered blocker: add a prerequisite that blocks a, release a.
	sa := e.ok("take", a)
	tok := sa["token"].(string)
	p := e.ok("add", "prereq", "--blocks", a)["id"].(string)
	rel := e.ok("release", tok, "--note", "needs prereq")
	if rel["status"] != "blocked" {
		t.Fatalf("release: %v", rel)
	}
	next := e.ok("take")
	if next["task_id"] != p {
		t.Fatalf("automatic take must pick the prerequisite: %v", next)
	}
	e.ok("finish", next["token"].(string))
	// --wait returns promptly once something is takeable.
	start := time.Now()
	w := e.ok("take", "--wait", "5s", "--poll", "100ms")
	if w["task_id"] != a || time.Since(start) > 4*time.Second {
		t.Fatalf("wait take: %v", w)
	}
	// --wait on a held task times out with the original code.
	start = time.Now()
	r := e.fails("TASK_ALREADY_TAKEN", "take", a, "--wait", "400ms", "--poll", "100ms")
	if time.Since(start) < 300*time.Millisecond {
		t.Fatalf("did not wait: %+v", r)
	}
	// Manual gate: stuck for machines, visible for humans.
	gate := e.ok("add", "approve", "--manual", "--blocks", b)["id"].(string)
	e.ok("finish", w["token"].(string))
	st = e.ok("status")
	if st["stuck"] != true || st["manual_pending"].(float64) != 1 {
		t.Fatalf("status with manual gate: %v", st)
	}
	e.fails("NO_AVAILABLE_TASK", "take")
	human := e.runIn(e.cwd, "", []string{"TASKS_OUTPUT="}, "show", gate)
	if !strings.Contains(human.stdout, "manual:") {
		t.Fatalf("%s", human.stdout)
	}
	sg := e.ok("take", gate)
	e.ok("finish", sg["token"].(string))
	sb := e.ok("take")
	if sb["task_id"] != b {
		t.Fatalf("%v", sb)
	}
	e.ok("finish", sb["token"].(string))
	if e.ok("status")["done"] != true {
		t.Fatal("not done")
	}
}
