package app

import (
	"os"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/testgit"
)

func TestMain(m *testing.M) { os.Exit(testgit.IsolateMain(m)) }
