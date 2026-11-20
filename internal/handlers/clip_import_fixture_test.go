package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

func TestClipImportTextFixture(t *testing.T) {
	file, err := os.Open("../../testdata/clipper/text-import.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	m, err := service.DecodeClipperManifest(file)
	if err != nil {
		t.Fatalf("public fixture decoder: %v", err)
	}
	f := newImportHTTPFixture(t)
	// Exercise publication's index update as well as the on-disk target. Only
	// the process-local destination handle in the manifest needs replacement.
	if _, err := f.settings.SwitchBase(m.Base); err != nil {
		t.Fatal(err)
	}
	destination, err := f.dest.Folders(m.Base)
	if err != nil {
		t.Fatal(err)
	}
	m.DestinationRevision = destination.DestinationRevision
	body, ct := importMultipart(t, m)
	created := importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
	if created.Code != 201 {
		t.Fatalf("new fixture import: %d %s", created.Code, created.Body)
	}
	replay := importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
	if replay.Code != 200 || !bytes.Equal(replay.Body.Bytes(), created.Body.Bytes()) {
		t.Fatalf("fixture replay: %d %s; original %s", replay.Code, replay.Body, created.Body)
	}
	var result model.ClipperImportResult
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(m.Markdown))
	want := model.ClipperImportResult{ImportID: m.ImportID, Base: m.Base, Path: m.Filename, Revision: "sha256:" + hex.EncodeToString(hash[:]), Warnings: []string{}}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("receipt: %#v, want %#v", result, want)
	}
	note, err := f.notes.GetNote(result.Path)
	if err != nil || note.Content != m.Markdown || note.Revision != result.Revision {
		t.Fatalf("note/receipt revision mismatch: note=%#v receipt=%#v err=%v", note, result, err)
	}
	content, err := os.ReadFile(filepath.Join(f.target, m.Filename))
	if err != nil || string(content) != m.Markdown {
		t.Fatalf("fixture content: %q %v", content, err)
	}
	entries, err := os.ReadDir(f.target)
	if err != nil || len(entries) != 1 || entries[0].Name() != m.Filename {
		t.Fatalf("extra publication: %v %v", entries, err)
	}
	nodes, err := repository.NewNoteRepository(f.db).GetAllNodes()
	if err != nil || len(nodes) != 1 || nodes[0].Path != m.Filename || nodes[0].Type != "file" {
		t.Fatalf("fixture index: %#v %v", nodes, err)
	}
	status := importRequest(f.router, "GET", importsPath+"?id="+m.ImportID, f.created.Secret, "", nil)
	wantStatus, err := json.Marshal(model.ClipperImportStatusResponse{ImportID: m.ImportID, State: model.ClipperImportSucceeded, Result: &want})
	if err != nil {
		t.Fatal(err)
	}
	if status.Code != 200 || status.Body.String() != string(wantStatus)+"\n" {
		t.Fatalf("fixture status: %d %s, want %s", status.Code, status.Body, wantStatus)
	}
	for _, response := range []http.Header{created.Header(), replay.Header(), status.Header()} {
		if response.Get("Cache-Control") != "no-store" || response.Get("Content-Type") != "application/json" {
			t.Fatalf("response headers: %v", response)
		}
	}
}
