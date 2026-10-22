package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStorageProspectiveNativeAliases(t *testing.T) {
	root := t.TempDir()
	identity, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	left, right := filepath.Join(root, "native"), filepath.Join(root, "alias")
	// These two lexical roots model native aliases that EvalSymlinks does not
	// unify (e.g. Windows subst). Their children do not exist at all.
	stat := func(path string) (os.FileInfo, error) {
		if path == left || path == right {
			return identity, nil
		}
		return nil, os.ErrNotExist
	}
	for _, tt := range []struct {
		name, parent, child string
		want                bool
	}{
		{"same missing tail", filepath.Join(left, "staging"), filepath.Join(right, "staging"), true},
		{"missing ancestor", filepath.Join(left, "staging"), filepath.Join(right, "staging", "upload"), true},
		{"missing descendant", filepath.Join(left, "staging", "upload"), filepath.Join(right, "staging"), false},
		{"missing sibling", filepath.Join(left, "staging"), filepath.Join(right, "staging-other"), false},
		{"different missing tails", filepath.Join(left, "staging", "a"), filepath.Join(right, "staging", "b"), false},
		{"existing alias ancestor", left, filepath.Join(right, "staging", "upload"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := privatePathContainsWithStat(tt.parent, tt.child, stat); got != tt.want {
				t.Fatalf("contains = %v, want %v", got, tt.want)
			}
		})
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("containment created filesystem entries")
	}
}
