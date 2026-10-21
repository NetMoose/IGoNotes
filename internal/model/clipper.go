package model

import "time"

const (
	ClipperProtocolVersion    = 1
	ClipperMaxMarkdownBytes   = 5 << 20
	ClipperMaxAssets          = 100
	ClipperMaxAssetBytes      = 10 << 20
	ClipperMaxTotalAssetBytes = 50 << 20
	ClipperMaxMultipartBytes  = 64 << 20

	ClipperImagePNG  = "image/png"
	ClipperImageJPEG = "image/jpeg"
	ClipperImageGIF  = "image/gif"
	ClipperImageWebP = "image/webp"
)

type ClipperLimits struct {
	MarkdownBytes   int64 `json:"markdown_bytes"`
	Assets          int   `json:"assets"`
	AssetBytes      int64 `json:"asset_bytes"`
	TotalAssetBytes int64 `json:"total_asset_bytes"`
	MultipartBytes  int64 `json:"multipart_bytes"`
}

func ClipperV1Limits() ClipperLimits {
	return ClipperLimits{
		MarkdownBytes:   ClipperMaxMarkdownBytes,
		Assets:          ClipperMaxAssets,
		AssetBytes:      ClipperMaxAssetBytes,
		TotalAssetBytes: ClipperMaxTotalAssetBytes,
		MultipartBytes:  ClipperMaxMultipartBytes,
	}
}

type ClipperInfoResponse struct {
	ProtocolVersion     int           `json:"protocol_version"`
	AppName             string        `json:"app_name"`
	AppVersion          string        `json:"app_version"`
	Limits              ClipperLimits `json:"limits"`
	SupportedImageTypes []string      `json:"supported_image_types"`
}

// Discovery DTOs deliberately expose no filesystem roots or Git configuration.
type ClipperBasesResponse struct {
	Bases []string `json:"bases"`
}

type ClipperFoldersResponse struct {
	Base                string   `json:"base"`
	Folders             []string `json:"folders"`
	DestinationRevision string   `json:"destination_revision"`
}

// ClipperManifest is the wire DTO, not the canonical digest serialization.
// See docs/api.md for the v1 canonical array contract. Validation and hashing
// belong to the import service; JSON marshaling alone does neither.
type ClipperManifest struct {
	ImportID            string                 `json:"import_id"`
	Base                string                 `json:"base"`
	DestinationRevision string                 `json:"destination_revision"`
	Parent              string                 `json:"parent"`
	Filename            string                 `json:"filename"`
	Markdown            string                 `json:"markdown"`
	SourceURL           string                 `json:"source_url"`
	CapturedAt          string                 `json:"captured_at"` // preserve the exact validated UTC timestamp string
	Assets              []ClipperAsset         `json:"assets"`
	ExternalImages      []ClipperExternalImage `json:"external_images"`
}

type ClipperAsset struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

type ClipperExternalImage struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

type ClipperImportResult struct {
	ImportID string   `json:"import_id"`
	Base     string   `json:"base"`
	Path     string   `json:"path"`
	Revision string   `json:"revision"`
	Warnings []string `json:"warnings"`
}

type ClipperImportState string

const (
	ClipperImportPending          ClipperImportState = "pending"
	ClipperImportSucceeded        ClipperImportState = "succeeded"
	ClipperImportRejected         ClipperImportState = "rejected"
	ClipperImportRecoveryRequired ClipperImportState = "recovery_required"
)

// Exactly one of Result/Error is present for a terminal state; pending has neither.
// HTTP status for this snapshot is 200, independently of the original POST status.
type ClipperImportStatusResponse struct {
	ImportID string               `json:"import_id"`
	State    ClipperImportState   `json:"state"`
	Result   *ClipperImportResult `json:"result,omitempty"`
	Error    *APIError            `json:"error,omitempty"`
}

type ClipperConnectionRequest struct {
	Label string `json:"label"`
}

// ClipperConnection is public metadata only. Never add a secret or its digest.
type ClipperConnection struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

type ClipperConnectionsResponse struct {
	Connections []ClipperConnection `json:"connections"`
}

// Only the creation response may carry the one-time secret.
type ClipperConnectionCreatedResponse struct {
	Connection ClipperConnection `json:"connection"`
	Secret     string            `json:"secret"`
}

type ClipperErrorCode string

const (
	ClipperUnauthorized        ClipperErrorCode = "clipper_unauthorized"
	ClipperInvalidRequest      ClipperErrorCode = "clipper_invalid_request"
	ClipperTooLarge            ClipperErrorCode = "clipper_too_large"
	ClipperUnsupportedImage    ClipperErrorCode = "clipper_unsupported_image"
	ClipperDestinationNotFound ClipperErrorCode = "clipper_destination_not_found"
	ClipperDestinationChanged  ClipperErrorCode = "clipper_destination_changed"
	ClipperNoteConflict        ClipperErrorCode = "note_conflict"
	ClipperRequestChanged      ClipperErrorCode = "clipper_request_changed"
	ClipperRecoveryRequired    ClipperErrorCode = "clipper_recovery_required"
	ClipperImportAbandoned     ClipperErrorCode = "clipper_import_abandoned"
	ClipperPending             ClipperErrorCode = "clipper_import_pending"
)

// ClipperError selects a fixed public message at the HTTP boundary. Wrap it to
// retain private diagnostics; neither wrappers nor arbitrary messages are sent.
// Existing setup/Git gates continue to use their existing service errors.
type ClipperError struct {
	Code ClipperErrorCode
}

func (e *ClipperError) Error() string { return string(e.Code) }
