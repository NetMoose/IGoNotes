package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

type importHTTPFixture struct {
	data, active, target, configPath, staging string
	db                                        *sql.DB
	notes                                     *service.NoteService
	settings                                  *service.SettingsService
	dest                                      *service.ClipperDestinationService
	connections                               *service.ClipperConnectionService
	created                                   model.ClipperConnectionCreatedResponse
	router                                    *http.ServeMux
}

func newImportHTTPFixture(t *testing.T) *importHTTPFixture {
	t.Helper()
	f := &importHTTPFixture{data: t.TempDir(), active: t.TempDir(), target: t.TempDir(), configPath: filepath.Join(t.TempDir(), "config.json")}
	f.staging = filepath.Join(f.data, "clipper-staging")
	complete := true
	cfg := model.Config{CurrentBase: "active", SetupCompleted: &complete, Bases: []model.Base{{Name: "active", Path: f.active}, {Name: "target", Path: f.target}}}
	if err := f.configStore(t).Save(&cfg); err != nil {
		t.Fatal(err)
	}
	f.start(t)
	var err error
	f.created, err = f.connections.Create(t.Context(), "HTTP import")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.notes.Close(); f.db.Close() })
	return f
}

func (f *importHTTPFixture) configStore(t *testing.T) *service.ConfigService {
	t.Helper()
	policy, err := service.NewPrivateStoragePolicy(filepath.Dir(f.configPath), filepath.Join(f.data, "metadata.db"), f.staging)
	if err != nil {
		t.Fatal(err)
	}
	return service.NewConfigService(f.configPath, policy)
}

func (f *importHTTPFixture) start(t *testing.T) {
	t.Helper()
	var err error
	f.db, err = repository.InitDB(filepath.Join(f.data, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := service.NewBaseOperationCoordinator()
	f.notes = service.NewNoteService(repository.NewNoteRepository(f.db), f.active, c)
	if err := f.notes.SyncFS(); err != nil {
		t.Fatal(err)
	}
	f.settings, err = service.NewSettingsService(f.configStore(t), f.notes, c, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.dest = service.NewClipperDestinationService(f.settings)
	f.connections = service.NewClipperConnectionService(repository.NewClipperConnectionRepository(f.db))
	f.router = f.routes(t, f.settings)
}

func (f *importHTTPFixture) routes(t *testing.T, state SetupState) *http.ServeMux {
	t.Helper()
	imports, err := service.NewClipImportService(f.settings, f.dest, f.notes, repository.NewClipImportRepository(f.db))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewClipImportHandler(imports, f.settings)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouter(NewNoteHandler(f.notes), NewSettingsHandler(f.settings), state, clipperTestSPA())
	RegisterClipperDiscoveryRoutes(r, NewClipperDiscoveryHandler(f.dest), f.connections, state)
	RegisterClipImportRoutes(r, h, f.connections, state)
	RegisterClipperConnectionRoutes(r, NewClipperConnectionHandler(f.connections), state)
	return r
}

func (f *importHTTPFixture) manifest(t *testing.T, base string) model.ClipperManifest {
	t.Helper()
	d, err := f.dest.Folders(base)
	if err != nil {
		t.Fatal(err)
	}
	return model.ClipperManifest{ImportID: "12345678-1234-4234-8234-123456789abc", Base: base, DestinationRevision: d.DestinationRevision, Filename: "clip.md", Markdown: "# PRIVATE_MARKDOWN\n", SourceURL: "https://example.com/article", CapturedAt: "2026-10-21T12:00:00Z", Assets: []model.ClipperAsset{}, ExternalImages: []model.ClipperExternalImage{}}
}

func importMultipart(t *testing.T, m model.ClipperManifest, parts ...string) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if len(parts) == 0 {
		parts = []string{"manifest"}
	}
	for _, name := range parts {
		p, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"`, name)}, "Content-Type": {"application/json"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(p).Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), w.FormDataContentType()
}

func importRequest(router http.Handler, method, path, token, ct string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, body)
	r.Header.Set("Origin", "chrome-extension://"+strings.Repeat("a", 32))
	r.Header.Set("Content-Type", ct)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method == "OPTIONS" {
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

const importsPath = "/api/clipper/v1/imports"

func TestClipImportHTTPReceiptLostResponseConcurrentRotationRestart(t *testing.T) {
	for _, base := range []string{"active", "target"} {
		t.Run(base, func(t *testing.T) {
			f := newImportHTTPFixture(t)
			m := f.manifest(t, base)
			body, ct := importMultipart(t, m)
			responses := make(chan *httptest.ResponseRecorder, 8)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					responses <- importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
				}()
			}
			wg.Wait()
			close(responses)
			var original string
			count := 0
			for w := range responses {
				if w.Code == http.StatusAccepted {
					assertPendingImportResponse(t, w)
					// All creators have finished. A pending duplicate must now resolve
					// to the same terminal receipt, never become a second publication.
					w = importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
					if w.Code != 200 {
						t.Fatalf("pending replay: %d %s", w.Code, w.Body)
					}
				}
				if w.Code != 200 && w.Code != 201 {
					t.Fatalf("duplicate: %d %s", w.Code, w.Body)
				}
				if w.Code == 201 {
					count++
				}
				if original == "" {
					original = w.Body.String()
				}
				if w.Body.String() != original {
					t.Fatal("replay changed receipt")
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(w.Header())
				}
			}
			if count != 1 {
				t.Fatalf("new imports=%d", count)
			}
			for _, secret := range []string{f.data, f.active, f.target, m.Markdown, f.created.Secret, "journal"} {
				if strings.Contains(original, secret) {
					t.Fatalf("private data leaked: %q", secret)
				}
			}
			root := f.active
			if base == "target" {
				root = f.target
			}
			data, err := os.ReadFile(filepath.Join(root, m.Filename))
			if err != nil || string(data) != m.Markdown {
				t.Fatalf("note: %q %v", data, err)
			}
			before, err := os.Stat(filepath.Join(root, m.Filename))
			if err != nil {
				t.Fatal(err)
			}
			if f.notes.GetBasePath() != f.active {
				t.Fatal("switched active base")
			}
			changed := m
			changed.Markdown += "changed"
			b, c := importMultipart(t, changed)
			assertDiscoveryError(t, importRequest(f.router, "POST", importsPath, f.created.Secret, c, bytes.NewReader(b)), 409, "clipper_request_changed")
			if err := f.connections.Revoke(t.Context(), f.created.Connection.ID); err != nil {
				t.Fatal(err)
			}
			rotated, err := f.connections.Create(t.Context(), "Rotated")
			if err != nil {
				t.Fatal(err)
			}
			assertDiscoveryError(t, importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body)), 401, "clipper_unauthorized")
			// Lose the original response, restart SQLite/services and use the historical
			// destination handle with a new process-local destination registry.
			if err := f.notes.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.start(t)
			w := importRequest(f.router, "POST", importsPath, rotated.Secret, ct, bytes.NewReader(body))
			if w.Code != 200 || w.Body.String() != original {
				t.Fatalf("restart replay: %d %s", w.Code, w.Body)
			}
			w = importRequest(f.router, "GET", importsPath+"?id="+m.ImportID, rotated.Secret, "", nil)
			var status model.ClipperImportStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != 200 || status.State != model.ClipperImportSucceeded {
				t.Fatalf("status: %d %s", w.Code, w.Body)
			}
			terminal, _ := json.Marshal(status.Result)
			if string(terminal) != strings.TrimSpace(original) {
				t.Fatal("GET changed terminal receipt")
			}
			after, err := os.Stat(filepath.Join(root, m.Filename))
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("replay replaced note inode", err)
			}
			replayed, err := os.ReadFile(filepath.Join(root, m.Filename))
			if err != nil || !bytes.Equal(data, replayed) {
				t.Fatal("replay changed note bytes", err)
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 1 {
				t.Fatal("duplicate files", files, err)
			}
			entries, _ := filepath.Glob(filepath.Join(f.staging, "clip-import-*"))
			if len(entries) != 0 {
				t.Fatalf("staging leaked: %v", entries)
			}
		})
	}
}

// Only the test store is intercepted: the real Claim commits before the creator
// waits, making a duplicate's early journal lookup return pending deterministically.
type importClaimBarrier struct {
	service.ClipImportStore
	claimed chan struct{}
	release chan struct{}
}

func assertPendingImportResponse(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	assertDiscoveryError(t, w, 202, "clipper_import_pending")
	var pending model.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &pending); err != nil || pending.Message != "Clipper import pending" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("malformed pending response", err)
	}
}

func (s *importClaimBarrier) Claim(ctx context.Context, r repository.ClipImportRecord) (repository.ClipImportRecord, bool, error) {
	record, created, err := s.ClipImportStore.Claim(ctx, r)
	if err == nil && created {
		close(s.claimed)
		select {
		case <-s.release:
		case <-ctx.Done():
			return record, created, ctx.Err()
		}
	}
	return record, created, err
}

func TestClipImportHTTPConcurrentPendingAfterClaimResolvesExactReceipt(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	body, ct := importMultipart(t, m)
	store := &importClaimBarrier{ClipImportStore: repository.NewClipImportRepository(f.db), claimed: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(store.release) })
	imports, err := service.NewClipImportService(f.settings, f.dest, f.notes, store)
	if err != nil {
		t.Fatal(err)
	}
	// Pin staging before the creator takes settings.mu. This test-only provider
	// allows the duplicate HTTP upload to reach lookup while Claim is held.
	staging, err := f.settings.OpenPrivateStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	staging.Close()
	h, err := NewClipImportHandler(imports, importRootProviderFunc(func() (*os.Root, error) { return os.OpenRoot(f.staging) }))
	if err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	RegisterClipImportRoutes(router, h, f.connections, discoverySetup(true))
	creator := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		creator <- importRequest(router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
	}()
	select {
	case <-store.claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("creator did not reach committed claim")
	}
	duplicate := importRequest(router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
	assertPendingImportResponse(t, duplicate)
	if files, err := os.ReadDir(f.target); err != nil || len(files) != 0 {
		t.Fatal("published before claim released", files, err)
	}
	release.Do(func() { close(store.release) })
	var created *httptest.ResponseRecorder
	select {
	case created = <-creator:
	case <-time.After(5 * time.Second):
		t.Fatal("creator did not complete")
	}
	if created.Code != 201 {
		t.Fatalf("creator: %d %s", created.Code, created.Body)
	}
	path := filepath.Join(f.target, m.Filename)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforePayload, err := os.ReadFile(path)
	if err != nil || string(beforePayload) != m.Markdown {
		t.Fatal("creator payload differs", err)
	}
	w := importRequest(router, "GET", importsPath+"?id="+m.ImportID, f.created.Secret, "", nil)
	var status model.ClipperImportStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != 200 || status.State != model.ClipperImportSucceeded {
		t.Fatal("pending did not resolve", w.Code, w.Body, err)
	}
	terminal, _ := json.Marshal(status.Result)
	if string(terminal) != strings.TrimSpace(created.Body.String()) {
		t.Fatal("GET receipt differs")
	}
	w = importRequest(router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
	if w.Code != 200 || w.Body.String() != created.Body.String() {
		t.Fatal("exact replay changed receipt", w.Code, w.Body)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("replay replaced inode", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, beforePayload) {
		t.Fatal("replay changed payload", err)
	}
	if files, err := os.ReadDir(f.target); err != nil || len(files) != 1 {
		t.Fatal("duplicate publication", files, err)
	}
}

type importUnreadBody struct{ t *testing.T }

func (b importUnreadBody) Read([]byte) (int, error) { b.t.Error("guard read body"); return 0, io.EOF }

func TestClipImportHTTPGuardsAndQuery(t *testing.T) {
	f := newImportHTTPFixture(t)
	for _, state := range []discoverySetup{false, true} {
		r := f.routes(t, state)
		assertDiscoveryError(t, importRequest(r, "POST", importsPath, "", "multipart/form-data", importUnreadBody{t}), 401, "clipper_unauthorized")
		if !state {
			assertDiscoveryError(t, importRequest(r, "POST", importsPath, f.created.Secret, "multipart/form-data", importUnreadBody{t}), 428, "setup_required")
		}
	}
	w := importRequest(f.router, "OPTIONS", importsPath, "", "", importUnreadBody{t})
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	w = importRequest(f.router, "DELETE", importsPath, f.created.Secret, "", importUnreadBody{t})
	assertDiscoveryError(t, w, 405, "method_not_allowed")
	if w.Header().Get("Allow") != "GET, POST, OPTIONS" {
		t.Fatal(w.Header())
	}
	for _, q := range []string{"", "?id=", "?id=12345678-1234-4234-8234-123456789abc", "?id=ABC", "?id=x&id=y", "?id=%zz", "?id=x;id=y", "?id=x&other=secret", "?id=" + strings.Repeat("x", 8193)} {
		assertDiscoveryError(t, importRequest(f.router, "GET", importsPath+q, f.created.Secret, "", nil), 400, "clipper_invalid_request")
	}
	for _, suffix := range []string{"/", "/child"} {
		assertDiscoveryError(t, importRequest(f.router, "GET", importsPath+suffix, f.created.Secret, "", nil), 404, "not_found")
	}
	if w := importRequest(f.router, "GET", "/api/clipper/connections", f.created.Secret, "", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	entries, _ := filepath.Glob(filepath.Join(f.staging, "clip-import-*"))
	if len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestClipImportHTTPRejectsMalformedBeforePublication(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	valid, ct := importMultipart(t, m)
	duplicate, duplicateCT := importMultipart(t, m, "manifest", "manifest")
	unknown, unknownCT := importMultipart(t, m, "manifest", "binary")
	partHeader := []byte("Content-Disposition: form-data; name=\"manifest\"")
	closing := valid[bytes.LastIndex(valid, []byte("\r\n--"))+2:]
	for _, tc := range []struct {
		name string
		data []byte
		ct   string
	}{
		{"truncated", valid[:len(valid)-8], ct}, {"epilogue", append(append([]byte{}, valid...), []byte("PRIVATE_TRAILING_GARBAGE")...), ct},
		{"duplicate", duplicate, duplicateCT}, {"unknown", unknown, unknownCT}, {"json", []byte(`{}`), "application/json"},
		{"duplicate disposition", bytes.Replace(valid, partHeader, append(append([]byte{}, partHeader...), append([]byte("\r\n"), partHeader...)...), 1), ct},
		{"wrong disposition", bytes.Replace(valid, []byte("form-data; name="), []byte("attachment; name="), 1), ct},
		{"extra disposition parameter", bytes.Replace(valid, partHeader, append(append([]byte{}, partHeader...), []byte("; unexpected=x")...), 1), ct},
		{"non JSON manifest", bytes.Replace(valid, []byte("application/json"), []byte("text/plain"), 1), ct},
		{"transfer encoding", bytes.Replace(valid, partHeader, append(append([]byte{}, partHeader...), []byte("\r\nContent-Transfer-Encoding: base64")...), 1), ct},
		{"empty", nil, ct},
		{"spoofed final epilogue", append(append(append([]byte{}, valid...), []byte("garbage\r\n")...), closing...), ct},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := importRequest(f.router, "POST", importsPath, f.created.Secret, tc.ct, bytes.NewReader(tc.data))
			assertDiscoveryError(t, w, 400, "clipper_invalid_request")
			if strings.Contains(w.Body.String(), f.data) {
				t.Fatal("path leak")
			}
		})
	}
	if entries, _ := os.ReadDir(f.target); len(entries) != 0 {
		t.Fatal("published malformed request")
	}
	if entries, _ := filepath.Glob(filepath.Join(f.staging, "clip-import-*")); len(entries) != 0 {
		t.Fatal(entries)
	}
}

type lostImportResponse struct {
	header http.Header
	code   int
}

func (w *lostImportResponse) Header() http.Header       { return w.header }
func (w *lostImportResponse) WriteHeader(code int)      { w.code = code }
func (w *lostImportResponse) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestClipImportHTTPLostResponseAndIgnoredClientFilename(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	body, ct := importMultipart(t, m)
	body = bytes.Replace(body, []byte(`name="manifest"`), []byte(`name="manifest"; filename="../../PRIVATE_CLIENT_FILENAME"`), 1)
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080"+importsPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Authorization", "Bearer "+f.created.Secret)
	lost := &lostImportResponse{header: make(http.Header)}
	f.router.ServeHTTP(lost, r)
	if lost.code != 201 {
		t.Fatalf("lost response status: %d", lost.code)
	}
	w := importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE_CLIENT_FILENAME") {
		t.Fatalf("lost response replay: %d %s", w.Code, w.Body)
	}
	entries, err := os.ReadDir(f.target)
	if err != nil || len(entries) != 1 || entries[0].Name() != m.Filename {
		t.Fatalf("duplicate publication/client filename: %v %v", entries, err)
	}
	if entries, _ := filepath.Glob(filepath.Join(f.staging, "clip-import-*")); len(entries) != 0 {
		t.Fatal(entries)
	}
}

type importCheckedReader struct {
	reader io.Reader
	check  func()
}

func (r *importCheckedReader) Read(p []byte) (int, error) {
	if r.check != nil {
		r.check()
		r.check = nil
	}
	return r.reader.Read(p)
}

type importZeroReader struct{}

func (importZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type importFailedReader struct{ err error }

func (r importFailedReader) Read([]byte) (int, error) { return 0, r.err }

func TestClipImportHTTPStagingIOFailureIsServerErrorWithoutPublication(t *testing.T) {
	f := newImportHTTPFixture(t)
	w := importRequest(f.router, "POST", importsPath, f.created.Secret, "multipart/form-data; boundary=abc", importFailedReader{fmt.Errorf("PRIVATE_IO %s", f.data)})
	assertDiscoveryError(t, w, 500, "internal_error")
	if strings.Contains(w.Body.String(), f.data) || strings.Contains(w.Body.String(), "PRIVATE_IO") {
		t.Fatal("IO details leaked")
	}
	if entries, _ := filepath.Glob(filepath.Join(f.staging, "clip-import-*")); len(entries) != 0 {
		t.Fatal(entries)
	}
	if entries, _ := os.ReadDir(f.target); len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestClipImportHTTPBoundedPrivateStagingAndCleanup(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	body, ct := importMultipart(t, m)
	check := func() {
		entries, err := filepath.Glob(filepath.Join(f.staging, "clip-import-*"))
		if err != nil || len(entries) != 1 {
			t.Fatalf("private staging: %v %v", entries, err)
		}
		for path, mode := range map[string]os.FileMode{entries[0]: 0700, filepath.Join(entries[0], "request"): 0600} {
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != mode {
				t.Fatalf("permissions %s: %v", path, info.Mode())
			}
		}
		for _, base := range []string{f.active, f.target} {
			entries, err := os.ReadDir(base)
			if err != nil || len(entries) != 0 {
				t.Fatalf("body read wrote base: %v %v", entries, err)
			}
		}
	}
	w := importRequest(f.router, "POST", importsPath, f.created.Secret, ct, &importCheckedReader{bytes.NewReader(body), check})
	if w.Code != 201 {
		t.Fatalf("private stage import: %d %s", w.Code, w.Body)
	}
	for _, payload := range []io.Reader{
		io.LimitReader(importZeroReader{}, model.ClipperMaxMultipartBytes+1),
		io.MultiReader(bytes.NewReader(body), io.LimitReader(importZeroReader{}, model.ClipperMaxMultipartBytes)),
	} {
		w = importRequest(f.router, "POST", importsPath, f.created.Secret, ct, payload)
		assertDiscoveryError(t, w, 413, "clipper_too_large")
	}
	entries, _ := filepath.Glob(filepath.Join(f.staging, "clip-import-*"))
	if len(entries) != 0 {
		t.Fatal(entries)
	}
	// JSON representation can exceed 5 MiB: enforce decoded Markdown, not the
	// byte length of the JSON part (six bytes per escaped '<' here).
	m.ImportID = "12345678-1234-4234-8234-123456789abd"
	m.Filename = "escaped.md"
	m.Markdown = strings.Repeat("<", 1<<20)
	body, ct = importMultipart(t, m)
	if len(body) <= model.ClipperMaxMarkdownBytes {
		t.Fatal("fixture must exceed raw markdown bound")
	}
	w = importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
	if w.Code != 201 {
		t.Fatalf("escaped JSON: %d %s", w.Code, w.Body)
	}
	m.ImportID = "12345678-1234-4234-8234-123456789abe"
	m.Filename = "too-large.md"
	m.Markdown = strings.Repeat("x", model.ClipperMaxMarkdownBytes+1)
	body, ct = importMultipart(t, m)
	assertDiscoveryError(t, importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body)), 413, "clipper_too_large")
	if _, err := os.Stat(filepath.Join(f.target, m.Filename)); !os.IsNotExist(err) {
		t.Fatalf("oversized Markdown published: %v", err)
	}
}

func TestClipImportHandlerRejectsInvalidDependenciesAndDefersProvider(t *testing.T) {
	f := newImportHTTPFixture(t)
	var typedNil *service.ClipImportService
	for _, importer := range []ClipImporter{nil, typedNil} {
		if _, err := NewClipImportHandler(importer, f.settings); err == nil {
			t.Fatal("accepted nil importer")
		}
	}
	var nilProvider *service.SettingsService
	for _, provider := range []PrivateStagingRootProvider{nil, nilProvider} {
		if _, err := NewClipImportHandler(importStatusStub{}, provider); err == nil {
			t.Fatal("accepted nil staging provider")
		}
	}
	calls := 0
	provider := importRootProviderFunc(func() (*os.Root, error) { calls++; return nil, errors.New("PRIVATE_STAGING_FAILURE " + f.staging) })
	h, err := NewClipImportHandler(importStatusStub{}, provider)
	if err != nil || calls != 0 {
		t.Fatalf("constructor opened staging: %d %v", calls, err)
	}
	for _, state := range []discoverySetup{false, true} {
		r := http.NewServeMux()
		RegisterClipImportRoutes(r, h, f.connections, state)
		assertDiscoveryError(t, importRequest(r, "POST", importsPath, "", "multipart/form-data; boundary=abc", importUnreadBody{t}), 401, "clipper_unauthorized")
		if !state {
			assertDiscoveryError(t, importRequest(r, "POST", importsPath, f.created.Secret, "multipart/form-data; boundary=abc", importUnreadBody{t}), 428, "setup_required")
		}
	}
	if calls != 0 {
		t.Fatalf("guards invoked staging provider %d times", calls)
	}
	if _, err := os.Stat(f.staging); !os.IsNotExist(err) {
		t.Fatalf("denied request created staging: %v", err)
	}
	r := http.NewServeMux()
	RegisterClipImportRoutes(r, h, f.connections, f.settings)
	w := importRequest(r, "POST", importsPath, f.created.Secret, "multipart/form-data; boundary=abc", importUnreadBody{t})
	assertDiscoveryError(t, w, 500, "internal_error")
	if calls != 1 || strings.Contains(w.Body.String(), f.staging) {
		t.Fatal("provider error leaked or was not called")
	}
}

type importRootProviderFunc func() (*os.Root, error)

func (f importRootProviderFunc) OpenPrivateStagingRoot() (*os.Root, error) { return f() }

func TestClipImportHTTPSlowUploadAllowsLifecycleButRejectsStagingOverlap(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	body, ct := importMultipart(t, m)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- importRequest(f.router, "POST", importsPath, f.created.Secret, ct, reader) }()
	if _, err := writer.Write(body[:len(body)/2]); err != nil {
		t.Fatal(err)
	}
	mutation := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		payload, _ := json.Marshal(model.BaseUpdateRequest{Name: "target", Path: f.staging})
		r := httptest.NewRequest("PUT", "http://127.0.0.1:8080/api/bases?name=target", bytes.NewReader(payload))
		r.Header.Set("Origin", "http://127.0.0.1:8080")
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		mutation <- w
	}()
	select {
	case w := <-mutation:
		assertDiscoveryError(t, w, 422, "invalid_base_path")
	case <-time.After(3 * time.Second):
		t.Fatal("upload retained lifecycle/coordinator lock")
	}
	select {
	case w := <-response:
		t.Fatalf("published before complete upload: %d %s", w.Code, w.Body)
	default:
	}
	if entries, _ := os.ReadDir(f.target); len(entries) != 0 {
		t.Fatal("incomplete upload wrote base")
	}
	if _, err := writer.Write(body[len(body)/2:]); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-response:
		if w.Code != 201 {
			t.Fatalf("upload: %d %s", w.Code, w.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not finish")
	}
	entries, err := os.ReadDir(f.staging)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging cleanup: %v %v", entries, err)
	}
}

func TestClipImportHTTPStagingRetargetDoesNotWriteOrCleanBase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege varies")
	}
	for _, duringUpload := range []bool{false, true} {
		t.Run(fmt.Sprintf("during-upload=%v", duringUpload), func(t *testing.T) {
			f := newImportHTTPFixture(t)
			m := f.manifest(t, "target")
			body, ct := importMultipart(t, m)
			root, err := f.settings.OpenPrivateStagingRoot()
			if err != nil {
				t.Fatal(err)
			}
			if err := root.Close(); err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(f.data, "original-staging")
			retarget := func() {
				if err := os.Rename(f.staging, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.target, f.staging); err != nil {
					t.Fatal(err)
				}
			}
			var input io.Reader = importUnreadBody{t}
			if duringUpload {
				input = &importCheckedReader{reader: bytes.NewReader(body), check: retarget}
			} else {
				retarget()
			}
			w := importRequest(f.router, "POST", importsPath, f.created.Secret, ct, input)
			if duringUpload {
				// Scoped cleanup uses the originally pinned root. Publication's policy
				// recheck rejects the new alias, without moving/erasing base contents.
				assertDiscoveryError(t, w, 500, "internal_error")
			} else {
				assertDiscoveryError(t, w, 422, "invalid_base_path")
			}
			if strings.Contains(w.Body.String(), f.data) {
				t.Fatal("private staging path leaked")
			}
			for _, path := range []string{f.target, moved} {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("retarget wrote/leaked files in %s: %v %v", path, entries, err)
				}
			}
		})
	}
}

func TestClipImportHTTPDurablePendingRejectedRecoverySnapshots(t *testing.T) {
	for _, state := range []repository.ClipImportState{repository.ClipImportPrepared, repository.ClipImportRejected, repository.ClipImportRecoveryRequired} {
		t.Run(string(state), func(t *testing.T) {
			f := newImportHTTPFixture(t)
			m := f.manifest(t, "target")
			digest, err := service.ClipperManifestDigest(m)
			if err != nil {
				t.Fatal(err)
			}
			repo := repository.NewClipImportRepository(f.db)
			now := time.Now().UTC()
			record, _, err := repo.Claim(t.Context(), repository.ClipImportRecord{ImportID: m.ImportID, RequestDigest: digest, BaseName: m.Base, BasePath: f.target, RootIdentity: "private-identity", DestinationRevision: m.DestinationRevision, NotePath: m.Filename, NoteContentHash: strings.Repeat("a", 64), Journal: repository.ClipImportJournal{Plan: json.RawMessage(`{"private":"PRIVATE_JOURNAL"}`), Checkpoint: json.RawMessage(`{}`)}, State: repository.ClipImportPrepared, CreatedAt: now, UpdatedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			want, code := 202, "clipper_import_pending"
			if state != repository.ClipImportPrepared {
				public := &model.APIError{Code: "internal_error", Message: internalErrorMessage}
				want, code = 500, "internal_error"
				if state == repository.ClipImportRecoveryRequired {
					public = &model.APIError{Code: "clipper_recovery_required", Message: "Clipper import requires recovery"}
					want, code = 409, public.Code
				}
				if err := repo.Transition(t.Context(), m.ImportID, record.Version(), state, nil, public); err != nil {
					t.Fatal(err)
				}
			}
			body, ct := importMultipart(t, m)
			w := importRequest(f.router, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
			assertDiscoveryError(t, w, want, code)
			w = importRequest(f.router, "GET", importsPath+"?id="+m.ImportID, f.created.Secret, "", nil)
			if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE_JOURNAL") || strings.Contains(w.Body.String(), f.target) {
				t.Fatalf("snapshot: %d %s", w.Code, w.Body)
			}
			entries, _ := os.ReadDir(f.target)
			if len(entries) != 0 {
				t.Fatal("receipt replay published a note")
			}
		})
	}
}

// The transport boundary must not let private IO errors become a zero success or
// a definite rejection when the durable state is still pending/uncertain.
type importStatusStub struct {
	status model.ClipperImportStatusResponse
	err    error
}

type importCountingService struct {
	ClipImporter
	imports int
}

func (s *importCountingService) Import(ctx context.Context, manifest model.ClipperManifest) (model.ClipperImportStatusResponse, bool, error) {
	s.imports++
	return s.ClipImporter.Import(ctx, manifest)
}

func TestClipImportHTTPRejectsFirstPaddedOrLFClosingBeforeImport(t *testing.T) {
	for _, padding := range []string{"", " ", "\t", " \t", strings.Repeat(" ", 8192)} {
		for _, newline := range []string{"\r\n", "\n"} {
			for _, epilogue := range []bool{false, true} {
				if padding == "" && newline == "\r\n" {
					continue
				} // normal closing covered by success/replay tests
				t.Run(fmt.Sprintf("padding=%d/newline=%q/epilogue=%v", len(padding), newline, epilogue), func(t *testing.T) {
					f := newImportHTTPFixture(t)
					m := f.manifest(t, "target")
					body, ct := importMultipart(t, m)
					if newline == "\n" {
						body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
					}
					last := bytes.LastIndex(body, []byte(newline+"--"))
					closing := bytes.TrimSuffix(body[last+len(newline):], []byte(newline))
					body = append(append([]byte{}, body[:len(body)-len(newline)]...), []byte(padding+newline)...)
					if epilogue {
						body = append(body, []byte("PRIVATE_TRAILING_GARBAGE"+newline+string(closing)+"\r\n")...)
					}
					imports, err := service.NewClipImportService(f.settings, f.dest, f.notes, repository.NewClipImportRepository(f.db))
					if err != nil {
						t.Fatal(err)
					}
					spy := &importCountingService{ClipImporter: imports}
					h, err := NewClipImportHandler(spy, f.settings)
					if err != nil {
						t.Fatal(err)
					}
					r := http.NewServeMux()
					RegisterClipImportRoutes(r, h, f.connections, f.settings)
					w := importRequest(r, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
					assertDiscoveryError(t, w, 400, "clipper_invalid_request")
					if spy.imports != 0 {
						t.Fatalf("malformed envelope reached Import %d times", spy.imports)
					}
					assertImportUnclaimedAndClean(t, f, m.ImportID)
				})
			}
		}
	}
}

func assertImportUnclaimedAndClean(t *testing.T, f *importHTTPFixture, id string) {
	t.Helper()
	_, found, err := repository.NewClipImportRepository(f.db).Get(t.Context(), id)
	if err != nil || found {
		t.Fatalf("unexpected journal claim: %v %v", found, err)
	}
	for _, dir := range []string{f.active, f.target, f.staging} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("unexpected files in %s: %v %v", dir, entries, err)
		}
	}
}

type importAuthFunc func(context.Context, string) error

func (f importAuthFunc) Authenticate(ctx context.Context, token string) error { return f(ctx, token) }

type importNoRereadBody struct {
	io.Reader
	eof     bool
	rereads int
}

func (r *importNoRereadBody) Read(p []byte) (int, error) {
	if r.eof {
		r.rereads++
		return 0, errors.New("request body read again after EOF")
	}
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}

func TestClipImportHTTPReauthorizesAfterPausedUpload(t *testing.T) {
	for _, operationalFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("operational=%v", operationalFailure), func(t *testing.T) {
			f := newImportHTTPFixture(t)
			m := f.manifest(t, "target")
			body, ct := importMultipart(t, m)
			imports, err := service.NewClipImportService(f.settings, f.dest, f.notes, repository.NewClipImportRepository(f.db))
			if err != nil {
				t.Fatal(err)
			}
			spy := &importCountingService{ClipImporter: imports}
			h, err := NewClipImportHandler(spy, f.settings)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			auth := importAuthFunc(func(ctx context.Context, token string) error {
				calls++
				if token != f.created.Secret {
					return errors.New("reauthorization used another token")
				}
				if calls == 2 && operationalFailure {
					return fmt.Errorf("PRIVATE_AUTH_FAILURE %s", f.data)
				}
				return f.connections.Authenticate(ctx, token)
			})
			router := http.NewServeMux()
			RegisterClipImportRoutes(router, h, auth, f.settings)
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			input := &importNoRereadBody{Reader: reader}
			response := make(chan *httptest.ResponseRecorder, 1)
			go func() { response <- importRequest(router, "POST", importsPath, f.created.Secret, ct, input) }()
			if _, err := writer.Write(body[:len(body)/2]); err != nil {
				t.Fatal(err)
			}
			if err := f.connections.Revoke(t.Context(), f.created.Connection.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(body[len(body)/2:]); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			select {
			case w := <-response:
				if operationalFailure {
					assertDiscoveryError(t, w, 500, "internal_error")
				} else {
					assertDiscoveryError(t, w, 401, "clipper_unauthorized")
				}
				if strings.Contains(w.Body.String(), f.data) || strings.Contains(w.Body.String(), f.created.Secret) {
					t.Fatal("auth error leaked private data")
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(w.Header())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("upload did not finish")
			}
			if calls != 2 || spy.imports != 0 {
				t.Fatalf("auth calls=%d import calls=%d", calls, spy.imports)
			}
			if !input.eof || input.rereads != 0 {
				t.Fatalf("body EOF=%v rereads=%d", input.eof, input.rereads)
			}
			assertImportUnclaimedAndClean(t, f, m.ImportID)
		})
	}
}

func TestClipImportHTTPGetAuthenticatesOnceAndBoundaryLookingMarkdown(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	m.Markdown = "# title\n--test-boundary-- \t\nbody"
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.SetBoundary("test-boundary"); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="manifest"`}, "Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(part).Encode(m); err != nil {
		t.Fatal(err)
	}
	w.Close()
	response := importRequest(f.router, "POST", importsPath, f.created.Secret, w.FormDataContentType(), bytes.NewReader(body.Bytes()))
	if response.Code != 201 {
		t.Fatalf("boundary-looking JSON text rejected: %d %s", response.Code, response.Body)
	}
	imports, err := service.NewClipImportService(f.settings, f.dest, f.notes, repository.NewClipImportRepository(f.db))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewClipImportHandler(imports, f.settings)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	auth := importAuthFunc(func(ctx context.Context, token string) error { calls++; return f.connections.Authenticate(ctx, token) })
	r := http.NewServeMux()
	RegisterClipImportRoutes(r, h, auth, f.settings)
	response = importRequest(r, "GET", importsPath+"?id="+m.ImportID, f.created.Secret, "", nil)
	if response.Code != 200 || calls != 1 {
		t.Fatalf("GET: status=%d auth calls=%d", response.Code, calls)
	}
}

func (s importStatusStub) Import(context.Context, model.ClipperManifest) (model.ClipperImportStatusResponse, bool, error) {
	return s.status, true, s.err
}
func (s importStatusStub) Status(context.Context, string) (model.ClipperImportStatusResponse, error) {
	return s.status, s.err
}

func TestClipImportHTTPPostStateMapping(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	body, ct := importMultipart(t, m)
	for _, tc := range []struct {
		state  model.ClipperImportState
		public *model.APIError
		err    error
		want   int
		code   string
	}{
		{model.ClipperImportPending, nil, nil, 202, "clipper_import_pending"},
		{model.ClipperImportRejected, &model.APIError{Code: "note_conflict", Message: "Note already exists"}, nil, 409, "note_conflict"},
		{model.ClipperImportRejected, &model.APIError{Code: "internal_error", Message: internalErrorMessage}, nil, 500, "internal_error"},
		{model.ClipperImportRecoveryRequired, &model.APIError{Code: "clipper_recovery_required", Message: "Clipper import requires recovery"}, nil, 409, "clipper_recovery_required"},
		{"", nil, fmt.Errorf("PRIVATE_IO_PATH %s", f.data), 500, "internal_error"},
	} {
		h, err := NewClipImportHandler(importStatusStub{model.ClipperImportStatusResponse{ImportID: m.ImportID, State: tc.state, Error: tc.public}, tc.err}, f.settings)
		if err != nil {
			t.Fatal(err)
		}
		r := http.NewServeMux()
		RegisterClipImportRoutes(r, h, f.connections, f.settings)
		w := importRequest(r, "POST", importsPath, f.created.Secret, ct, bytes.NewReader(body))
		assertDiscoveryError(t, w, tc.want, tc.code)
		if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), f.data) {
			t.Fatal(w.Header(), w.Body)
		}
		if tc.err == nil {
			w = importRequest(r, "GET", importsPath+"?id="+m.ImportID, f.created.Secret, "", nil)
			if w.Code != 200 {
				t.Fatalf("GET snapshot: %d %s", w.Code, w.Body)
			}
		}
	}
}
