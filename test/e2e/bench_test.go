package e2e

import (
	"os/exec"
	"testing"
)

// BenchmarkCLIStartup measures a full process: start, open the database,
// run migrations check, answer a trivial query, exit.
func BenchmarkCLIStartup(b *testing.B) {
	e := newEnv(&testing.T{})
	cmd := exec.Command(bin, "init", "--name", "b")
	cmd.Dir, cmd.Env = e.cwd, e.env
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("%v %s", err, out)
	}
	b.Run("version", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			c := exec.Command(bin, "version")
			c.Dir, c.Env = e.cwd, e.env
			if err := c.Run(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("list", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			c := exec.Command(bin, "list")
			c.Dir, c.Env = e.cwd, e.env
			if err := c.Run(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
