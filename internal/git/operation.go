package git

import (
	"context"
	"time"

	"IGoNotes/internal/model"
)

type OperationKind string
type OperationState string
type Stage string

const (
	OperationInitialize OperationKind = "initialize"
	OperationSync       OperationKind = "sync"

	OperationQueued    OperationState = "queued"
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
	OperationConflict  OperationState = "conflict"

	StageQueued       Stage = "queued"
	StageProbing      Stage = "probing"
	StageFetching     Stage = "fetching"
	StageSnapshotting Stage = "snapshotting"
	StageBackingUp    Stage = "backing_up"
	StageSwitching    Stage = "switching"
	StageMerging      Stage = "merging"
	StageReindexing   Stage = "reindexing"
	StagePushing      Stage = "pushing"
	StageCompleted    Stage = "completed"
)

type Operation struct {
	ID                string
	BaseName          string
	RepoPath          string
	ConfigFingerprint string
	RemoteFingerprint string
	Kind              OperationKind
	State             OperationState
	Stage             Stage
	Branch            string
	BackupRef         string
	LocalOID          string
	CandidateOID      string
	RemoteOID         string
	PushOID           string
	ChangedPaths      []string
	ConflictPaths     []string
	Error             *SafeError
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Checkpoint struct {
	Stage         Stage
	BackupRef     string
	LocalOID      string
	CandidateOID  string
	RemoteOID     string
	PushOID       string
	ChangedPaths  []string
	ConflictPaths []string
}

type ConfiguredBase struct {
	Name              string
	Path              string
	URL               string
	Branch            string
	AutoSync          bool
	IntervalMinutes   int
	CommitTemplate    string
	Fingerprint       string
	RemoteFingerprint string
}

type WorktreeTransaction func(
	context.Context,
	func(canonicalPath string) error,
) error

type Progress func(context.Context, Checkpoint) error

type InitializeRequest struct {
	Snapshot      ConfiguredBase
	Confirmations model.GitConfirmations
}

type SyncRequest struct {
	Snapshot ConfiguredBase
}

type InitializeOptions struct {
	Operation     Operation
	Snapshot      ConfiguredBase
	Probe         model.GitProbeResponse
	Confirmations model.GitConfirmations
	LastRemoteOID string
}

type SyncOptions struct {
	Operation     Operation
	Snapshot      ConfiguredBase
	LastRemoteOID string
}

type OperationResult struct {
	HeadOID       string
	RemoteOID     string
	PushOID       string
	BackupRef     string
	ChangedPaths  []string
	ConflictPaths []string
	Ahead         int
	Behind        int
}

type RecoveryResult struct {
	HeadOID       string
	MergeHeadOID  string
	RemoteOID     string
	ConflictPaths []string
	Blocking      bool
}

type RecoveryOptions struct {
	Snapshot  ConfiguredBase
	Operation *Operation
}
