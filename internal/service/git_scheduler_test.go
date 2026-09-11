package service

import (
	"context"
	"errors"
	"io"
	"log"
	"runtime"
	"sync"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

func TestGitSchedulerStaggersOverdueBasesInConfigOrder(t *testing.T) {
	now := schedulerTestNow()
	clock := newFakeGitSchedulerClock(now)
	statuses := &fakeGitSchedulerStatuses{statuses: map[string]model.GitStatus{
		"/notes/b": schedulerReadyStatus("b", "/notes/b", now.Add(-time.Hour)),
		"/notes/a": schedulerReadyStatus("a", "/notes/a", now.Add(-time.Hour)),
	}}
	snapshots := &fakeGitSchedulerSnapshots{bases: []gitcmd.ConfiguredBase{
		schedulerBase("b", "/notes/b", 5), schedulerBase("a", "/notes/a", 5),
	}}
	queue := &fakeGitSchedulerQueue{calls: make(chan gitcmd.SyncRequest, 2)}
	cancel := startGitScheduler(t, clock, statuses, snapshots, queue)
	defer cancel()

	clock.Advance(5 * time.Second)
	if got := <-queue.calls; got.Snapshot.Name != "b" {
		t.Fatalf("first scheduled base = %q, want b", got.Snapshot.Name)
	}
	clock.waitForReset(t)
	clock.Advance(5 * time.Second)
	if got := <-queue.calls; got.Snapshot.Name != "a" {
		t.Fatalf("second scheduled base = %q, want a", got.Snapshot.Name)
	}
}

func TestGitSchedulerSupportsExactlyFiveFifteenThirtySixtyMinutes(t *testing.T) {
	for _, interval := range []int{5, 15, 30, 60} {
		t.Run(time.Duration(interval).String(), func(t *testing.T) {
			now := schedulerTestNow()
			clock := newFakeGitSchedulerClock(now)
			statuses := &fakeGitSchedulerStatuses{statuses: map[string]model.GitStatus{
				"/notes/work": schedulerReadyStatus("work", "/notes/work", now),
			}}
			snapshots := &fakeGitSchedulerSnapshots{bases: []gitcmd.ConfiguredBase{schedulerBase("work", "/notes/work", interval)}}
			queue := &fakeGitSchedulerQueue{calls: make(chan gitcmd.SyncRequest, 1)}
			cancel := startGitScheduler(t, clock, statuses, snapshots, queue)
			defer cancel()

			clock.Advance(time.Duration(interval)*time.Minute - time.Second)
			assertNoGitSchedulerCall(t, queue.calls)
			clock.Advance(time.Second)
			if got := <-queue.calls; got.Snapshot.IntervalMinutes != interval {
				t.Fatalf("scheduled interval = %d, want %d", got.Snapshot.IntervalMinutes, interval)
			}
		})
	}
}

func TestGitSchedulerUsesPersistedLastAttemptAsIntervalAnchor(t *testing.T) {
	now := schedulerTestNow()
	clock := newFakeGitSchedulerClock(now)
	lastAttempt := now.Add(2 * time.Minute)
	statuses := &fakeGitSchedulerStatuses{statuses: map[string]model.GitStatus{
		"/notes/work": schedulerReadyStatus("work", "/notes/work", lastAttempt),
	}}
	snapshots := &fakeGitSchedulerSnapshots{bases: []gitcmd.ConfiguredBase{schedulerBase("work", "/notes/work", 5)}}
	queue := &fakeGitSchedulerQueue{calls: make(chan gitcmd.SyncRequest, 1)}
	cancel := startGitScheduler(t, clock, statuses, snapshots, queue)
	defer cancel()

	clock.Advance(6*time.Minute + 59*time.Second)
	assertNoGitSchedulerCall(t, queue.calls)
	clock.Advance(time.Second)
	<-queue.calls
}

func TestGitSchedulerQueuesDueBasesInStableOrder(t *testing.T) {
	now := schedulerTestNow()
	clock := newFakeGitSchedulerClock(now)
	statuses := &fakeGitSchedulerStatuses{statuses: map[string]model.GitStatus{
		"/notes/z": schedulerReadyStatus("z", "/notes/z", now),
		"/notes/a": schedulerReadyStatus("a", "/notes/a", now),
	}}
	snapshots := &fakeGitSchedulerSnapshots{bases: []gitcmd.ConfiguredBase{
		schedulerBase("z", "/notes/z", 5), schedulerBase("a", "/notes/a", 5),
	}}
	queue := &fakeGitSchedulerQueue{calls: make(chan gitcmd.SyncRequest, 2)}
	cancel := startGitScheduler(t, clock, statuses, snapshots, queue)
	defer cancel()

	clock.Advance(5 * time.Minute)
	if got := <-queue.calls; got.Snapshot.Name != "z" {
		t.Fatalf("first due base = %q, want z", got.Snapshot.Name)
	}
	if got := <-queue.calls; got.Snapshot.Name != "a" {
		t.Fatalf("second due base = %q, want a", got.Snapshot.Name)
	}
}

func TestGitSchedulerDoesNotQueueBlockedBases(t *testing.T) {
	now := schedulerTestNow()
	clock := newFakeGitSchedulerClock(now)
	states := []model.GitState{
		model.GitStatePaused, model.GitStateConflict, model.GitStateNeedsReconnect,
		model.GitStateInitializing, model.GitStateSyncing, model.GitStateUnconfigured,
	}
	statuses := &fakeGitSchedulerStatuses{statuses: make(map[string]model.GitStatus)}
	bases := make([]gitcmd.ConfiguredBase, 0, len(states)+2)
	for index, state := range states {
		path := "/notes/blocked-" + string(rune('a'+index))
		statuses.statuses[path] = model.GitStatus{Base: path, RepositoryPath: path, State: state}
		bases = append(bases, schedulerBase(path, path, 5))
	}
	statuses.statuses["/notes/failures"] = model.GitStatus{Base: "failures", RepositoryPath: "/notes/failures", State: model.GitStateError, ConsecutiveFailures: 5}
	bases = append(bases, schedulerBase("failures", "/notes/failures", 5), gitcmd.ConfiguredBase{Name: "disabled", Path: "/notes/disabled", AutoSync: false, IntervalMinutes: 5})
	queue := &fakeGitSchedulerQueue{calls: make(chan gitcmd.SyncRequest, 1)}
	cancel := startGitScheduler(t, clock, statuses, &fakeGitSchedulerSnapshots{bases: bases}, queue)
	defer cancel()

	clock.Advance(time.Hour)
	assertNoGitSchedulerCall(t, queue.calls)
}

func TestGitSchedulerRetriesMetadataAndQueueErrorsAfterThirtySeconds(t *testing.T) {
	now := schedulerTestNow()
	clock := newFakeGitSchedulerClock(now)
	statuses := &fakeGitSchedulerStatuses{err: errors.New("metadata unavailable"), statuses: map[string]model.GitStatus{
		"/notes/work": schedulerReadyStatus("work", "/notes/work", now),
	}}
	snapshots := &fakeGitSchedulerSnapshots{bases: []gitcmd.ConfiguredBase{schedulerBase("work", "/notes/work", 5)}}
	queue := &fakeGitSchedulerQueue{err: errors.New("queue unavailable"), calls: make(chan gitcmd.SyncRequest, 2)}
	cancel := startGitScheduler(t, clock, statuses, snapshots, queue)
	defer cancel()

	statuses.setError(nil)
	clock.Advance(30 * time.Second)
	clock.waitForReset(t)
	clock.Advance(5 * time.Minute)
	<-queue.calls
	clock.waitForReset(t)
	clock.Advance(29 * time.Second)
	assertGitSchedulerCalls(t, queue.calls, 0)
	clock.Advance(time.Second)
	if got := <-queue.calls; got.Snapshot.Name != "work" {
		t.Fatalf("retry queued %q, want work", got.Snapshot.Name)
	}
}

func TestGitSchedulerReconcilesChangesWithoutOverlappingTimers(t *testing.T) {
	now := schedulerTestNow()
	clock := newFakeGitSchedulerClock(now)
	statuses := &fakeGitSchedulerStatuses{statuses: map[string]model.GitStatus{
		"/notes/work": schedulerReadyStatus("work", "/notes/work", now),
	}}
	snapshots := &fakeGitSchedulerSnapshots{bases: []gitcmd.ConfiguredBase{schedulerBase("work", "/notes/work", 15)}}
	queue := &fakeGitSchedulerQueue{calls: make(chan gitcmd.SyncRequest, 1)}
	changes := make(chan struct{}, 1)
	scheduler := newGitScheduler(clock, statuses, snapshots, changes, queue, log.New(io.Discard, "", 0))
	ctx, cancelContext := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); scheduler.run(ctx) }()
	clock.waitForReset(t)

	snapshots.set([]gitcmd.ConfiguredBase{schedulerBase("renamed", "/notes/work", 5)})
	changes <- struct{}{}
	clock.waitForReset(t)
	if got := clock.timerCount(); got != 1 {
		t.Fatalf("timer count = %d, want one resettable timer", got)
	}
	clock.Advance(5 * time.Minute)
	if got := <-queue.calls; got.Snapshot.Name != "renamed" {
		t.Fatalf("reconciled snapshot name = %q, want renamed", got.Snapshot.Name)
	}

	cancelContext()
	<-done
}

func TestGitSchedulerCancellationStopsTimerAndQueuesNothing(t *testing.T) {
	now := schedulerTestNow()
	clock := newFakeGitSchedulerClock(now)
	statuses := &fakeGitSchedulerStatuses{statuses: map[string]model.GitStatus{
		"/notes/work": schedulerReadyStatus("work", "/notes/work", now),
	}}
	queue := &fakeGitSchedulerQueue{calls: make(chan gitcmd.SyncRequest, 1)}
	cancel := startGitScheduler(t, clock, statuses, &fakeGitSchedulerSnapshots{bases: []gitcmd.ConfiguredBase{schedulerBase("work", "/notes/work", 5)}}, queue)
	cancel()
	clock.Advance(time.Hour)
	assertNoGitSchedulerCall(t, queue.calls)
}

func TestGitSchedulerRejectsNilDependencies(t *testing.T) {
	clock := newFakeGitSchedulerClock(schedulerTestNow())
	statuses := &fakeGitSchedulerStatuses{}
	snapshots := &fakeGitSchedulerSnapshots{}
	queue := &fakeGitSchedulerQueue{}
	for _, test := range []struct {
		name string
		new  func()
	}{
		{"clock", func() { newGitScheduler(nil, statuses, snapshots, make(chan struct{}), queue, nil) }},
		{"statuses", func() { newGitScheduler(clock, nil, snapshots, make(chan struct{}), queue, nil) }},
		{"snapshots", func() { newGitScheduler(clock, statuses, nil, make(chan struct{}), queue, nil) }},
		{"changes", func() { newGitScheduler(clock, statuses, snapshots, nil, queue, nil) }},
		{"queue", func() { newGitScheduler(clock, statuses, snapshots, make(chan struct{}), nil, nil) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("newGitScheduler() did not panic")
				}
			}()
			test.new()
		})
	}
}

func startGitScheduler(t *testing.T, clock *fakeGitSchedulerClock, statuses GitStatusReader, snapshots GitOrderedSnapshots, queue GitSyncQueue) context.CancelFunc {
	t.Helper()
	scheduler := newGitScheduler(clock, statuses, snapshots, make(chan struct{}), queue, log.New(io.Discard, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); scheduler.run(ctx) }()
	clock.waitForReset(t)
	return func() {
		cancel()
		<-done
	}
}

func schedulerTestNow() time.Time {
	return time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC)
}

func schedulerBase(name, path string, interval int) gitcmd.ConfiguredBase {
	return gitcmd.ConfiguredBase{Name: name, Path: path, URL: "https://example.invalid/" + name, Branch: "main", AutoSync: true, IntervalMinutes: interval}
}

func schedulerReadyStatus(name, path string, attempt time.Time) model.GitStatus {
	return model.GitStatus{Base: name, RepositoryPath: path, State: model.GitStateReady, LastAttempt: &attempt}
}

func assertNoGitSchedulerCall(t *testing.T, calls <-chan gitcmd.SyncRequest) {
	t.Helper()
	select {
	case got := <-calls:
		t.Fatalf("unexpected scheduled base %q", got.Snapshot.Name)
	default:
	}
}

func assertGitSchedulerCalls(t *testing.T, calls <-chan gitcmd.SyncRequest, want int) {
	t.Helper()
	got := 0
	for {
		select {
		case <-calls:
			got++
		default:
			if got != want {
				t.Fatalf("scheduled calls = %d, want %d", got, want)
			}
			return
		}
	}
}

type fakeGitSchedulerClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  map[*fakeGitSchedulerTimer]struct{}
	resets  chan struct{}
	created int
}

type fakeGitSchedulerTimer struct {
	clock    *fakeGitSchedulerClock
	channel  chan time.Time
	deadline time.Time
	armed    bool
}

func newFakeGitSchedulerClock(now time.Time) *fakeGitSchedulerClock {
	return &fakeGitSchedulerClock{now: now, timers: make(map[*fakeGitSchedulerTimer]struct{}), resets: make(chan struct{}, 32)}
}

func (c *fakeGitSchedulerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeGitSchedulerClock) NewTimer(delay time.Duration) GitResilienceTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeGitSchedulerTimer{clock: c, channel: make(chan time.Time, 1), deadline: c.now.Add(delay), armed: true}
	c.timers[timer] = struct{}{}
	c.created++
	return timer
}

func (c *fakeGitSchedulerClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	now := c.now
	due := make([]*fakeGitSchedulerTimer, 0)
	for timer := range c.timers {
		if timer.armed && !timer.deadline.After(now) {
			timer.armed = false
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		select {
		case timer.channel <- now:
		default:
		}
	}
}

func (c *fakeGitSchedulerClock) waitForReset(t *testing.T) {
	t.Helper()
	for attempts := 0; attempts < 10000; attempts++ {
		select {
		case <-c.resets:
			return
		default:
			runtime.Gosched()
		}
	}
	t.Fatal("scheduler did not reset its timer")
}

func (c *fakeGitSchedulerClock) timerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.created
}

func (t *fakeGitSchedulerTimer) C() <-chan time.Time { return t.channel }

func (t *fakeGitSchedulerTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasArmed := t.armed
	t.armed = false
	return wasArmed
}

func (t *fakeGitSchedulerTimer) Reset(delay time.Duration) bool {
	t.clock.mu.Lock()
	wasArmed := t.armed
	t.deadline = t.clock.now.Add(delay)
	t.armed = true
	t.clock.mu.Unlock()
	select {
	case t.clock.resets <- struct{}{}:
	default:
	}
	return wasArmed
}

type fakeGitSchedulerStatuses struct {
	mu       sync.Mutex
	statuses map[string]model.GitStatus
	err      error
}

func (s *fakeGitSchedulerStatuses) Get(_ context.Context, path string) (model.GitStatus, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return model.GitStatus{}, false, s.err
	}
	status, found := s.statuses[path]
	return status, found, nil
}

func (s *fakeGitSchedulerStatuses) List(context.Context) ([]model.GitStatus, error) { return nil, nil }

func (s *fakeGitSchedulerStatuses) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

type fakeGitSchedulerSnapshots struct {
	mu    sync.Mutex
	bases []gitcmd.ConfiguredBase
}

func (s *fakeGitSchedulerSnapshots) OrderedGitSnapshots() []gitcmd.ConfiguredBase {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gitcmd.ConfiguredBase(nil), s.bases...)
}

func (s *fakeGitSchedulerSnapshots) set(bases []gitcmd.ConfiguredBase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bases = append([]gitcmd.ConfiguredBase(nil), bases...)
}

type fakeGitSchedulerQueue struct {
	mu    sync.Mutex
	err   error
	calls chan gitcmd.SyncRequest
}

func (q *fakeGitSchedulerQueue) QueueSync(_ context.Context, request gitcmd.SyncRequest) (gitcmd.Operation, bool, error) {
	q.mu.Lock()
	err := q.err
	q.mu.Unlock()
	if q.calls != nil {
		q.calls <- request
	}
	return gitcmd.Operation{}, false, err
}
