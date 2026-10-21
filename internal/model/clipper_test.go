package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestClipperV1WireContract(t *testing.T) {
	stamp := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		value any
		wire  string
	}{
		{"info", ClipperInfoResponse{ProtocolVersion: ClipperProtocolVersion, AppName: "IGoNotes", AppVersion: "dev", Limits: ClipperV1Limits(), SupportedImageTypes: []string{ClipperImagePNG, ClipperImageJPEG, ClipperImageGIF, ClipperImageWebP}}, `{"protocol_version":1,"app_name":"IGoNotes","app_version":"dev","limits":{"markdown_bytes":5242880,"assets":100,"asset_bytes":10485760,"total_asset_bytes":52428800,"multipart_bytes":67108864},"supported_image_types":["image/png","image/jpeg","image/gif","image/webp"]}`},
		{"bases names only", ClipperBasesResponse{Bases: []string{"work"}}, `{"bases":["work"]}`},
		{"folders relative root", ClipperFoldersResponse{Base: "work", Folders: []string{"", "research"}, DestinationRevision: "opaque"}, `{"base":"work","folders":["","research"],"destination_revision":"opaque"}`},
		{"manifest", ClipperManifest{ImportID: "id", Base: "work", DestinationRevision: "opaque", Parent: "", Filename: "clip.md", Markdown: "текст\r\n", SourceURL: "https://example.test/", CapturedAt: "2026-10-21T12:00:00Z", Assets: []ClipperAsset{{ID: "a1", SHA256: "digest", MediaType: "image/png", Size: 123}}, ExternalImages: []ClipperExternalImage{{URL: "https://example.test/a.png", Reason: "permission_denied"}}}, `{"import_id":"id","base":"work","destination_revision":"opaque","parent":"","filename":"clip.md","markdown":"текст\r\n","source_url":"https://example.test/","captured_at":"2026-10-21T12:00:00Z","assets":[{"id":"a1","sha256":"digest","media_type":"image/png","size":123}],"external_images":[{"url":"https://example.test/a.png","reason":"permission_denied"}]}`},
		{"result", ClipperImportResult{ImportID: "id", Base: "work", Path: "research/clip.md", Revision: "sha256:digest", Warnings: []string{}}, `{"import_id":"id","base":"work","path":"research/clip.md","revision":"sha256:digest","warnings":[]}`},
		{"pending", ClipperImportStatusResponse{ImportID: "id", State: ClipperImportPending}, `{"import_id":"id","state":"pending"}`},
		{"succeeded", ClipperImportStatusResponse{ImportID: "id", State: ClipperImportSucceeded, Result: &ClipperImportResult{ImportID: "id", Base: "work", Path: "clip.md", Revision: "sha256:digest", Warnings: []string{}}}, `{"import_id":"id","state":"succeeded","result":{"import_id":"id","base":"work","path":"clip.md","revision":"sha256:digest","warnings":[]}}`},
		{"rejected", ClipperImportStatusResponse{ImportID: "id", State: ClipperImportRejected, Error: &APIError{Code: "note_conflict", Message: "Note already exists"}}, `{"import_id":"id","state":"rejected","error":{"code":"note_conflict","message":"Note already exists"}}`},
		{"recovery", ClipperImportStatusResponse{ImportID: "id", State: ClipperImportRecoveryRequired, Error: &APIError{Code: "clipper_recovery_required", Message: "Clipper import requires recovery"}}, `{"import_id":"id","state":"recovery_required","error":{"code":"clipper_recovery_required","message":"Clipper import requires recovery"}}`},
		{"create request", ClipperConnectionRequest{Label: "Laptop"}, `{"label":"Laptop"}`},
		{"metadata no secret or digest", ClipperConnection{ID: "connection", Label: "Laptop", CreatedAt: stamp}, `{"id":"connection","label":"Laptop","created_at":"2026-10-21T12:00:00Z","revoked_at":null}`},
		{"revoked metadata", ClipperConnection{ID: "connection", Label: "Laptop", CreatedAt: stamp, RevokedAt: &stamp}, `{"id":"connection","label":"Laptop","created_at":"2026-10-21T12:00:00Z","revoked_at":"2026-10-21T12:00:00Z"}`},
		{"connection list", ClipperConnectionsResponse{Connections: []ClipperConnection{}}, `{"connections":[]}`},
		{"one time secret", ClipperConnectionCreatedResponse{Connection: ClipperConnection{ID: "connection", Label: "Laptop", CreatedAt: stamp}, Secret: "one-time"}, `{"connection":{"id":"connection","label":"Laptop","created_at":"2026-10-21T12:00:00Z","revoked_at":null},"secret":"one-time"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.wire {
				t.Fatalf("wire = %s; want %s", data, test.wire)
			}
			decoded := reflect.New(reflect.TypeOf(test.value))
			if err := json.Unmarshal([]byte(test.wire), decoded.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded.Elem().Interface(), test.value) {
				t.Fatalf("wire round trip lost data: %#v", decoded.Elem().Interface())
			}
		})
	}
}
