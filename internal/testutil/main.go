// Package testutil contains shared test fixture setup.
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Run uses a canonical temporary parent so t.TempDir fixtures can be used as
// canonical repository snapshots on macOS and Windows as well as Linux.
// Tests of aliases still create their own explicit symlinks beneath this parent.
func Run(m *testing.M) int {
	parent, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "canonical test temporary directory:", err)
		return 1
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(key, parent); err != nil {
			fmt.Fprintln(os.Stderr, "configure test temporary directory:", err)
			return 1
		}
	}
	return m.Run()
}
