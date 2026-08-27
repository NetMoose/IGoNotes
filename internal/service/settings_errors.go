package service

import "errors"

var (
	ErrSetupRequired         = errors.New("setup required")
	ErrSetupAlreadyCompleted = errors.New("setup already completed")
	ErrSetupCannotReopen     = errors.New("setup cannot be reopened")
	ErrInvalidConfig         = errors.New("invalid config")
	ErrInvalidMode           = errors.New("invalid mode")
	ErrInvalidName           = errors.New("invalid name")
	ErrInvalidPath           = errors.New("invalid path")
	ErrBaseNotFound          = errors.New("base not found")
	ErrBaseNameConflict      = errors.New("base name conflict")
	ErrBasePathConflict      = errors.New("base path conflict")
	ErrActiveBase            = errors.New("active base")
	ErrLastBase              = errors.New("last base")
	ErrRollbackFailed        = errors.New("rollback failed")
)

type FieldError struct {
	Kind    error
	Field   string
	Message string
}

func (e *FieldError) Error() string {
	return e.Message
}

func (e *FieldError) Unwrap() error {
	return e.Kind
}
