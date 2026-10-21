package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"os"
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func validClipManifest() model.ClipperManifest {
	return model.ClipperManifest{ImportID: "550e8400-e29b-41d4-a716-446655440000", Base: "work", DestinationRevision: "opaque", Filename: "clip.md", Markdown: "Hello\n", SourceURL: "https://example.test/", CapturedAt: "2026-10-21T12:00:00Z", Assets: []model.ClipperAsset{}, ExternalImages: []model.ClipperExternalImage{}}
}

func clipAsset(id string) model.ClipperAsset {
	return model.ClipperAsset{ID: id, SHA256: strings.Repeat("a", 64), MediaType: model.ClipperImagePNG, Size: 1}
}

func requireClipError(t *testing.T, err error, code model.ClipperErrorCode) {
	t.Helper()
	var typed *model.ClipperError
	if !errors.As(err, &typed) || typed.Code != code || err.Error() != string(code) {
		t.Fatalf("error = %v, want safe %s", err, code)
	}
}

func TestClipperManifestValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*model.ClipperManifest)
		code   model.ClipperErrorCode
	}{
		{"uuid version", func(m *model.ClipperManifest) { m.ImportID = "550e8400-e29b-11d4-a716-446655440000" }, model.ClipperInvalidRequest},
		{"uuid case", func(m *model.ClipperManifest) { m.ImportID = strings.ToUpper(m.ImportID) }, model.ClipperInvalidRequest},
		{"uuid variant", func(m *model.ClipperManifest) { m.ImportID = "550e8400-e29b-41d4-7716-446655440000" }, model.ClipperInvalidRequest},
		{"base empty", func(m *model.ClipperManifest) { m.Base = " " }, model.ClipperInvalidRequest},
		{"base trim", func(m *model.ClipperManifest) { m.Base = " work" }, model.ClipperInvalidRequest},
		{"revision", func(m *model.ClipperManifest) { m.DestinationRevision = "" }, model.ClipperInvalidRequest},
		{"empty", func(m *model.ClipperManifest) { m.Markdown = "\r\n\t\u2003" }, model.ClipperInvalidRequest},
		{"utf8", func(m *model.ClipperManifest) { m.Markdown = string([]byte{255}) }, model.ClipperInvalidRequest},
		{"markdown limit", func(m *model.ClipperManifest) { m.Markdown = strings.Repeat("x", model.ClipperMaxMarkdownBytes+1) }, model.ClipperTooLarge},
		{"url scheme", func(m *model.ClipperManifest) { m.SourceURL = "file:///etc/passwd" }, model.ClipperInvalidRequest},
		{"url relative", func(m *model.ClipperManifest) { m.SourceURL = "//example.test/" }, model.ClipperInvalidRequest},
		{"url credentials", func(m *model.ClipperManifest) { m.SourceURL = "https://secret@example.test/" }, model.ClipperInvalidRequest},
		{"url empty host", func(m *model.ClipperManifest) { m.SourceURL = "https:///path" }, model.ClipperInvalidRequest},
		{"url control", func(m *model.ClipperManifest) { m.SourceURL += "\x7f" }, model.ClipperInvalidRequest},
		{"url escaped control", func(m *model.ClipperManifest) { m.SourceURL += "%0a" }, model.ClipperInvalidRequest},
		{"date offset", func(m *model.ClipperManifest) { m.CapturedAt = "2026-10-21T12:00:00+00:00" }, model.ClipperInvalidRequest},
		{"date invalid", func(m *model.ClipperManifest) { m.CapturedAt = "2026-02-30T12:00:00Z" }, model.ClipperInvalidRequest},
		{"date comma", func(m *model.ClipperManifest) { m.CapturedAt = "2026-10-21T12:00:00,1Z" }, model.ClipperInvalidRequest},
		{"date precision", func(m *model.ClipperManifest) { m.CapturedAt = "2026-10-21T12:00:00.1234567890Z" }, model.ClipperInvalidRequest},
		{"nil assets", func(m *model.ClipperManifest) { m.Assets = nil }, model.ClipperInvalidRequest},
		{"nil external", func(m *model.ClipperManifest) { m.ExternalImages = nil }, model.ClipperInvalidRequest},
		{"duplicate assets", func(m *model.ClipperManifest) { m.Assets = []model.ClipperAsset{clipAsset("a"), clipAsset("a")} }, model.ClipperInvalidRequest},
		{"external url", func(m *model.ClipperManifest) {
			m.ExternalImages = []model.ClipperExternalImage{{URL: "javascript:alert(1)", Reason: "failed"}}
		}, model.ClipperInvalidRequest},
		{"external reason", func(m *model.ClipperManifest) {
			m.ExternalImages = []model.ClipperExternalImage{{URL: m.SourceURL, Reason: " "}}
		}, model.ClipperInvalidRequest},
		{"external html", func(m *model.ClipperManifest) {
			m.ExternalImages = []model.ClipperExternalImage{{URL: m.SourceURL, Reason: "<script>"}}
		}, model.ClipperInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := validClipManifest()
			tc.change(&m)
			requireClipError(t, ValidateClipperManifest(m), tc.code)
			digest, err := ClipperManifestDigest(m)
			requireClipError(t, err, tc.code)
			if digest != "" {
				t.Fatal("digest returned for invalid manifest")
			}
		})
	}
}

func TestClipperManifestPaths(t *testing.T) {
	for _, filename := range []string{"", ".md", "a.MD", "a/b.md", `a\b.md`, "../a.md", "/a.md", "C:a.md", "a?.md", "a*.md", "a|.md", "a<.md", "a>.md", "a\".md", "a\n.md", "a\x7f.md", "a.md.", "a.md ", "CON.md", "con.extra.md", "PRN.md", "AUX.md", "NUL.md", "COM1.md", "LPT9.md", "COM¹.md", "LPT².md", ".git"} {
		t.Run("filename/"+filename, func(t *testing.T) {
			m := validClipManifest()
			m.Filename = filename
			requireClipError(t, ValidateClipperManifest(m), model.ClipperInvalidRequest)
		})
	}
	for _, parent := range []string{".", "..", "a/../b", "a/./b", "a//b", "a/", "/a", `a\b`, "C:/a", ".git", "a/.GiT/b", "a\n", "CON", "a./b", "a /b"} {
		t.Run("parent/"+parent, func(t *testing.T) {
			m := validClipManifest()
			m.Parent = parent
			requireClipError(t, ValidateClipperManifest(m), model.ClipperInvalidRequest)
		})
	}
	for _, filename := range []string{"Заметка 😀.md", "YAML-like title.md", "COM10.md", "console.md"} {
		m := validClipManifest()
		m.Filename = filename
		m.Parent = "исследования/😀"
		m.Markdown = "---\r\ntitle: '\"quoted\": yes'\r\n---\r\n# 😀\r\n"
		m.CapturedAt = "2026-10-21T12:00:00.1200Z"
		m.SourceURL = "HTTPS://Example.test:443/a%2Fb?q=a&x=1#fragment"
		if err := ValidateClipperManifest(m); err != nil {
			t.Fatalf("valid Unicode/YAML values: %v", err)
		}
	}
}

func TestClipperManifestExternalBounds(t *testing.T) {
	m := validClipManifest()
	m.SourceURL = "https://example.test/" + strings.Repeat("x", 8192-len("https://example.test/"))
	m.ExternalImages = make([]model.ClipperExternalImage, 100)
	for i := range m.ExternalImages {
		m.ExternalImages[i] = model.ClipperExternalImage{URL: m.SourceURL, Reason: strings.Repeat("é", 512)}
	}
	if err := ValidateClipperManifest(m); err != nil {
		t.Fatal(err)
	}
	m.SourceURL += "x"
	requireClipError(t, ValidateClipperManifest(m), model.ClipperInvalidRequest)
	m.SourceURL = "https://example.test/"
	m.ExternalImages[0].Reason += "x"
	requireClipError(t, ValidateClipperManifest(m), model.ClipperInvalidRequest)
	m.ExternalImages[0].Reason = "failed"
	m.ExternalImages = append(m.ExternalImages, model.ClipperExternalImage{URL: m.SourceURL, Reason: "failed"})
	requireClipError(t, ValidateClipperManifest(m), model.ClipperTooLarge)
	for _, reason := range []string{string([]byte{255}), "bad\x00reason", "bad\nreason", "bad\x7freason"} {
		m.ExternalImages = []model.ClipperExternalImage{{URL: m.SourceURL, Reason: reason}}
		requireClipError(t, ValidateClipperManifest(m), model.ClipperInvalidRequest)
	}
}

func TestClipperManifestDigestIncludesEveryField(t *testing.T) {
	m := validClipManifest()
	m.Assets = []model.ClipperAsset{clipAsset("a")}
	m.ExternalImages = []model.ClipperExternalImage{{URL: m.SourceURL, Reason: "failed"}}
	original, err := ClipperManifestDigest(m)
	if err != nil {
		t.Fatal(err)
	}
	changes := []func(*model.ClipperManifest){
		func(m *model.ClipperManifest) { m.ImportID = "550e8400-e29b-41d4-a716-446655440001" },
		func(m *model.ClipperManifest) { m.Base = "other" },
		func(m *model.ClipperManifest) { m.DestinationRevision += "x" },
		func(m *model.ClipperManifest) { m.Parent = "nested" },
		func(m *model.ClipperManifest) { m.Filename = "other.md" },
		func(m *model.ClipperManifest) { m.Markdown += " " },
		func(m *model.ClipperManifest) { m.SourceURL += "#fragment" },
		func(m *model.ClipperManifest) { m.CapturedAt = "2026-10-21T12:00:00.000Z" },
		func(m *model.ClipperManifest) { m.Assets[0].ID = "b" },
		func(m *model.ClipperManifest) { m.Assets[0].SHA256 = strings.Repeat("b", 64) },
		func(m *model.ClipperManifest) { m.Assets[0].MediaType = model.ClipperImageJPEG },
		func(m *model.ClipperManifest) { m.Assets[0].Size++ },
		func(m *model.ClipperManifest) { m.ExternalImages[0].URL += "#fragment" },
		func(m *model.ClipperManifest) { m.ExternalImages[0].Reason = "other" },
	}
	for i, change := range changes {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			changed := m
			changed.Assets = append([]model.ClipperAsset{}, m.Assets...)
			changed.ExternalImages = append([]model.ClipperExternalImage{}, m.ExternalImages...)
			change(&changed)
			digest, err := ClipperManifestDigest(changed)
			if err != nil || digest == original {
				t.Fatalf("semantic change ignored: %s %v", digest, err)
			}
		})
	}
}

func TestDecodeClipperManifestSemanticEscapesAndPropertyOrder(t *testing.T) {
	m := validClipManifest()
	data, _ := json.Marshal(m)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	fields["markdown"] = json.RawMessage(`"\u0048ello\u000a"`)
	// Map marshaling uses a different property order from the DTO.
	wire, _ := json.MarshalIndent(fields, "", "  ")
	got, err := DecodeClipperManifest(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, _ := ClipperManifestDigest(m)
	gotDigest, _ := ClipperManifestDigest(got)
	if gotDigest != wantDigest {
		t.Fatal("property order / equivalent escapes changed digest")
	}
}

func TestClipperManifestAssetLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		asset model.ClipperAsset
		code  model.ClipperErrorCode
	}{
		{"id", clipAsset("é"), model.ClipperInvalidRequest}, {"reserved", clipAsset("manifest"), model.ClipperInvalidRequest}, {"empty", clipAsset(""), model.ClipperInvalidRequest}, {"long", clipAsset(strings.Repeat("a", 65)), model.ClipperInvalidRequest},
		{"separator", clipAsset("a/b"), model.ClipperInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := validClipManifest()
			m.Assets = []model.ClipperAsset{tc.asset}
			requireClipError(t, ValidateClipperManifest(m), tc.code)
		})
	}
	for _, hash := range []string{"", strings.Repeat("A", 64), "sha256:" + strings.Repeat("a", 64), strings.Repeat("g", 64)} {
		m := validClipManifest()
		a := clipAsset("a")
		a.SHA256 = hash
		m.Assets = []model.ClipperAsset{a}
		requireClipError(t, ValidateClipperManifest(m), model.ClipperInvalidRequest)
	}
	for _, size := range []int64{-1, 0, model.ClipperMaxAssetBytes + 1, 1 << 62} {
		m := validClipManifest()
		a := clipAsset("a")
		a.Size = size
		m.Assets = []model.ClipperAsset{a}
		code := model.ClipperInvalidRequest
		if size > model.ClipperMaxAssetBytes {
			code = model.ClipperTooLarge
		}
		requireClipError(t, ValidateClipperManifest(m), code)
	}
	for _, mime := range []string{"image/svg+xml", "image/png; charset=utf-8", "IMAGE/PNG", ""} {
		m := validClipManifest()
		a := clipAsset("a")
		a.MediaType = mime
		m.Assets = []model.ClipperAsset{a}
		requireClipError(t, ValidateClipperManifest(m), model.ClipperUnsupportedImage)
	}
	m := validClipManifest()
	m.Markdown = strings.Repeat("é", model.ClipperMaxMarkdownBytes/2)
	for i := range model.ClipperMaxAssets {
		m.Assets = append(m.Assets, clipAsset(fmt.Sprintf("a%d", i)))
	}
	if err := ValidateClipperManifest(m); err != nil {
		t.Fatal(err)
	}
	m.Assets = append(m.Assets, clipAsset("extra"))
	requireClipError(t, ValidateClipperManifest(m), model.ClipperTooLarge)
	m = validClipManifest()
	for i := range 5 {
		a := clipAsset(fmt.Sprintf("a%d", i))
		a.Size = model.ClipperMaxAssetBytes
		m.Assets = append(m.Assets, a)
	}
	if err := ValidateClipperManifest(m); err != nil {
		t.Fatal(err)
	}
	m.Assets = append(m.Assets, clipAsset("extra"))
	requireClipError(t, ValidateClipperManifest(m), model.ClipperTooLarge)
}

func TestDecodeClipperManifestStrict(t *testing.T) {
	data, _ := json.Marshal(validClipManifest())
	valid := string(data)
	for _, bad := range []string{"null", "[]", "{}", valid + " {}", valid + " garbage", strings.Replace(valid, `"base":"work"`, `"base":"work","base":"other"`, 1), strings.Replace(valid, `"base":"work"`, `"base":"work","b\u0061se":"other"`, 1), strings.Replace(valid, `"base":"work"`, `"unknown":1,"base":"work"`, 1), strings.Replace(valid, `"markdown":"Hello\n"`, `"markdown":"\ud800"`, 1), strings.Replace(valid, `"markdown":"Hello\n"`, `"markdown":"\udc00"`, 1), strings.Replace(valid, "Hello", string([]byte{255}), 1)} {
		_, err := DecodeClipperManifest(strings.NewReader(bad))
		requireClipError(t, err, model.ClipperInvalidRequest)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for field := range fields {
		for _, missing := range []bool{false, true} {
			copyFields := map[string]json.RawMessage{}
			for k, v := range fields {
				copyFields[k] = v
			}
			if missing {
				delete(copyFields, field)
			} else {
				copyFields[field] = json.RawMessage("null")
			}
			bad, _ := json.Marshal(copyFields)
			_, err := DecodeClipperManifest(bytes.NewReader(bad))
			requireClipError(t, err, model.ClipperInvalidRequest)
		}
	}
	m := validClipManifest()
	m.Assets = []model.ClipperAsset{clipAsset("a")}
	m.ExternalImages = []model.ClipperExternalImage{{URL: m.SourceURL, Reason: "failed"}}
	nested, _ := json.Marshal(m)
	for _, pair := range [][2]string{{`"id":"a"`, `"id":"a","id":"b"`}, {`"size":1`, `"size":null`}, {`"size":1`, `"size":1.0`}, {`"size":1`, `"size":1e0`}, {`"size":1`, `"size":1,"extra":0`}, {`"reason":"failed"`, `"reason":null`}, {`"reason":"failed"`, `"reason":"failed","reason":"other"`}} {
		_, err := DecodeClipperManifest(strings.NewReader(strings.Replace(string(nested), pair[0], pair[1], 1)))
		requireClipError(t, err, model.ClipperInvalidRequest)
	}
	for _, replacement := range []string{`"\ud83d\ude00"`, `"�"`, `"\\ud800"`} {
		good := strings.Replace(valid, `"Hello\n"`, replacement, 1)
		if _, err := DecodeClipperManifest(strings.NewReader(good)); err != nil {
			t.Fatalf("valid scalar/escaped slash: %v", err)
		}
	}
	got, err := DecodeClipperManifest(bytes.NewReader(data))
	if err != nil || !reflect.DeepEqual(got, validClipManifest()) {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
}

func TestDecodeClipperManifestRejectsExcessBeforeNextItem(t *testing.T) {
	for _, field := range []string{"assets", "external_images"} {
		t.Run(field, func(t *testing.T) {
			var item []byte
			var limit int
			if field == "assets" {
				item, _ = json.Marshal(clipAsset("a"))
				limit = model.ClipperMaxAssets
			} else {
				item, _ = json.Marshal(model.ClipperExternalImage{URL: "https://example.test/", Reason: "failed"})
				limit = clipperMaxExternalImages
			}
			var prefix strings.Builder
			fmt.Fprintf(&prefix, `{"%s":[`, field)
			for i := range limit {
				if i > 0 {
					prefix.WriteByte(',')
				}
				if field == "assets" {
					unique, _ := json.Marshal(clipAsset(fmt.Sprintf("a%d", i)))
					prefix.Write(unique)
				} else {
					prefix.Write(item)
				}
			}
			// Once the inclusive limit is reached, even malformed or wrong-type
			// excess entries must not be tokenized. Generic tree construction
			// instead reaches the sentinel and reports invalid JSON/schema.
			for _, tail := range []string{`,{"broken":!`, `,null]}`, `,{}]}`} {
				_, err := DecodeClipperManifest(strings.NewReader(prefix.String() + tail))
				requireClipError(t, err, model.ClipperTooLarge)
			}
		})
	}
}

func TestDecodeClipperManifestInclusiveArrayLimits(t *testing.T) {
	m := validClipManifest()
	for i := range model.ClipperMaxAssets {
		m.Assets = append(m.Assets, clipAsset(fmt.Sprintf("a%d", i)))
	}
	for range clipperMaxExternalImages {
		m.ExternalImages = append(m.ExternalImages, model.ClipperExternalImage{URL: m.SourceURL, Reason: "failed"})
	}
	wire, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeClipperManifest(bytes.NewReader(wire))
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("inclusive limits roundtrip: %v", err)
	}
}

func TestDecodeClipperManifestRejectsWideInvalidContainersEarly(t *testing.T) {
	for _, tc := range []struct{ name, prefix, suffix string }{
		{"unknown root field", `{"unknown":`, `}`},
		{"wrong string type", `{"markdown":`, `}`},
		{"wrong assets type", `{"assets":{"unknown":`, `}}`},
		{"unknown asset field", `{"assets":[{"unknown":`, `}]}`},
		{"wrong asset id type", `{"assets":[{"id":`, `}]}`},
		{"wrong asset size type", `{"assets":[{"size":`, `}]}`},
		{"unknown external field", `{"external_images":[{"unknown":`, `}]}`},
		{"wrong external reason type", `{"external_images":[{"reason":`, `}]}`},
		{"wrong asset entry type", `{"assets":[`, `]}`},
		{"wrong external entry type", `{"external_images":[`, `]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, container := range []string{"array", "object"} {
				t.Run(container, func(t *testing.T) {
					payload := func(width int) string {
						var b strings.Builder
						b.WriteString(tc.prefix)
						if container == "array" {
							b.WriteByte('[')
						} else {
							b.WriteByte('{')
						}
						for i := range width {
							if i > 0 {
								b.WriteByte(',')
							}
							if container == "object" {
								fmt.Fprintf(&b, `"k%d":`, i)
							}
							b.WriteString(`{}`)
						}
						if container == "array" {
							b.WriteByte(']')
						} else {
							b.WriteByte('}')
						}
						b.WriteString(tc.suffix)
						return b.String()
					}
					narrow, wide := payload(1), payload(4096)
					for _, wire := range []string{narrow, wide} {
						_, err := DecodeClipperManifest(strings.NewReader(wire))
						requireClipError(t, err, model.ClipperInvalidRequest)
					}
					allocs := func(wire string) float64 {
						return testing.AllocsPerRun(1, func() {
							_, _ = DecodeClipperManifest(strings.NewReader(wire))
						})
					}
					small, large := allocs(narrow), allocs(wide)
					t.Logf("allocations: narrow=%.0f wide=%.0f delta=%.0f", small, large, large-small)
					// Reading the bounded raw input still allocates a buffer. Its
					// logarithmic growth fits comfortably here; materializing or
					// merely tokenizing 4096 forbidden entries does not.
					if large-small > 256 {
						t.Fatalf("forbidden subtree traversed: allocations narrow=%.0f wide=%.0f (delta must be <=256)", small, large)
					}
				})
			}
		})
	}
}

func TestClipperManifestSharedDigests(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clipper/manifest-digests.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name      string          `json:"name"`
		Manifest  json.RawMessage `json:"manifest"`
		Canonical string          `json:"canonical"`
		Digest    string          `json:"digest"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 5 {
		t.Fatal("missing shared cases")
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			m, err := DecodeClipperManifest(bytes.NewReader(f.Manifest))
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(m)
			canonical, err := CanonicalClipperManifest(m)
			if err != nil || string(canonical) != f.Canonical {
				t.Fatalf("canonical = %s (%v), want %s", canonical, err, f.Canonical)
			}
			digest, err := ClipperManifestDigest(m)
			if err != nil || digest != f.Digest {
				t.Fatalf("digest = %s (%v), want %s", digest, err, f.Digest)
			}
			hash := sha256.Sum256([]byte(f.Canonical))
			if hex.EncodeToString(hash[:]) != f.Digest {
				t.Fatal("fixture hash mismatch")
			}
			after, _ := json.Marshal(m)
			if !bytes.Equal(before, after) {
				t.Fatal("canonicalization mutated manifest")
			}
		})
	}
}

func TestClipperManifestSemanticDigest(t *testing.T) {
	m := validClipManifest()
	m.Assets = []model.ClipperAsset{clipAsset("z"), clipAsset("A")}
	m.ExternalImages = []model.ClipperExternalImage{{URL: m.SourceURL, Reason: "one"}, {URL: m.SourceURL, Reason: "two"}}
	original, _ := ClipperManifestDigest(m)
	m.Assets[0], m.Assets[1] = m.Assets[1], m.Assets[0]
	sorted, _ := ClipperManifestDigest(m)
	if original != sorted {
		t.Fatal("asset ordering changed digest")
	}
	for _, boundary := range []string{"boundary-one", "boundary-two"} {
		var wire bytes.Buffer
		w := multipart.NewWriter(&wire)
		if err := w.SetBoundary(boundary); err != nil {
			t.Fatal(err)
		}
		part, _ := w.CreateFormField("manifest")
		_ = json.NewEncoder(part).Encode(m)
		_ = w.Close()
		r := multipart.NewReader(&wire, boundary)
		p, err := r.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeClipperManifest(p)
		if err != nil {
			t.Fatal(err)
		}
		digest, _ := ClipperManifestDigest(decoded)
		if digest != original {
			t.Fatal("transport changed digest")
		}
	}
	m.ExternalImages[0], m.ExternalImages[1] = m.ExternalImages[1], m.ExternalImages[0]
	reordered, _ := ClipperManifestDigest(m)
	if reordered == original {
		t.Fatal("external ordering ignored")
	}
	m = validClipManifest()
	lf, _ := ClipperManifestDigest(m)
	m.Markdown = "Hello\r\n"
	crlf, _ := ClipperManifestDigest(m)
	if lf == crlf {
		t.Fatal("newline normalized")
	}
}
