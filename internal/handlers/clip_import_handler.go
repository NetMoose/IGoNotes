package handlers

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

// ClipImporter is the text-only publication boundary.
type ClipImporter interface {
	Import(context.Context, model.ClipperManifest) (model.ClipperImportStatusResponse, bool, error)
	Status(context.Context, string) (model.ClipperImportStatusResponse, error)
}

// StagedClipImporter optionally consumes validated assets while their private
// request root is alive. Returning does NOT retain or transfer staging ownership.
// An implementation must durably own every byte before reporting success.
type StagedClipImporter interface {
	ImportStaged(context.Context, *service.StagedClip) (model.ClipperImportStatusResponse, bool, error)
}

type ClipImportHandler struct {
	imports ClipImporter
	staging PrivateStagingRootProvider
}

// PrivateStagingRootProvider validates the configured private-storage policy and
// returns a pinned root outside every base, without retaining lifecycle locks.
// The caller owns the handle and uses only root-relative IO.
type PrivateStagingRootProvider interface {
	OpenPrivateStagingRoot() (*os.Root, error)
}

// Construction does not open/create staging. Only authenticated, setup-complete
// POST requests may call the provider, after checking the multipart envelope type.
func NewClipImportHandler(imports ClipImporter, staging PrivateStagingRootProvider) (*ClipImportHandler, error) {
	if imports == nil || (reflect.ValueOf(imports).Kind() == reflect.Pointer && reflect.ValueOf(imports).IsNil()) {
		return nil, errors.New("Clipper importer is required")
	}
	if staging == nil || ((reflect.ValueOf(staging).Kind() == reflect.Pointer || reflect.ValueOf(staging).Kind() == reflect.Func) && reflect.ValueOf(staging).IsNil()) {
		return nil, errors.New("Clipper staging provider is required")
	}
	return &ClipImportHandler{imports: imports, staging: staging}, nil
}

func RegisterClipImportRoutes(mux *http.ServeMux, handler *ClipImportHandler, auth ClipperAuthenticator, state SetupState) {
	guarded := requireClipper(auth, func(w http.ResponseWriter, r *http.Request, token string) {
		RequireSetup(state, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.serveHTTP(w, r, func() error { return auth.Authenticate(r.Context(), token) })
		})).ServeHTTP(w, r)
	}, http.MethodGet, http.MethodPost)
	// Outer wrapper includes auth/setup/method failures as well as public receipts.
	mux.Handle("/api/clipper/v1/imports", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		guarded.ServeHTTP(w, r)
	}))
}

var clipImportID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Only RegisterClipImportRoutes exposes this handler, supplying the authenticated
// request's authority recheck. Direct, unguarded HTTP use is not supported.
func (h *ClipImportHandler) serveHTTP(w http.ResponseWriter, r *http.Request, reauthorize func() error) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		h.status(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}
	var status model.ClipperImportStatusResponse
	var replay bool
	_, err := h.stageClip(w, r, func(payload *service.StagedClip) error {
		if err := reauthorize(); err != nil {
			return err
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		var err error
		if importer, ok := h.imports.(StagedClipImporter); ok {
			status, replay, err = importer.ImportStaged(r.Context(), payload)
		} else {
			// Never silently drop attachments at the legacy text boundary.
			if len(payload.Assets) != 0 {
				return invalidClipImport()
			}
			status, replay, err = h.imports.Import(r.Context(), payload.Manifest)
		}
		return err
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	switch status.State {
	case model.ClipperImportSucceeded:
		if status.Result == nil {
			writeServiceError(w, errors.New("missing Clipper receipt"))
			return
		}
		code := http.StatusCreated
		if replay {
			code = http.StatusOK
		}
		writeClipImportJSON(w, code, status.Result)
	case model.ClipperImportPending:
		writeServiceError(w, &model.ClipperError{Code: model.ClipperPending})
	case model.ClipperImportRecoveryRequired:
		writeServiceError(w, &model.ClipperError{Code: model.ClipperRecoveryRequired})
	case model.ClipperImportRejected:
		// The journal holds a safe APIError. Map its original public category,
		// never a zero result or private IO diagnostics. Unknown codes fail closed.
		if status.Error != nil && status.Error.Code == "git_conflict_pending" {
			writeServiceError(w, service.ErrGitConflictPending)
		} else if status.Error != nil {
			writeServiceError(w, &model.ClipperError{Code: model.ClipperErrorCode(status.Error.Code)})
		} else {
			writeServiceError(w, errors.New("missing Clipper rejection"))
		}
	default:
		writeServiceError(w, errors.New("invalid Clipper import state"))
	}
}

func (h *ClipImportHandler) status(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.RawQuery) > 8192 {
		writeServiceError(w, invalidClipImport())
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	ids := query["id"]
	if err != nil || len(query) != 1 || len(ids) != 1 || !clipImportID.MatchString(ids[0]) {
		writeServiceError(w, invalidClipImport())
		return
	}
	status, err := h.imports.Status(r.Context(), ids[0])
	if errors.Is(err, sql.ErrNoRows) {
		err = invalidClipImport()
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeClipImportJSON(w, http.StatusOK, status)
}

func writeClipImportJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func invalidClipImport() error { return &model.ClipperError{Code: model.ClipperInvalidRequest} }

func (h *ClipImportHandler) stageClip(w http.ResponseWriter, r *http.Request, consume func(*service.StagedClip) error) (manifest model.ClipperManifest, err error) {
	if r.URL.RawQuery != "" || len(r.Header.Values("Content-Type")) != 1 {
		return manifest, invalidClipImport()
	}
	media, params, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if parseErr != nil || media != "multipart/form-data" || len(params) != 1 || params["boundary"] == "" {
		return manifest, invalidClipImport()
	}
	boundary := params["boundary"]
	// Use the standard writer's boundary validation without generating a body.
	validator := multipart.NewWriter(io.Discard)
	if validator.SetBoundary(boundary) != nil {
		return manifest, invalidClipImport()
	}
	root, err := h.staging.OpenPrivateStagingRoot()
	if err != nil {
		return manifest, err
	}
	if root == nil {
		return manifest, errors.New("missing Clipper staging root")
	}
	defer root.Close()
	name := "clip-import-" + rand.Text()
	if err := root.Mkdir(name, 0700); err != nil {
		return manifest, err
	}
	defer func() { err = errors.Join(err, root.RemoveAll(name)) }()
	stage, err := root.OpenRoot(name)
	if err != nil {
		return manifest, err
	}
	defer stage.Close()
	file, err := stage.OpenFile("request", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return manifest, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	// Spool the entire bounded wire body before parsing or taking service locks.
	// This also forces the total cap to cover MIME headers and any epilogue.
	r.Body = http.MaxBytesReader(w, r.Body, model.ClipperMaxMultipartBytes)
	if _, err := io.Copy(file, importContextReader{r.Context(), r.Body}); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return manifest, &model.ClipperError{Code: model.ClipperTooLarge}
		}
		return manifest, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return manifest, err
	}
	// multipart.Reader accepts epilogues. v1 deliberately accepts none: verify
	// the first final delimiter is at EOF, using only bounded line-prefix memory.
	if err := clipImportEnvelope(importContextReader{r.Context(), file}, boundary); err != nil {
		return manifest, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return manifest, err
	}
	payload, err := service.StageClip(r.Context(), stage, multipart.NewReader(file, boundary))
	if err != nil {
		return manifest, err
	}
	return payload.Manifest, consume(payload)
}

type importContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r importContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func clipImportEnvelope(input io.Reader, boundary string) error {
	reader := bufio.NewReader(input)
	closing := "--" + boundary + "--"
	prefix := make([]byte, 0, len(closing)+2)
	for {
		prefix = prefix[:0]
		length := 0
		// Track the entire suffix in constant memory. A binary line beginning
		// with "--boundary-- <other bytes>" is data, not a final delimiter.
		suffixState := 0 // 0: padding, 1: CR, 2: LF, 3: not a delimiter
		var err error
		for {
			var fragment []byte
			fragment, err = reader.ReadSlice('\n')
			for i, b := range fragment {
				if length+i < len(closing) {
					continue
				}
				switch suffixState {
				case 0:
					switch b {
					case ' ', '\t':
					case '\r':
						suffixState = 1
					case '\n':
						suffixState = 2
					default:
						suffixState = 3
					}
				case 1:
					if b == '\n' {
						suffixState = 2
					} else {
						suffixState = 3
					}
				case 2:
					suffixState = 3
				}
			}
			length += len(fragment)
			if len(prefix) < cap(prefix) {
				n := min(cap(prefix)-len(prefix), len(fragment))
				prefix = append(prefix, fragment[:n]...)
			}
			if err != bufio.ErrBufferFull {
				break
			}
		}
		// Go's mime/multipart.Reader.isFinalBoundary accepts SP/HTAB after
		// "--boundary--" (RFC 2046 transport padding), and LF in compatibility
		// mode. Our strict envelope permits only an unpadded CRLF final line or
		// an unpadded closing at EOF. Reject other closing forms immediately:
		// ignoring one could let the parser stop before an unchecked epilogue.
		if suffixState != 3 && strings.HasPrefix(string(prefix), closing) && len(prefix) > len(closing) {
			switch prefix[len(closing)] {
			case ' ', '\t', '\n':
				return invalidClipImport()
			}
		}
		if length == len(prefix) && (string(prefix) == closing+"\r\n" || (err == io.EOF && string(prefix) == closing)) {
			if _, err := reader.Peek(1); err == io.EOF {
				return nil
			}
			return invalidClipImport()
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			return invalidClipImport()
		}
	}
}
