package handlers

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

func TestClipperErrorMapping(t *testing.T) {
	for _, test := range []struct {
		code    model.ClipperErrorCode
		status  int
		message string
	}{
		{"clipper_unauthorized", 401, "Clipper authorization failed"},
		{"clipper_invalid_request", 400, "Invalid Clipper request"},
		{"clipper_too_large", 413, "Clipper request exceeds limits"},
		{"clipper_unsupported_image", 415, "Unsupported Clipper image"},
		{"clipper_destination_not_found", 404, "Clipper destination not found"},
		{"clipper_destination_changed", 409, "Clipper destination changed"},
		{"note_conflict", 409, "Note already exists"},
		{"clipper_request_changed", 409, "Clipper request changed"},
		{"clipper_recovery_required", 409, "Clipper import requires recovery"},
		{"clipper_import_abandoned", 409, "Clipper import abandoned; files may already exist"},
		{"clipper_import_pending", 202, "Clipper import pending"},
		{"future_code", 500, internalErrorMessage},
	} {
		t.Run(string(test.code), func(t *testing.T) {
			err := &model.ClipperError{Code: test.code}
			if err.Error() != string(test.code) {
				t.Fatalf("error = %q", err.Error())
			}
			recorder := httptest.NewRecorder()
			writeServiceError(recorder, fmt.Errorf("/private/base token=secret: %w", err))
			code := string(test.code)
			if test.status == 500 {
				code = "internal_error"
			}
			assertAPIErrorResponse(t, recorder, test.status, model.APIError{Code: code, Message: test.message})
		})
	}
}

func TestClipperErrorPreservesRollbackPrecedence(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeServiceError(recorder, errors.Join(&model.ClipperError{Code: model.ClipperInvalidRequest}, service.ErrRollbackFailed))
	assertAPIErrorResponse(t, recorder, 500, model.APIError{Code: "rollback_failed", Message: internalErrorMessage})
}
