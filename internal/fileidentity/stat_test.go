package fileidentity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatIdentitySurvivesPathReplacement(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "base")
	displaced := filepath.Join(parent, "original")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	captured, err := Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := Stat(displaced)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(captured, replacement) {
		t.Fatal("captured identity followed the replacement pathname")
	}
	if !os.SameFile(captured, original) {
		t.Fatal("captured identity no longer matches the original directory")
	}
}
