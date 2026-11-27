//go:build windows

package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
)

func privateRuntimePath(_ fs.FileInfo, _ bool) error {
	return fmt.Errorf("runtime registry ownership validation is unavailable on Windows")
}

func openRuntimeRegistration(_ *os.Root, _ string) (*os.File, error) {
	return nil, fmt.Errorf("private runtime registry opening is unavailable on Windows")
}

func runtimeParent(_ context.Context, _ int) (int, error) {
	return 0, fmt.Errorf("runtime ancestry validation is unavailable on Windows")
}
