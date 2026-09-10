//go:build unix

package git

import (
	"bytes"
	"os"
	"syscall"
	"testing"
)

func TestWriteConflictEntryAppliesModesUnderRestrictiveUmask(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	for _, test := range []struct {
		path string
		mode string
		want os.FileMode
	}{
		{path: "umask-644", mode: "100644", want: 0o644},
		{path: "umask-755", mode: "100755", want: 0o755},
	} {
		if err := writeConflictEntry(root, test.path, test.mode, bytes.NewBufferString("mode")); err != nil {
			t.Fatal(err)
		}
		info, err := root.Lstat(test.path)
		if err != nil || info.Mode().Perm() != test.want {
			t.Fatalf("%s mode = %v, %v; want %v", test.path, info.Mode(), err, test.want)
		}
	}
}
