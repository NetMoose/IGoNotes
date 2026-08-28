//go:build windows

package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanRelativeNotePathRejectsWindowsNonLocalPaths(t *testing.T) {
	invalid := []string{
		"NUL",
		"CON",
		"PRN",
		"AUX",
		"COM1",
		"LPT1",
		"CONIN$",
		"CONOUT$",
		`notes\NUL`,
		`\notes\note.md`,
		`\\server\share\note.md`,
		`C:\notes\note.md`,
		`C:notes\note.md`,
		`..`,
		`..\outside.md`,
	}
	for _, path := range []string{"NUL.txt", "CON.md", "PRN.log", "AUX.note", "COM1.md", "LPT1.md"} {
		if !filepath.IsLocal(path) {
			invalid = append(invalid, path)
		}
	}

	for _, path := range invalid {
		t.Run(path, func(t *testing.T) {
			if filepath.IsLocal(path) {
				t.Fatalf("test path %q is local on this Windows version", path)
			}
			if _, err := cleanRelativeNotePath(path, false); !errors.Is(err, ErrInvalidNotePath) {
				t.Errorf("cleanRelativeNotePath(%q) error = %v, want ErrInvalidNotePath", path, err)
			}
		})
	}

	path := `notes\nested\note.md`
	got, err := cleanRelativeNotePath(path, false)
	if err != nil {
		t.Fatalf("cleanRelativeNotePath(%q) error = %v", path, err)
	}
	if want := `notes/nested/note.md`; got != want {
		t.Errorf("cleanRelativeNotePath(%q) = %q, want %q", path, got, want)
	}
}

func TestScanNotesUsesSlashSeparatedWindowsIDs(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "notes", "nested")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("note"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	nodes, err := scanNotesPath(t, base)
	if err != nil {
		t.Fatalf("scanNotes() error = %v", err)
	}
	found := false
	for _, node := range nodes {
		if node.ID == "notes/nested/note.md" && node.Path == node.ID && node.ParentID == "notes/nested" {
			found = true
		}
	}
	if !found {
		t.Errorf("scanNotes() nodes = %#v, want slash-normalized nested note", nodes)
	}
}
