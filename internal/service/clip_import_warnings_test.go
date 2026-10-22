package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

func TestClipImportWarningsClaimedBeforeEffectsAndUsedForReceipt(t *testing.T) {
	for _, images := range []bool{false, true} {
		for _, warnings := range [][]string{{}, {"Z denied", "A unavailable", "Z denied", "Внешнее изображение"}} {
			name := "text"
			if images {
				name = "images"
			}
			if len(warnings) == 0 {
				name += "/empty"
			} else {
				name += "/ordered duplicates"
			}
			t.Run(name, func(t *testing.T) {
				f, staged := stagedPublicationFixture(t)
				if !images {
					staged.Manifest.Assets = []model.ClipperAsset{}
				}
				staged.Manifest.ExternalImages = []model.ClipperExternalImage{}
				for _, reason := range warnings {
					staged.Manifest.ExternalImages = append(staged.Manifest.ExternalImages, model.ClipperExternalImage{URL: "https://example.test/image.png", Reason: reason})
				}
				f.service.store = &hookedClipImportStore{ClipImportStore: f.store, claim: func(record repository.ClipImportRecord) error {
					var plan map[string]json.RawMessage
					if err := json.Unmarshal(record.Journal.Plan, &plan); err != nil {
						t.Fatal(err)
					}
					var saved []string
					if raw, ok := plan["warnings"]; !ok || string(raw) == "null" {
						t.Errorf("immutable plan must distinguish explicit warnings from legacy absence: %s", record.Journal.Plan)
					} else if err := json.Unmarshal(raw, &saved); err != nil || !reflect.DeepEqual(saved, warnings) {
						t.Errorf("claim warnings = %q, want %q (%v)", saved, warnings, err)
					}
					if _, err := os.Stat(filepath.Join(f.target, staged.Manifest.Filename)); !os.IsNotExist(err) {
						t.Fatal("note effect before claim", err)
					}
					if images {
						if _, err := os.Stat(filepath.Join(f.target, "assets")); !os.IsNotExist(err) {
							t.Fatal("asset effect before claim", err)
						}
					}
					// Mutation after serialization must not change the receipt's metadata.
					if len(staged.Manifest.ExternalImages) != 0 {
						staged.Manifest.ExternalImages[0].Reason = "changed after claim"
					}
					return nil
				}}
				status, _, err := f.service.ImportStaged(t.Context(), staged)
				if err != nil || status.Result == nil || !reflect.DeepEqual(status.Result.Warnings, warnings) {
					t.Fatalf("receipt = %+v, want warnings %q (%v)", status, warnings, err)
				}
			})
		}
	}
}

func TestClipImportReceiptWarningsLegacyPresence(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		for _, tc := range []struct {
			name, field string
			want        []string
			unknown     bool
		}{
			{name: "legacy absent", unknown: true},
			{name: "null", field: `,"warnings":null`, unknown: true},
			{name: "invalid type", field: `,"warnings":"unknown"`, unknown: true},
			{name: "known empty", field: `,"warnings":[]`, want: []string{}},
			{name: "ordered duplicates", field: `,"warnings":["Z","A","Z"]`, want: []string{"Z", "A", "Z"}},
		} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				raw := json.RawMessage(`{"version":` + version + tc.field + `}`)
				got, err := clipImportReceiptWarnings(raw)
				if tc.unknown {
					if err == nil || got != nil {
						t.Fatalf("unknown legacy metadata became receipt warnings: %q %v", got, err)
					}
					return
				}
				if err != nil || !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("warnings = %q %v, want %q", got, err, tc.want)
				}
			})
		}
	}
}
