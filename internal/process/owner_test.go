package process

import (
	"os"
	"os/exec"
	"testing"
)

func TestOwnerOfLiveProcessIsNotGone(t *testing.T) {
	if Self().Gone() {
		t.Fatal("the running process reported gone")
	}
}

func TestOwnerOfExitedProcessIsGone(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o := Owner{PID: cmd.Process.Pid, Host: Self().Host}
	o.Start, _ = startTime(o.PID)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if !o.Gone() {
		t.Fatalf("exited process %d not reported gone", o.PID)
	}
}

func TestOwnerWithReusedPidIsGone(t *testing.T) {
	self := Self()
	if self.Start == "" {
		t.Skip("platform reports no process start time")
	}
	self.Start += "x"
	if !self.Gone() {
		t.Fatal("a pid whose start time differs from the recorded one was not reported gone")
	}
}

func TestOwnerOfUnknownHostOrPidIsNeverGone(t *testing.T) {
	self := Self()
	other := Owner{PID: 1 << 22, Host: self.Host + "-elsewhere"}
	if other.Gone() {
		t.Fatal("an owner on another host was judged by this host's pids")
	}
	if (Owner{Host: self.Host}).Gone() {
		t.Fatal("an owner without a pid was reported gone")
	}
}
