package service

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func TestClipperPortablePathFixtures(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clipper/portable-paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name          string `json:"name"`
		Filename      string `json:"filename"`
		Parent        string `json:"parent"`
		ManifestValid bool   `json:"manifestValid"`
		VisibleParent bool   `json:"visibleParent"`
		Proposal      *struct {
			Title    string `json:"title"`
			Filename string `json:"filename"`
		} `json:"proposal"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 9 {
		t.Fatal("missing shared path cases")
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			m := validClipManifest()
			m.Filename, m.Parent = f.Filename, f.Parent
			err := ValidateClipperManifest(m)
			if f.ManifestValid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireClipError(t, err, model.ClipperInvalidRequest)
			}
			visible := true
			if f.Parent != "" {
				for _, component := range strings.Split(f.Parent, "/") {
					visible = visible && noteDirectoryVisible(component)
				}
			}
			if visible != f.VisibleParent {
				t.Fatalf("parent visibility = %v, want %v", visible, f.VisibleParent)
			}
			if f.Proposal != nil {
				m.Filename, m.Parent = f.Proposal.Filename, ""
				if err := ValidateClipperManifest(m); err != nil {
					t.Fatalf("proposed filename: %v", err)
				}
			}
		})
	}
}
