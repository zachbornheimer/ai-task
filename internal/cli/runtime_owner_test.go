//go:build !windows

package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const runtimeOwnerHelperEnv = "ATASK_RUNTIME_OWNER_TEST_ROLE"

func TestRuntimeRegistryOwnerDeath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root, _, _ := runtimeFixture(t)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeRegistryOwnerProcess$")
	cmd.Env = append(os.Environ(), runtimeOwnerHelperEnv+"=owner", runtimeRootEnv+"="+root, runtimeSessionEnv+"="+testRuntimeSession)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(output)
	ready := runtimeOwnerReadLine(t, ctx, reader)
	fields := strings.Fields(ready)
	if len(fields) != 2 || fields[0] != "READY" {
		t.Fatalf("owner child did not validate initial registration: %q", ready)
	}
	childPID, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child, err := os.FindProcess(childPID); err == nil {
			_ = child.Kill()
		}
	}()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	if _, err := fmt.Fprintln(input, "owner-dead"); err != nil {
		t.Fatal(err)
	}
	if result := runtimeOwnerReadLine(t, ctx, reader); result != "REJECTED" {
		t.Fatalf("orphaned CLI retained owner authority: %q", result)
	}
}

func runtimeOwnerReadLine(t *testing.T, ctx context.Context, reader *bufio.Reader) string {
	t.Helper()
	result := make(chan string, 1)
	go func() { line, _ := reader.ReadString('\n'); result <- strings.TrimSpace(line) }()
	select {
	case line := <-result:
		return line
	case <-ctx.Done():
		t.Fatal("timed out waiting for runtime owner test process")
		return ""
	}
}

func TestRuntimeRegistryOwnerProcess(t *testing.T) {
	switch os.Getenv(runtimeOwnerHelperEnv) {
	case "owner":
		root := os.Getenv(runtimeRootEnv)
		_, fixturePath, registration := runtimeFixture(t)
		registration.PID = os.Getpid()
		registration.Session = testRuntimeSession
		writeRuntimeFixture(t, filepath.Join(root, filepath.Base(fixturePath)), registration)
		child := exec.Command(os.Args[0], "-test.run=^TestRuntimeRegistryOwnerProcess$")
		child.Env = append(os.Environ(), runtimeOwnerHelperEnv+"=child")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		_ = child.Wait()
	case "child":
		if owner, err := lookupRuntime(context.Background(), os.Getenv); err != nil || owner == nil {
			fmt.Println("INITIAL-LOOKUP-FAILED")
			os.Exit(1)
		}
		fmt.Printf("READY %d\n", os.Getpid())
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			os.Exit(1)
		}
		if owner, err := lookupRuntime(context.Background(), os.Getenv); err == nil || owner != nil {
			fmt.Println("AUTHORIZED")
			os.Exit(1)
		}
		fmt.Println("REJECTED")
		os.Exit(0)
	}
}
