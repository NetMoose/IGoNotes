package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"IGoNotes/internal/model"
)

const (
	clipperMaxURLBytes       = 8192
	clipperMaxReasonBytes    = 1024
	clipperMaxExternalImages = 100
)

var (
	clipperUUID      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	clipperAssetID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	clipperSHA256    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	clipperTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)
)

func clipperManifestError(code model.ClipperErrorCode) error {
	return &model.ClipperError{Code: code}
}

// ValidateClipperManifest checks protocol v1 metadata only. It does not resolve
// destinations, read assets, or perform filesystem/network operations. Values
// are rejected rather than sanitized; all accepted strings remain unchanged.
func ValidateClipperManifest(m model.ClipperManifest) error {
	invalid := func() error { return clipperManifestError(model.ClipperInvalidRequest) }
	for _, s := range []string{m.ImportID, m.Base, m.DestinationRevision, m.Parent, m.Filename, m.Markdown, m.SourceURL, m.CapturedAt} {
		if !utf8.ValidString(s) {
			return invalid()
		}
	}
	if !clipperUUID.MatchString(m.ImportID) || m.Base == "" || strings.TrimSpace(m.Base) != m.Base || strings.TrimSpace(m.DestinationRevision) == "" {
		return invalid()
	}
	if m.Parent != "" {
		for _, component := range strings.Split(m.Parent, "/") {
			if !clipperSafeComponent(component) {
				return invalid()
			}
		}
	}
	if !clipperSafeComponent(m.Filename) || !strings.HasSuffix(m.Filename, ".md") || len(m.Filename) == len(".md") {
		return invalid()
	}
	if len(m.Markdown) > model.ClipperMaxMarkdownBytes {
		return clipperManifestError(model.ClipperTooLarge)
	}
	if strings.TrimSpace(m.Markdown) == "" || !clipperSafeURL(m.SourceURL) || !clipperTimestamp.MatchString(m.CapturedAt) {
		return invalid()
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CapturedAt); err != nil {
		return invalid()
	}
	if m.Assets == nil || m.ExternalImages == nil {
		return invalid()
	}
	if len(m.Assets) > model.ClipperMaxAssets || len(m.ExternalImages) > clipperMaxExternalImages {
		return clipperManifestError(model.ClipperTooLarge)
	}
	ids := make(map[string]bool, len(m.Assets))
	var total int64
	for _, a := range m.Assets {
		if !clipperAssetID.MatchString(a.ID) || a.ID == "manifest" || ids[a.ID] || !clipperSHA256.MatchString(a.SHA256) || a.Size <= 0 {
			return invalid()
		}
		ids[a.ID] = true
		if a.Size > model.ClipperMaxAssetBytes {
			return clipperManifestError(model.ClipperTooLarge)
		}
		total += a.Size // bounded count and per-asset size prevent overflow
		if total > model.ClipperMaxTotalAssetBytes {
			return clipperManifestError(model.ClipperTooLarge)
		}
		switch a.MediaType {
		case model.ClipperImagePNG, model.ClipperImageJPEG, model.ClipperImageGIF, model.ClipperImageWebP:
		default:
			return clipperManifestError(model.ClipperUnsupportedImage)
		}
	}
	for _, e := range m.ExternalImages {
		if !clipperSafeURL(e.URL) || !utf8.ValidString(e.Reason) || strings.TrimSpace(e.Reason) == "" || len(e.Reason) > clipperMaxReasonBytes || strings.ContainsAny(e.Reason, "<>") || strings.ContainsFunc(e.Reason, unicode.IsControl) {
			return invalid()
		}
	}
	return nil
}

// Portable components exclude Windows device aliases even on Unix hosts.
func clipperSafeComponent(s string) bool {
	if s == "" || s == "." || s == ".." || strings.EqualFold(s, ".git") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") || strings.ContainsAny(s, `/\<>:"|?*`) || strings.ContainsFunc(s, unicode.IsControl) {
		return false
	}
	stem := strings.ToUpper(strings.TrimRight(strings.SplitN(s, ".", 2)[0], " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if strings.HasPrefix(stem, prefix) {
			n := strings.TrimPrefix(stem, prefix)
			if len([]rune(n)) == 1 && strings.ContainsAny(n, "123456789¹²³") {
				return false
			}
		}
	}
	return true
}

func clipperSafeURL(s string) bool {
	if s == "" || len(s) > clipperMaxURLBytes || !utf8.ValidString(s) || strings.ContainsAny(s, `\`) || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Host == "" || u.Hostname() == "" || u.User != nil {
		return false
	}
	// Encoded controls are unsafe too, but the original URL is never rewritten.
	unescaped, err := url.PathUnescape(s)
	return err == nil && utf8.ValidString(unescaped) && !strings.ContainsFunc(unescaped, unicode.IsControl)
}

// DecodeClipperManifest reads one bounded JSON object and validates it. It rejects
// duplicate (including escape-equivalent) keys, unknown/missing/null fields,
// trailing input and malformed Unicode before encoding/json can replace it.
// Multipart handlers can pass the manifest part directly to this function.
func DecodeClipperManifest(reader io.Reader) (model.ClipperManifest, error) {
	var m model.ClipperManifest
	invalid := func() (model.ClipperManifest, error) {
		return model.ClipperManifest{}, clipperManifestError(model.ClipperInvalidRequest)
	}
	data, err := io.ReadAll(io.LimitReader(reader, model.ClipperMaxMultipartBytes+1))
	if err != nil {
		return invalid()
	}
	if len(data) > model.ClipperMaxMultipartBytes {
		return m, clipperManifestError(model.ClipperTooLarge)
	}
	if !utf8.Valid(data) || !clipperJSONScalars(data) {
		return invalid()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := clipperDecodeObject(d, map[string]any{
		"import_id": &m.ImportID, "base": &m.Base,
		"destination_revision": &m.DestinationRevision, "parent": &m.Parent,
		"filename": &m.Filename, "markdown": &m.Markdown,
		"source_url": &m.SourceURL, "captured_at": &m.CapturedAt,
		"assets": &m.Assets, "external_images": &m.ExternalImages,
	}); err != nil {
		if typed, ok := err.(*model.ClipperError); ok {
			return model.ClipperManifest{}, typed
		}
		return invalid()
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid()
	}
	if err := ValidateClipperManifest(m); err != nil {
		return model.ClipperManifest{}, err
	}
	return m, nil
}

// Each schema maps a fixed set of required wire names to typed DTO fields.
// Unknown/duplicate keys are rejected before reading their values. Only the
// three protocol object shapes below are traversed, never arbitrary subtrees.
func clipperDecodeObject(d *json.Decoder, fields map[string]any) error {
	invalid := func() error { return clipperManifestError(model.ClipperInvalidRequest) }
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return invalid()
	}
	seen := make(map[string]bool, len(fields))
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return invalid()
		}
		field, known := fields[name]
		if !known {
			return invalid()
		}
		seen[name] = true // bounded by the fixed schema's field count
		if err := clipperDecodeField(d, field); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(seen) != len(fields) {
		return invalid()
	}
	return nil
}

func clipperDecodeField(d *json.Decoder, field any) error {
	switch target := field.(type) {
	case *[]model.ClipperAsset:
		items, err := clipperDecodeArray(d, model.ClipperMaxAssets, func(d *json.Decoder, a *model.ClipperAsset) error {
			return clipperDecodeObject(d, map[string]any{
				"id": &a.ID, "sha256": &a.SHA256, "media_type": &a.MediaType, "size": &a.Size,
			})
		})
		*target = items
		return err
	case *[]model.ClipperExternalImage:
		items, err := clipperDecodeArray(d, clipperMaxExternalImages, func(d *json.Decoder, e *model.ClipperExternalImage) error {
			return clipperDecodeObject(d, map[string]any{"url": &e.URL, "reason": &e.Reason})
		})
		*target = items
		return err
	}
	// A mismatched container is rejected at its opening delimiter. Never walk
	// into it or use Decode on a scalar (which could buffer the whole subtree).
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch target := field.(type) {
	case *string:
		if value, ok := token.(string); ok {
			*target = value
			return nil
		}
	case *int64:
		if number, ok := token.(json.Number); ok {
			if value, err := number.Int64(); err == nil {
				*target = value
				return nil
			}
		}
	}
	return clipperManifestError(model.ClipperInvalidRequest)
}

// Arrays retain at most limit typed entries. Check the bound before consuming
// the next item, so an excess item cannot allocate or override the size error.
func clipperDecodeArray[T any](d *json.Decoder, limit int, decodeItem func(*json.Decoder, *T) error) ([]T, error) {
	token, err := d.Token()
	if err != nil || token != json.Delim('[') {
		return nil, clipperManifestError(model.ClipperInvalidRequest)
	}
	items := make([]T, 0)
	for d.More() {
		if len(items) == limit {
			return nil, clipperManifestError(model.ClipperTooLarge)
		}
		var item T
		if err := decodeItem(d, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	end, err := d.Token()
	if err != nil || end != json.Delim(']') {
		return nil, clipperManifestError(model.ClipperInvalidRequest)
	}
	return items, nil
}

// JSON syntax is checked by the decoder; this pass only rejects lone surrogate
// escapes. Escaped backslashes must not be mistaken for Unicode escapes.
func clipperJSONScalars(data []byte) bool {
	inside := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inside = !inside
			continue
		}
		if !inside || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

// CanonicalClipperManifest returns the exact v1 ordered-array JSON UTF-8 bytes
// defined in docs/api.md. Sorting uses a private slice and preserves the caller.
func CanonicalClipperManifest(m model.ClipperManifest) ([]byte, error) {
	if err := ValidateClipperManifest(m); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`["igonotes-clipper",1`)
	for _, s := range []string{m.ImportID, m.Base, m.DestinationRevision, m.Parent, m.Filename, m.Markdown, m.SourceURL, m.CapturedAt} {
		b.WriteByte(',')
		clipperQuote(&b, s)
	}
	b.WriteString(",[")
	assets := append([]model.ClipperAsset(nil), m.Assets...)
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	for i, a := range assets {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		for j, s := range []string{a.ID, a.SHA256, a.MediaType} {
			if j > 0 {
				b.WriteByte(',')
			}
			clipperQuote(&b, s)
		}
		b.WriteByte(',')
		b.WriteString(strconv.FormatInt(a.Size, 10))
		b.WriteByte(']')
	}
	b.WriteString("],[")
	for i, e := range m.ExternalImages {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		clipperQuote(&b, e.URL)
		b.WriteByte(',')
		clipperQuote(&b, e.Reason)
		b.WriteByte(']')
	}
	b.WriteString("]]")
	return b.Bytes(), nil
}

// ClipperManifestDigest hashes validated canonical bytes, returning lowercase
// SHA-256 hex without a prefix. Invalid manifests never receive a digest.
func ClipperManifestDigest(m model.ClipperManifest) (string, error) {
	canonical, err := CanonicalClipperManifest(m)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// Unlike encoding/json, v1 leaves HTML and U+2028/U+2029 literal. Input has
// already been validated as UTF-8, so copying non-ASCII bytes preserves scalars.
func clipperQuote(b *bytes.Buffer, s string) {
	const digits = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(digits[c>>4])
				b.WriteByte(digits[c&15])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}
