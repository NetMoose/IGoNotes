package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

type stagedImportProbe struct {
	importStatusStub
	calls   int
	consume func(*service.StagedClip)
}

type clipSpaceReader struct{}

func (clipSpaceReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

func TestClipImportHTTPWireInclusiveBoundary(t *testing.T) {
	f := newImportHTTPFixture(t)
	m := f.manifest(t, "target")
	for _, delta := range []int64{-1, 0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			wire, err := os.CreateTemp(t.TempDir(), "wire-")
			if err != nil {
				t.Fatal(err)
			}
			defer wire.Close()
			w := multipart.NewWriter(wire)
			p, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="manifest"`}, "Content-Type": {"application/json"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(p).Encode(m); err != nil {
				t.Fatal(err)
			}
			pos, _ := wire.Seek(0, io.SeekCurrent)
			tail := int64(len("\r\n--" + w.Boundary() + "--\r\n"))
			if _, err := io.CopyN(p, clipSpaceReader{}, model.ClipperMaxMultipartBytes+delta-pos-tail); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			info, _ := wire.Stat()
			if info.Size() != model.ClipperMaxMultipartBytes+delta {
				t.Fatal(info.Size())
			}
			wire.Seek(0, io.SeekStart)
			probe := &stagedImportProbe{consume: func(s *service.StagedClip) {
				if len(s.Assets) != 0 || s.Manifest.ImportID != m.ImportID {
					t.Fatal("wrong payload")
				}
			}}
			h, _ := NewClipImportHandler(probe, f.settings)
			router := http.NewServeMux()
			RegisterClipImportRoutes(router, h, f.connections, f.settings)
			response := importRequest(router, "POST", importsPath, f.created.Secret, w.FormDataContentType(), wire)
			if delta > 0 {
				assertDiscoveryError(t, response, 413, "clipper_too_large")
				if probe.calls != 0 {
					t.Fatal("oversized request reached importer")
				}
			} else {
				assertDiscoveryError(t, response, 400, "clipper_invalid_request")
				if probe.calls != 1 {
					t.Fatal("inclusive boundary rejected before importer")
				}
			}
			assertImportUnclaimedAndClean(t, f, m.ImportID)
		})
	}
}

func (p *stagedImportProbe) ImportStaged(ctx context.Context, s *service.StagedClip) (model.ClipperImportStatusResponse, bool, error) {
	p.calls++
	p.consume(s)
	return model.ClipperImportStatusResponse{}, false, &model.ClipperError{Code: model.ClipperInvalidRequest}
}

func TestClipImportHTTPStagedAssetsLifetimeAndValidation(t *testing.T) {
	f := newImportHTTPFixture(t)
	var pngBody bytes.Buffer
	if err := png.Encode(&pngBody, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	// Valid ancillary payload includes a binary line that resembles a padded
	// closing delimiter but is not one according to mime/multipart.
	data := pngBody.Bytes()
	extra := []byte("opaque\n--asset-boundary-- \tNOT-A-DELIMITER\r\n")
	var chunk bytes.Buffer
	binary.Write(&chunk, binary.BigEndian, uint32(len(extra)))
	chunk.WriteString("tEXt")
	chunk.Write(extra)
	binary.Write(&chunk, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("tEXt"), extra...)))
	data = append(append(append([]byte{}, data[:33]...), chunk.Bytes()...), data[33:]...)
	for _, tc := range []string{"valid", "missing", "unexpected", "duplicate", "bad-hash", "truncated", "duplicate-manifest", "cancel"} {
		t.Run(tc, func(t *testing.T) {
			m := f.manifest(t, "target")
			content := data
			if tc == "truncated" {
				content = data[:len(data)-8]
			}
			hash := sha256.Sum256(content)
			m.Assets = []model.ClipperAsset{{ID: "Z", SHA256: hex.EncodeToString(hash[:]), MediaType: "image/png", Size: int64(len(content))}}
			if tc == "bad-hash" {
				m.Assets[0].SHA256 = hex.EncodeToString(make([]byte, 32))
			}
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			w.SetBoundary("asset-boundary")
			writeAsset := func(id string) {
				p, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"; filename="../../private.png"`, id)}, "Content-Type": {"image/png"}})
				if err != nil {
					t.Fatal(err)
				}
				p.Write(content)
			}
			if tc != "missing" {
				id := "Z"
				if tc == "unexpected" {
					id = "z"
				}
				writeAsset(id)
				if tc == "duplicate" {
					writeAsset(id)
				}
			}
			writeManifest := func() {
				p, _ := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="manifest"; filename="blob"`}, "Content-Type": {"application/json"}})
				json.NewEncoder(p).Encode(m)
			}
			writeManifest()
			if tc == "duplicate-manifest" {
				writeManifest()
			}
			w.Close()
			var borrowed *service.StagedClip
			probe := &stagedImportProbe{consume: func(s *service.StagedClip) {
				borrowed = s
				asset, err := s.OpenAsset("Z")
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(asset)
				asset.Close()
				if err != nil || !bytes.Equal(got, data) {
					t.Fatal("asset unavailable during import", err)
				}
			}}
			h, _ := NewClipImportHandler(probe, f.settings)
			router := http.NewServeMux()
			RegisterClipImportRoutes(router, h, f.connections, f.settings)
			var responseCode int
			if tc == "cancel" {
				r := httptest.NewRequest("POST", "http://127.0.0.1:8080"+importsPath, bytes.NewReader(body.Bytes()))
				r.Header.Set("Content-Type", w.FormDataContentType())
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				r = r.WithContext(ctx)
				response := httptest.NewRecorder()
				h.serveHTTP(response, r, func() error { return nil })
				responseCode = response.Code
			} else {
				response := importRequest(router, "POST", importsPath, f.created.Secret, w.FormDataContentType(), bytes.NewReader(body.Bytes()))
				responseCode = response.Code
			}
			if tc == "valid" {
				if probe.calls != 1 {
					t.Fatalf("validated image not delivered: calls=%d status=%d", probe.calls, responseCode)
				}
				if asset, err := borrowed.OpenAsset("Z"); err == nil {
					asset.Close()
					t.Fatal("staging outlived importer")
				}
			} else if probe.calls != 0 {
				t.Fatal("invalid payload reached importer")
			}
			assertImportUnclaimedAndClean(t, f, m.ImportID)
		})
	}
}
