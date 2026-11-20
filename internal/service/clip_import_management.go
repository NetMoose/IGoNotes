package service

import (
	"context"
	"database/sql"
	"errors"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

// ListUnfinished is a read-only snapshot, not an admission decision. Never return
// private records or their journal, identity, digest, or canonical root.
func (s *ClipImportService) ListUnfinished(ctx context.Context) ([]model.ClipperImportSummary, error) {
	rows, err := s.store.ListUnfinished(ctx)
	if err != nil {
		return nil, fixedClipperError(err)
	}
	result := make([]model.ClipperImportSummary, 0, len(rows))
	for _, row := range rows {
		status := publicClipImport(row)
		summary := model.ClipperImportSummary{ImportID: row.ImportID, Base: row.BaseName, Path: row.NotePath, State: status.State}
		if status.State == model.ClipperImportRecoveryRequired {
			summary.Error = &model.APIError{Code: string(model.ClipperRecoveryRequired), Message: "Clipper import requires recovery"}
		}
		result = append(result, summary)
	}
	return result, nil
}

// Abandon only changes durable metadata. Coordinator serializes it with local
// recovery/publication; CAS also rejects stale versions from other DB writers.
// No settings/root/index access or file cleanup belongs in this operation.
func (s *ClipImportService) Abandon(ctx context.Context, id string) (model.ClipperImportStatusResponse, error) {
	zero := model.ClipperImportStatusResponse{}
	if !clipperUUID.MatchString(id) {
		return zero, &model.ClipperError{Code: model.ClipperInvalidRequest}
	}
	s.settings.coordinator.Lock()
	defer s.settings.coordinator.Unlock()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	row, found, err := s.store.Get(ctx, id)
	if err != nil {
		return zero, fixedClipperError(err)
	}
	if !found {
		return zero, sql.ErrNoRows
	}
	if row.State == repository.ClipImportRejected && row.Error != nil && row.Error.Code == string(model.ClipperImportAbandoned) {
		return publicClipImport(row), nil
	}
	if row.State != repository.ClipImportRecoveryRequired {
		return zero, &model.ClipperError{Code: model.ClipperImportNotAbandonable}
	}
	public := &model.APIError{Code: string(model.ClipperImportAbandoned), Message: "Clipper import abandoned; files may already exist"}
	if err := s.store.Transition(ctx, id, row.Version(), repository.ClipImportRejected, nil, public); err != nil {
		if errors.Is(err, repository.ErrClipImportTransition) {
			return zero, &model.ClipperError{Code: model.ClipperImportNotAbandonable}
		}
		return zero, fixedClipperError(err)
	}
	row.State, row.Error, row.Result = repository.ClipImportRejected, public, nil
	return publicClipImport(row), nil
}
