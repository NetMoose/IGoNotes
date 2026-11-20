package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

func imageImportWire(t *testing.T, m model.ClipperManifest, data []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	p, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="manifest"`}, "Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(p).Encode(m)
	for _, a := range m.Assets {
		p, err = w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"`, a.ID)}, "Content-Type": {a.MediaType}})
		if err != nil {
			t.Fatal(err)
		}
		p.Write(data)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), w.FormDataContentType()
}

func TestClipImagesHTTPFourFormatsHistoricalReplay(t *testing.T) {
	images := map[string][]byte{}
	pixel := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var b bytes.Buffer
	png.Encode(&b, pixel)
	images["png"] = append([]byte{}, b.Bytes()...)
	b.Reset()
	jpeg.Encode(&b, pixel, nil)
	images["jpg"] = append([]byte{}, b.Bytes()...)
	b.Reset()
	gif.Encode(&b, pixel, nil)
	images["gif"] = b.Bytes()
	images["webp"], _ = base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	for ext, data := range images {
		for _, base := range []string{"active", "target"} {
			t.Run(ext+"/"+base, func(t *testing.T) {
				f := newImportHTTPFixture(t)
				beforeIndex, err := repository.NewNoteRepository(f.db).GetAllNodes()
				if err != nil {
					t.Fatal(err)
				}
				m := f.manifest(t, base)
				m.Parent = "research/articles"
				root := f.target
				if base == "active" {
					root = f.active
				}
				if err := os.MkdirAll(filepath.Join(root, m.Parent), 0700); err != nil {
					t.Fatal(err)
				}
				media := "image/" + ext
				if ext == "jpg" {
					media = "image/jpeg"
				}
				hash := sha256.Sum256(data)
				digest := hex.EncodeToString(hash[:])
				m.Assets = []model.ClipperAsset{{ID: "a", MediaType: media, SHA256: digest, Size: int64(len(data))}, {ID: "b", MediaType: media, SHA256: digest, Size: int64(len(data))}}
				name := "clip-" + m.ImportID + "-" + digest + "." + ext
				m.Markdown = "![a](../../assets/images/" + name + ")\n![b](../../assets/images/" + name + ")\n"
				wire, contentType := imageImportWire(t, m, data)
				response := importRequest(f.router, "POST", importsPath, f.created.Secret, contentType, bytes.NewReader(wire))
				if response.Code != 201 {
					t.Fatalf("import %d %s", response.Code, response.Body.String())
				}
				receipt := response.Body.String()
				if base == "target" {
					afterIndex, err := repository.NewNoteRepository(f.db).GetAllNodes()
					if err != nil {
						t.Fatal(err)
					}
					before, _ := json.Marshal(beforeIndex)
					after, _ := json.Marshal(afterIndex)
					if !bytes.Equal(before, after) {
						t.Fatal("inactive import changed active global index")
					}
				}
				actual, err := os.ReadFile(filepath.Join(root, "assets/images", name))
				if err != nil || !bytes.Equal(actual, data) {
					t.Fatal("image bytes", err)
				}
				note, err := os.ReadFile(filepath.Join(root, m.Parent, m.Filename))
				if err != nil || string(note) != m.Markdown {
					t.Fatal("note bytes", err)
				}
				entries, err := os.ReadDir(filepath.Join(root, "assets/images"))
				if err != nil || len(entries) != 1 {
					t.Fatal("dedup", entries, err)
				}
				entries, err = os.ReadDir(f.staging)
				if err != nil || len(entries) != 0 {
					t.Fatal("request/owned staging leak", entries, err)
				}
				var result model.ClipperImportResult
				json.Unmarshal(response.Body.Bytes(), &result)
				got := httptest.NewRecorder()
				f.router.ServeHTTP(got, newLocalRouterRequest(http.MethodGet, "/api/note?id="+m.Parent+"/"+m.Filename, nil))
				if base == "active" {
					var noteResult struct {
						Revision string `json:"revision"`
					}
					json.Unmarshal(got.Body.Bytes(), &noteResult)
					if got.Code != 200 || noteResult.Revision != result.Revision {
						t.Fatalf("revision %d %s %s", got.Code, got.Body.String(), result.Revision)
					}
				}
				if err = f.connections.Revoke(t.Context(), f.created.Connection.ID); err != nil {
					t.Fatal(err)
				}
				rotated, err := f.connections.Create(t.Context(), "rotated")
				if err != nil {
					t.Fatal(err)
				}
				f.notes.Close()
				f.db.Close()
				f.start(t)
				response = importRequest(f.router, "POST", importsPath, rotated.Secret, contentType, bytes.NewReader(wire))
				if response.Code != 200 || response.Body.String() != receipt {
					t.Fatalf("historical replay %d %s", response.Code, response.Body.String())
				}
				entries, err = os.ReadDir(f.staging)
				if err != nil || len(entries) != 0 {
					t.Fatal("replay copied staging", entries, err)
				}
			})
		}
	}
}

type pendingImageStore struct {
	service.ClipImportStore
	published    int
	fail         bool
	noteBoundary int
}

func (s *pendingImageStore) Checkpoint(ctx context.Context, id string, v repository.ClipImportVersion, raw json.RawMessage) error {
	var cp struct {
		Step string `json:"step"`
	}
	json.Unmarshal(raw, &cp)
	if cp.Step == "published" {
		s.published++
		boundary := s.noteBoundary
		if boundary == 0 {
			boundary = 2
		}
		if s.published == boundary {
			s.fail = true
		}
	}
	if s.fail {
		return errors.New("lost checkpoint acknowledgement")
	}
	return s.ClipImportStore.Checkpoint(ctx, id, v, raw)
}

func TestClipImportWarningsDurableAfterNotePublicationAndHandlerCleanup(t *testing.T) {
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
				f := newImportHTTPFixture(t)
				m := f.manifest(t, "target")
				m.Parent = "research/articles"
				if err := os.MkdirAll(filepath.Join(f.target, m.Parent), 0700); err != nil {
					t.Fatal(err)
				}
				m.ExternalImages = []model.ClipperExternalImage{}
				for _, reason := range warnings {
					m.ExternalImages = append(m.ExternalImages, model.ClipperExternalImage{URL: "https://example.test/external.png", Reason: reason})
				}
				var b bytes.Buffer
				if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
					t.Fatal(err)
				}
				data := b.Bytes()
				imageHash := sha256.Sum256(data)
				boundary := 1
				version := 1
				if images {
					boundary = 2
					version = 2
					m.Assets = []model.ClipperAsset{{ID: "a", MediaType: "image/png", Size: int64(len(data)), SHA256: hex.EncodeToString(imageHash[:])}}
					m.Markdown = "![local](../../assets/images/clip-" + m.ImportID + "-" + m.Assets[0].SHA256 + ".png)\n"
				}
				// Compare the reconstructed durable result to an ordinary HTTP success.
				normal := newImportHTTPFixture(t)
				normalManifest := m
				normalManifest.DestinationRevision = normal.manifest(t, "target").DestinationRevision
				if err := os.MkdirAll(filepath.Join(normal.target, m.Parent), 0700); err != nil {
					t.Fatal(err)
				}
				wire, media := imageImportWire(t, normalManifest, data)
				success := importRequest(normal.router, "POST", importsPath, normal.created.Secret, media, bytes.NewReader(wire))
				if success.Code != 201 {
					t.Fatalf("normal success %d %s", success.Code, success.Body.String())
				}
				var normalResult model.ClipperImportResult
				if err := json.Unmarshal(success.Body.Bytes(), &normalResult); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(normalResult.Warnings, warnings) {
					t.Fatalf("normal warnings = %q, want %q", normalResult.Warnings, warnings)
				}
				// Simulate a historical terminal row predating the additive field.
				// Its receipt remains authoritative; replay must never backfill it.
				if _, err := normal.db.Exec(`UPDATE clip_imports SET journal_json=json_remove(journal_json,'$.plan.warnings') WHERE import_id=?`, m.ImportID); err != nil {
					t.Fatal(err)
				}
				normal.notes.Close()
				if err := normal.db.Close(); err != nil {
					t.Fatal(err)
				}
				normal.start(t)
				replayed := importRequest(normal.router, "POST", importsPath, normal.created.Secret, media, bytes.NewReader(wire))
				if replayed.Code != 200 || replayed.Body.String() != success.Body.String() {
					t.Fatalf("legacy terminal receipt changed: %d %s", replayed.Code, replayed.Body.String())
				}
				legacy, found, err := repository.NewClipImportRepository(normal.db).Get(t.Context(), m.ImportID)
				if err != nil || !found {
					t.Fatal(err)
				}
				var legacyPlan map[string]json.RawMessage
				if err := json.Unmarshal(legacy.Journal.Plan, &legacyPlan); err != nil {
					t.Fatal(err)
				}
				if _, present := legacyPlan["warnings"]; present {
					t.Fatal("replay rewrote historical plan")
				}
				store := &pendingImageStore{ClipImportStore: repository.NewClipImportRepository(f.db), noteBoundary: boundary}
				importer, err := service.NewClipImportService(f.settings, f.dest, f.notes, store)
				if err != nil {
					t.Fatal(err)
				}
				h, err := NewClipImportHandler(importer, f.settings)
				if err != nil {
					t.Fatal(err)
				}
				router := http.NewServeMux()
				RegisterClipImportRoutes(router, h, f.connections, f.settings)
				wire, media = imageImportWire(t, m, data)
				response := importRequest(router, "POST", importsPath, f.created.Secret, media, bytes.NewReader(wire))
				if response.Code != 202 {
					t.Fatalf("post-publication failure %d %s", response.Code, response.Body.String())
				}
				if store.published != boundary {
					t.Fatal("did not reach note-publication boundary")
				}
				f.notes.Close()
				if err = f.db.Close(); err != nil {
					t.Fatal(err)
				}
				f.start(t)
				record, found, err := repository.NewClipImportRepository(f.db).Get(t.Context(), m.ImportID)
				if err != nil || !found {
					t.Fatal("reopened journal", err)
				}
				if record.Result != nil || record.State != repository.ClipImportPublishing {
					t.Fatal("test must reconstruct without an existing receipt", record.State)
				}
				type filePlan struct {
					Parent   string `json:"parent"`
					Filename string `json:"filename"`
					SHA256   string `json:"sha256"`
					Size     int64  `json:"size"`
				}
				var plan struct {
					Version   int       `json:"version"`
					Markdown  string    `json:"markdown"`
					Warnings  *[]string `json:"warnings"`
					Note      filePlan  `json:"note"`
					StagingID string    `json:"staging_id"`
					Assets    []struct {
						File   filePlan `json:"file"`
						Source struct {
							Name     string `json:"name"`
							Identity struct {
								Device uint64 `json:"device"`
								File   uint64 `json:"file"`
							} `json:"identity"`
						} `json:"source"`
					} `json:"assets"`
				}
				if err = json.Unmarshal(record.Journal.Plan, &plan); err != nil || plan.Version != version {
					t.Fatal("plan", err)
				}
				if plan.Warnings == nil || !reflect.DeepEqual(*plan.Warnings, warnings) {
					t.Fatalf("reopened plan warnings = %+v, want %q", plan.Warnings, warnings)
				}
				note, err := os.ReadFile(filepath.Join(f.target, plan.Note.Parent, plan.Note.Filename))
				if err != nil || string(note) != plan.Markdown || plan.Markdown != m.Markdown || int64(len(note)) != plan.Note.Size || path.Join(plan.Note.Parent, plan.Note.Filename) != record.NotePath {
					t.Fatal("incomplete durable note information", err)
				}
				noteHash := sha256.Sum256(note)
				if hex.EncodeToString(noteHash[:]) != plan.Note.SHA256 || record.NoteContentHash != plan.Note.SHA256 {
					t.Fatal("durable note hash mismatch")
				}
				entries, err := os.ReadDir(f.staging)
				if err != nil {
					t.Fatal(err)
				}
				if images {
					if len(entries) != 1 || entries[0].Name() != plan.StagingID || len(plan.Assets) != 1 {
						t.Fatal("borrowed request survived or owned source missing", entries)
					}
					a := plan.Assets[0]
					source, err := os.ReadFile(filepath.Join(f.staging, plan.StagingID, a.Source.Name))
					if err != nil || !bytes.Equal(source, data) || int64(len(source)) != a.File.Size || a.File.SHA256 != hex.EncodeToString(imageHash[:]) || (a.Source.Identity.Device == 0 && a.Source.Identity.File == 0) {
						t.Fatal("incomplete durable asset source information", err)
					}
					published, err := os.ReadFile(filepath.Join(f.target, a.File.Parent, a.File.Filename))
					if err != nil || !bytes.Equal(source, published) {
						t.Fatal("published asset differs from owned source", err)
					}
				} else if len(entries) != 0 {
					t.Fatal("text request root survived handler cleanup", entries)
				}
				rebuilt := model.ClipperImportResult{ImportID: record.ImportID, Base: record.BaseName, Path: record.NotePath, Revision: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(plan.Markdown))), Warnings: *plan.Warnings}
				if !reflect.DeepEqual(rebuilt, normalResult) {
					t.Fatalf("reconstructed = %+v, normal = %+v", rebuilt, normalResult)
				}
			})
		}
	}
}

func TestClipImagesHTTPPendingRetainsOwnedNotBorrowedSources(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	data := b.Bytes()
	hash := sha256.Sum256(data)
	m.Assets = []model.ClipperAsset{{ID: "a", MediaType: "image/png", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}}
	store := &pendingImageStore{ClipImportStore: repository.NewClipImportRepository(f.db)}
	importer, err := service.NewClipImportService(f.settings, f.dest, f.notes, store)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewClipImportHandler(importer, f.settings)
	if err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	RegisterClipImportRoutes(router, h, f.connections, f.settings)
	wire, media := imageImportWire(t, m, data)
	response := importRequest(router, "POST", importsPath, f.created.Secret, media, bytes.NewReader(wire))
	if response.Code != 202 {
		t.Fatalf("pending %d %s", response.Code, response.Body.String())
	}
	record, found, err := store.Get(t.Context(), m.ImportID)
	if err != nil || !found {
		t.Fatal(err)
	}
	var plan struct {
		Version   int    `json:"version"`
		StagingID string `json:"staging_id"`
		Assets    []struct {
			Source struct {
				Name string `json:"name"`
			} `json:"source"`
		} `json:"assets"`
	}
	if err = json.Unmarshal(record.Journal.Plan, &plan); err != nil || plan.Version != 2 || len(plan.Assets) != 1 {
		t.Fatal("plan", err)
	}
	entries, err := os.ReadDir(f.staging)
	if err != nil || len(entries) != 1 || entries[0].Name() != plan.StagingID {
		t.Fatal("borrowed request survived or durable staging missing", entries, err)
	}
	actual, err := os.ReadFile(filepath.Join(f.staging, plan.StagingID, plan.Assets[0].Source.Name))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("recovery source unreadable after handler cleanup", err)
	}
}
