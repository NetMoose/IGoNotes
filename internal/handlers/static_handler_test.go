package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesPWAAssets(t *testing.T) {
	manifestBody := []byte(`{"name":"IGoNotes"}`)
	iconBody := []byte("\x89PNG\r\n\x1a\nicon")
	handler := NewSPAHandler(fstest.MapFS{
		"manifest.webmanifest": {Data: manifestBody},
		"icons/icon-192.png":   {Data: iconBody},
	})

	tests := []struct {
		name        string
		path        string
		contentType string
		body        []byte
	}{
		{
			name:        "manifest",
			path:        "/manifest.webmanifest",
			contentType: "application/manifest+json",
			body:        manifestBody,
		},
		{
			name:        "icon",
			path:        "/icons/icon-192.png",
			contentType: "image/png",
			body:        iconBody,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if got := recorder.Header().Get("Content-Type"); got != tt.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.contentType)
			}
			if got := recorder.Body.String(); got != string(tt.body) {
				t.Fatalf("body = %q, want %q", got, tt.body)
			}
		})
	}
}
