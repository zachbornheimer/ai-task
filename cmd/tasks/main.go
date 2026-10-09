// Command tasks is the CLI for the task engine. See `tasks help`.
package main

import (
	"os"

	"github.com/zachbornheimer/ai-task/internal/cli"
)

func main() {
	os.Exit(cli.Main(cli.Env{Args: os.Args[1:], Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}))
}
