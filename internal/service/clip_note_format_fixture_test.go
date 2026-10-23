package service

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"IGoNotes/internal/model"
)

// The JS suite checks these same bytes with an independent YAML 1.2 parser.
// Go validates the actual v1 envelope, not an invented wire title field.
func TestClipperNoteFormatFixtures(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clipper/note-format.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name  string `json:"name"`
		Input struct {
			Title      string `json:"title"`
			SourceURL  string `json:"sourceUrl"`
			CapturedAt string `json:"capturedAt"`
			Markdown   string `json:"markdown"`
		} `json:"input"`
		ExpectedFilename string `json:"expectedFilename"`
		FullMarkdown     string `json:"fullMarkdown"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no note format fixtures")
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			m := validClipManifest()
			m.Filename, m.Markdown = fixture.ExpectedFilename, fixture.FullMarkdown
			m.SourceURL, m.CapturedAt = fixture.Input.SourceURL, fixture.Input.CapturedAt
			if len(m.Filename) > 255 || !utf8.ValidString(m.Filename) {
				t.Fatal("filename is not a bounded UTF-8 component")
			}
			if err := ValidateClipperManifest(m); err != nil {
				t.Fatal(err)
			}
			if fixture.Name == "truncated-windows-device" {
				unsafe := m
				unsafe.Filename = "CON.md" // the old JS output must fail actual server validation
				requireClipError(t, ValidateClipperManifest(unsafe), model.ClipperInvalidRequest)
			}
			prefix, body, ok := strings.Cut(m.Markdown, "\n---\n\n")
			lines := strings.Split(prefix, "\n")
			if !ok || body != fixture.Input.Markdown || len(lines) != 4 || lines[0] != "---" ||
				!strings.HasPrefix(lines[1], `title: "`) || !strings.HasPrefix(lines[2], `source: "`) || !strings.HasPrefix(lines[3], `clipped_at: "`) {
				t.Fatal("unexpected raw prefix or modified body bytes")
			}
			wire, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeClipperManifest(bytes.NewReader(wire))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Markdown != fixture.FullMarkdown || decoded.Filename != fixture.ExpectedFilename || decoded.SourceURL != fixture.Input.SourceURL || decoded.CapturedAt != fixture.Input.CapturedAt {
				t.Fatal("wire decoder changed exact fixture content")
			}
		})
	}
}
