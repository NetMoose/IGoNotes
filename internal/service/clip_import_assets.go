package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"strings"

	"IGoNotes/internal/model"
	"golang.org/x/image/webp"
)

// StagedClip is a validated, request-scoped payload, not a durable import.
// The caller owns the private root and must keep it open through consumption,
// then remove its request directory on every outcome. Importers must finish
// reading/copying assets before returning; retaining this payload does not
// transfer ownership. Task 5.2 must durably own bytes before acknowledging them.
type StagedClip struct {
	Manifest model.ClipperManifest
	Assets   map[string]StagedClipAsset // exact manifest ID -> verified source bytes
	root     *os.Root
}

type StagedClipAsset struct {
	Descriptor model.ClipperAsset
	Name       string // generated private root-relative name, never a client filename
	Extension  string
}

// OpenAsset opens verified source bytes read-only relative to the pinned root.
// The consumer owns the returned descriptor, and must close it before returning.
func (s *StagedClip) OpenAsset(id string) (*os.File, error) {
	a, ok := s.Assets[id]
	if !ok {
		return nil, clipperManifestError(model.ClipperInvalidRequest)
	}
	return s.root.Open(a.Name)
}

type clipContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r clipContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func clipImageExtension(media string) string {
	switch media {
	case model.ClipperImagePNG:
		return "png"
	case model.ClipperImageJPEG:
		return "jpg"
	case model.ClipperImageGIF:
		return "gif"
	case model.ClipperImageWebP:
		return "webp"
	}
	return ""
}

// StageClip consumes raw multipart parts in any order. Network/body limits and
// strict outer framing belong to the handler. Assets are streamed to private,
// exclusive files with byte/hash caps before any decoder runs. No base, journal,
// URL or lifecycle lock is accessed. On failure only generated assets are removed.
func StageClip(ctx context.Context, root *os.Root, reader *multipart.Reader) (payload *StagedClip, err error) {
	if root == nil || reader == nil {
		return nil, errors.New("missing Clipper staging inputs")
	}
	s := &StagedClip{root: root, Assets: make(map[string]StagedClipAsset)}
	var owned []string
	defer func() {
		if err != nil {
			for _, name := range owned {
				err = errors.Join(err, root.Remove(name))
			}
		}
	}()
	invalid := func() error { return clipperManifestError(model.ClipperInvalidRequest) }
	manifestSeen := false
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, e := reader.NextRawPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, invalid()
		}
		if len(part.Header.Values("Content-Disposition")) != 1 || len(part.Header.Values("Content-Type")) != 1 {
			return nil, invalid()
		}
		for key := range part.Header {
			if key != "Content-Disposition" && key != "Content-Type" {
				return nil, invalid()
			}
		}
		disposition, fields, e := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if e != nil || disposition != "form-data" {
			return nil, invalid()
		}
		for key := range fields {
			if key != "name" && key != "filename" {
				return nil, invalid()
			}
		}
		id := fields["name"]
		media, params, e := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if e != nil {
			return nil, invalid()
		}
		if id == "manifest" {
			if manifestSeen || media != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
				return nil, invalid()
			}
			manifestSeen = true
			s.Manifest, e = DecodeClipperManifest(clipContextReader{ctx, part})
			if e != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, e
			}
			continue
		}
		if !clipperAssetID.MatchString(id) || len(params) != 0 {
			return nil, invalid()
		}
		if media == "application/json" {
			return nil, invalid()
		}
		if manifestSeen {
			declared := false
			for _, a := range s.Manifest.Assets {
				if a.ID == id {
					declared = true
					break
				}
			}
			if !declared {
				return nil, invalid()
			}
		}
		if _, exists := s.Assets[id]; exists {
			return nil, invalid()
		}
		if len(s.Assets) == model.ClipperMaxAssets {
			return nil, clipperManifestError(model.ClipperTooLarge)
		}
		ext := clipImageExtension(media)
		if ext == "" {
			return nil, clipperManifestError(model.ClipperUnsupportedImage)
		}
		name := "asset-" + rand.Text()
		file, e := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		owned = append(owned, name)
		hash := sha256.New()
		limit := min(int64(model.ClipperMaxAssetBytes), int64(model.ClipperMaxTotalAssetBytes)-total)
		n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(clipContextReader{ctx, part}, limit+1))
		closeErr := file.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if n > limit {
			return nil, clipperManifestError(model.ClipperTooLarge)
		}
		total += n
		s.Assets[id] = StagedClipAsset{Descriptor: model.ClipperAsset{ID: id, MediaType: media, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}, Name: name, Extension: ext}
	}
	if !manifestSeen || len(s.Assets) != len(s.Manifest.Assets) {
		return nil, invalid()
	}
	// Follow declared order for validation; identity and canonical digest are
	// independent of both descriptor and multipart order.
	for _, declared := range s.Manifest.Assets {
		asset, ok := s.Assets[declared.ID]
		if !ok || asset.Descriptor != declared {
			return nil, invalid()
		}
		if err := validateClipAsset(ctx, root, asset); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func validateClipAsset(ctx context.Context, root *os.Root, asset StagedClipAsset) error {
	file, err := root.Open(asset.Name)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := preflightClipImage(ctx, file, asset.Descriptor.Size, asset.Descriptor.MediaType); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return decodeClipImage(ctx, file, asset.Descriptor.MediaType)
}

// Process-wide, not per-handler: only one complete pixel decode is live at a
// time. Waiting and reader IO honor cancellation, without lifecycle/SQL locks.
var clipDecodeGate = make(chan struct{}, 1)

func decodeClipImage(ctx context.Context, input io.Reader, media string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case clipDecodeGate <- struct{}{}:
	}
	defer func() { <-clipDecodeGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	r := clipContextReader{ctx, input}
	var err error
	switch media {
	case model.ClipperImagePNG:
		_, err = png.Decode(r)
	case model.ClipperImageJPEG:
		_, err = jpeg.Decode(r)
	case model.ClipperImageGIF:
		_, err = gif.DecodeAll(r)
	case model.ClipperImageWebP:
		_, err = webp.Decode(r)
	default:
		return clipperManifestError(model.ClipperUnsupportedImage)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return clipperManifestError(model.ClipperInvalidRequest)
	}
	return nil
}
