package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

type staticSettingsSnapshot struct{ config model.Config }

func (s staticSettingsSnapshot) ReadConfigSnapshot(read func(model.Config) error) error {
	return read(cloneConfig(s.config))
}

type fakeGitStatusReader struct {
	statuses map[string]model.GitStatus
	getErr   error
	listErr  error
	gets     []string
	lists    int
}

type blockingGitStatusStore struct {
	mu                sync.Mutex
	statuses          map[string]model.GitStatus
	listCalls         int
	firstListStarted  chan struct{}
	secondListStarted chan struct{}
	releaseFirstList  <-chan struct{}
}

func (s *blockingGitStatusStore) Get(_ context.Context, path string) (model.GitStatus, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, ok := s.statuses[path]
	return cloneGitStatusForTest(status), ok, nil
}

func (s *blockingGitStatusStore) List(_ context.Context) ([]model.GitStatus, error) {
	s.mu.Lock()
	s.listCalls++
	call := s.listCalls
	result := make([]model.GitStatus, 0, len(s.statuses))
	for _, status := range s.statuses {
		result = append(result, cloneGitStatusForTest(status))
	}
	s.mu.Unlock()
	if call == 1 {
		close(s.firstListStarted)
		<-s.releaseFirstList
	} else if call == 2 {
		close(s.secondListStarted)
	}
	return result, nil
}

func (s *blockingGitStatusStore) Upsert(_ context.Context, status model.GitStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[status.RepositoryPath] = cloneGitStatusForTest(status)
	return nil
}

func (s *blockingGitStatusStore) Delete(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.statuses, path)
	return nil
}

func (f *fakeGitStatusReader) Get(_ context.Context, path string) (model.GitStatus, bool, error) {
	f.gets = append(f.gets, path)
	if f.getErr != nil {
		return model.GitStatus{}, false, f.getErr
	}
	status, ok := f.statuses[path]
	return cloneGitStatusForTest(status), ok, nil
}

func (f *fakeGitStatusReader) List(_ context.Context) ([]model.GitStatus, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := make([]model.GitStatus, 0, len(f.statuses))
	for _, status := range f.statuses {
		result = append(result, cloneGitStatusForTest(status))
	}
	return result, nil
}

func TestGitStatusServiceReturnsConfigOrderAndSynthesizesCoherentStatuses(t *testing.T) {
	firstPath, secondPath, thirdPath := t.TempDir(), t.TempDir(), t.TempDir()
	config := model.Config{Bases: []model.Base{
		{Name: "unconfigured", Path: firstPath},
		{Name: "missing", Path: secondPath, GitURL: "missing.git", GitBranch: "main"},
		{Name: "existing", Path: thirdPath, GitURL: "existing.git", GitBranch: "main"},
	}}
	reader := &fakeGitStatusReader{statuses: map[string]model.GitStatus{
		firstPath: {Base: "stale", RepositoryPath: firstPath, State: model.GitStateReady, ChangedPaths: []string{"ignored.md"}},
		thirdPath: {Base: "old-name", RepositoryPath: thirdPath, State: model.GitStateConflict, Ahead: 2, ChangedPaths: nil},
	}}
	service := NewGitStatusService(staticSettingsSnapshot{config}, reader)

	response, err := service.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := []model.GitStatus{
		{Base: "unconfigured", RepositoryPath: firstPath, State: model.GitStateUnconfigured, ChangedPaths: []string{}},
		{Base: "missing", RepositoryPath: secondPath, State: model.GitStateNeedsReconnect, ChangedPaths: []string{}},
		{Base: "existing", RepositoryPath: thirdPath, State: model.GitStateConflict, Ahead: 2, ChangedPaths: []string{}},
	}
	if !reflect.DeepEqual(response.Statuses, want) {
		t.Fatalf("statuses = %#v, want %#v", response.Statuses, want)
	}
	if reader.lists != 1 || len(reader.gets) != 0 {
		t.Fatalf("reader calls = lists %d gets %v", reader.lists, reader.gets)
	}
}

func TestGitStatusServiceExactFilterAndMissingBase(t *testing.T) {
	path := t.TempDir()
	config := model.Config{Bases: []model.Base{{Name: "Work", Path: path, GitURL: "work.git", GitBranch: "main"}}}
	reader := &fakeGitStatusReader{statuses: map[string]model.GitStatus{path: {Base: "old", RepositoryPath: path, State: model.GitStateReady}}}
	service := NewGitStatusService(staticSettingsSnapshot{config}, reader)

	response, err := service.Status(context.Background(), "Work")
	if err != nil || len(response.Statuses) != 1 || response.Statuses[0].Base != "Work" || response.Statuses[0].ChangedPaths == nil {
		t.Fatalf("Status(Work) = %#v, %v", response, err)
	}
	if reader.lists != 0 || !reflect.DeepEqual(reader.gets, []string{path}) {
		t.Fatalf("reader calls = lists %d gets %v", reader.lists, reader.gets)
	}
	if _, err := service.Status(context.Background(), "work"); !errors.Is(err, ErrBaseNotFound) {
		t.Fatalf("Status(work) error = %v, want ErrBaseNotFound", err)
	}
}

func TestGitStatusServiceNilReaderTreatsRowsAsMissing(t *testing.T) {
	path := t.TempDir()
	config := model.Config{Bases: []model.Base{{Name: "work", Path: path, GitURL: "work.git", GitBranch: "main"}}}
	response, err := NewGitStatusService(staticSettingsSnapshot{config}, nil).Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := model.GitStatus{Base: "work", RepositoryPath: path, State: model.GitStateNeedsReconnect, ChangedPaths: []string{}}
	if len(response.Statuses) != 1 || !reflect.DeepEqual(response.Statuses[0], want) {
		t.Fatalf("Status() = %#v, want %#v", response, want)
	}
}

func TestGitStatusServiceForwardsReaderErrors(t *testing.T) {
	path := t.TempDir()
	wantErr := errors.New("status database closed")
	config := model.Config{Bases: []model.Base{{Name: "work", Path: path, GitURL: "work.git", GitBranch: "main"}}}
	service := NewGitStatusService(staticSettingsSnapshot{config}, &fakeGitStatusReader{listErr: wantErr})
	if _, err := service.Status(context.Background(), ""); !errors.Is(err, wantErr) {
		t.Fatalf("Status() error = %v, want %v", err, wantErr)
	}
}

func TestGitStatusServiceSerializesConfigAndStatusGeneration(t *testing.T) {
	path := t.TempDir()
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "old", Path: path, GitURL: "work.git", GitBranch: "main"}},
		CurrentBase:    "old",
		SetupCompleted: &completed,
	}
	release := make(chan struct{})
	statuses := &blockingGitStatusStore{
		statuses: map[string]model.GitStatus{
			path: {Base: "old", RepositoryPath: path, State: model.GitStateReady, ChangedPaths: []string{"old.md"}},
		},
		firstListStarted:  make(chan struct{}),
		secondListStarted: make(chan struct{}),
		releaseFirstList:  release,
	}
	settings, err := NewSettingsServiceWithGit(
		&fakeConfigStore{config: &config},
		&fakeBaseRuntime{path: path},
		NewBaseOperationCoordinator(),
		"",
		nil,
		nil,
		statuses,
	)
	if err != nil {
		t.Fatal(err)
	}
	service := NewGitStatusService(settings, statuses)
	readDone := make(chan struct{})
	var first model.GitStatusResponse
	var readErr error
	go func() {
		first, readErr = service.Status(context.Background(), "")
		close(readDone)
	}()
	<-statuses.firstListStarted

	mutationDone := make(chan error, 1)
	go func() {
		_, err := settings.UpdateBase("old", model.BaseUpdateRequest{Name: "new", Path: path})
		mutationDone <- err
	}()
	select {
	case <-statuses.secondListStarted:
		close(release)
		<-readDone
		<-mutationDone
		t.Fatal("rename reached status persistence while aggregation callback was blocked")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-readDone
	if readErr != nil {
		t.Fatalf("first Status() error = %v", readErr)
	}
	if len(first.Statuses) != 1 || first.Statuses[0].Base != "old" || !reflect.DeepEqual(first.Statuses[0].ChangedPaths, []string{"old.md"}) {
		t.Fatalf("first Status() = %#v, want coherent old generation", first)
	}
	if err := <-mutationDone; err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	second, err := service.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("second Status() error = %v", err)
	}
	if len(second.Statuses) != 1 || second.Statuses[0].Base != "new" || !reflect.DeepEqual(second.Statuses[0].ChangedPaths, []string{"old.md"}) {
		t.Fatalf("second Status() = %#v, want coherent new generation", second)
	}
}
