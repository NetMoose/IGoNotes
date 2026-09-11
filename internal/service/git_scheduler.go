package service

import (
	"context"
	"log"
	"sort"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

const (
	gitSchedulerStagger    = 5 * time.Second
	gitSchedulerRetryDelay = 30 * time.Second
	gitSchedulerMaxFailure = 5
)

type realGitSchedulerClock struct{}

func (realGitSchedulerClock) Now() time.Time { return time.Now() }

func (realGitSchedulerClock) NewTimer(delay time.Duration) GitResilienceTimer {
	return realGitSchedulerTimer{timer: time.NewTimer(delay)}
}

type realGitSchedulerTimer struct{ timer *time.Timer }

func (t realGitSchedulerTimer) C() <-chan time.Time            { return t.timer.C }
func (t realGitSchedulerTimer) Stop() bool                     { return t.timer.Stop() }
func (t realGitSchedulerTimer) Reset(delay time.Duration) bool { return t.timer.Reset(delay) }

type gitScheduleEntry struct {
	snapshot    gitcmd.ConfiguredBase
	due         time.Time
	lastAttempt *time.Time
	order       int
	blocked     bool
}

type gitScheduler struct {
	clock         GitResilienceClock
	statuses      GitStatusReader
	snapshots     GitOrderedSnapshots
	configChanges <-chan struct{}
	queue         GitSyncQueue
	logger        *log.Logger
	statusChanged chan struct{}
	entries       map[string]gitScheduleEntry
	startedAt     time.Time
}

func newGitScheduler(
	clock GitResilienceClock,
	statuses GitStatusReader,
	snapshots GitOrderedSnapshots,
	configChanges <-chan struct{},
	queue GitSyncQueue,
	logger *log.Logger,
) *gitScheduler {
	if clock == nil || statuses == nil || snapshots == nil || configChanges == nil || queue == nil {
		panic("service.newGitScheduler: nil dependency")
	}
	if logger == nil {
		logger = log.Default()
	}
	return &gitScheduler{
		clock:         clock,
		statuses:      statuses,
		snapshots:     snapshots,
		configChanges: configChanges,
		queue:         queue,
		logger:        logger,
		statusChanged: make(chan struct{}, 1),
		entries:       make(map[string]gitScheduleEntry),
	}
}

func (s *gitScheduler) notifyStatusChanged() {
	select {
	case s.statusChanged <- struct{}{}:
	default:
	}
}

func (s *gitScheduler) run(ctx context.Context) {
	s.startedAt = s.clock.Now()
	if err := s.reconcile(ctx, true); err != nil {
		s.logger.Printf("Git autosync scheduler metadata error: %v", err)
	}

	timer := s.clock.NewTimer(gitSchedulerRetryDelay)
	defer timer.Stop()
	for {
		resetGitSchedulerTimer(timer, s.nextDelay(s.clock.Now()))
		select {
		case <-ctx.Done():
			return
		case <-s.configChanges:
			s.reconcileAndLog(ctx, false)
		case <-s.statusChanged:
			s.reconcileAndLog(ctx, false)
		case <-timer.C():
			if !s.queueDue(ctx, s.clock.Now()) {
				return
			}
			s.reconcileAndLog(ctx, false)
		}
	}
}

func resetGitSchedulerTimer(timer GitResilienceTimer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
	timer.Reset(delay)
}

func (s *gitScheduler) reconcileAndLog(ctx context.Context, startup bool) {
	if err := s.reconcile(ctx, startup); err != nil {
		s.logger.Printf("Git autosync scheduler metadata error: %v", err)
	}
}

func (s *gitScheduler) reconcile(ctx context.Context, startup bool) error {
	now := s.clock.Now()
	bases := s.snapshots.OrderedGitSnapshots()
	type scheduledBase struct {
		snapshot gitcmd.ConfiguredBase
		status   model.GitStatus
		found    bool
		order    int
		blocked  bool
	}
	candidates := make([]scheduledBase, 0, len(bases))
	for order, snapshot := range bases {
		if !snapshot.AutoSync {
			continue
		}
		if !gitSchedulerInterval(snapshot.IntervalMinutes) {
			s.logger.Printf("Git autosync scheduler ignored base %q with invalid interval", snapshot.Name)
			continue
		}
		status, found, err := s.statuses.Get(ctx, snapshot.Path)
		if err != nil {
			return err
		}
		candidates = append(candidates, scheduledBase{
			snapshot: snapshot,
			status:   status,
			found:    found,
			order:    order,
			blocked:  !found || gitScheduleBlocked(status),
		})
	}

	next := make(map[string]gitScheduleEntry, len(candidates))
	eligible := 0
	for _, candidate := range candidates {
		path := candidate.snapshot.Path
		if candidate.blocked {
			next[path] = gitScheduleEntry{snapshot: candidate.snapshot, order: candidate.order, blocked: true}
			continue
		}
		eligible++
		previous, existed := s.entries[path]
		unchangedAttempt := equalGitScheduleAttempt(previous.lastAttempt, candidate.status.LastAttempt)
		unchanged := existed && !previous.blocked && previous.snapshot.IntervalMinutes == candidate.snapshot.IntervalMinutes && unchangedAttempt
		if unchanged {
			previous.snapshot = candidate.snapshot
			previous.order = candidate.order
			next[path] = previous
			continue
		}

		slot := now.Add(time.Duration(eligible) * gitSchedulerStagger)
		if startup {
			slot = s.startedAt.Add(time.Duration(eligible) * gitSchedulerStagger)
		}
		next[path] = gitScheduleEntry{
			snapshot:    candidate.snapshot,
			due:         gitScheduleDue(now, slot, candidate.status.LastAttempt, time.Duration(candidate.snapshot.IntervalMinutes)*time.Minute, startup),
			lastAttempt: cloneGitScheduleAttempt(candidate.status.LastAttempt),
			order:       candidate.order,
		}
	}
	s.entries = next
	return nil
}

func gitSchedulerInterval(minutes int) bool {
	switch minutes {
	case 5, 15, 30, 60:
		return true
	default:
		return false
	}
}

func gitScheduleBlocked(status model.GitStatus) bool {
	if status.ConsecutiveFailures >= gitSchedulerMaxFailure {
		return true
	}
	switch status.State {
	case model.GitStatePaused, model.GitStateConflict, model.GitStateNeedsReconnect,
		model.GitStateInitializing, model.GitStateSyncing, model.GitStateUnconfigured:
		return true
	default:
		return false
	}
}

func gitScheduleDue(now, slot time.Time, lastAttempt *time.Time, interval time.Duration, startup bool) time.Time {
	if lastAttempt == nil {
		return slot
	}
	due := lastAttempt.Add(interval)
	if !due.After(now) && (startup || slot.After(now)) {
		return slot
	}
	return due
}

func equalGitScheduleAttempt(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

func cloneGitScheduleAttempt(attempt *time.Time) *time.Time {
	if attempt == nil {
		return nil
	}
	copy := *attempt
	return &copy
}

func (s *gitScheduler) nextDelay(now time.Time) time.Duration {
	var due time.Time
	for _, entry := range s.entries {
		if entry.blocked || (!due.IsZero() && !entry.due.Before(due)) {
			continue
		}
		due = entry.due
	}
	if due.IsZero() {
		return gitSchedulerRetryDelay
	}
	if !due.After(now) {
		return 0
	}
	return due.Sub(now)
}

func (s *gitScheduler) queueDue(ctx context.Context, now time.Time) bool {
	entries := make([]gitScheduleEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		if !entry.blocked && !entry.due.After(now) {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(left, right int) bool {
		if !entries[left].due.Equal(entries[right].due) {
			return entries[left].due.Before(entries[right].due)
		}
		if entries[left].order != entries[right].order {
			return entries[left].order < entries[right].order
		}
		return entries[left].snapshot.Path < entries[right].snapshot.Path
	})
	for _, entry := range entries {
		if ctx.Err() != nil {
			return false
		}
		_, _, err := s.queue.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: entry.snapshot})
		current, found := s.entries[entry.snapshot.Path]
		if !found {
			continue
		}
		if err != nil {
			s.logger.Printf("Git autosync scheduler queue error for base %q: %v", entry.snapshot.Name, err)
			current.due = now.Add(gitSchedulerRetryDelay)
		} else {
			current.due = now.Add(time.Duration(current.snapshot.IntervalMinutes) * time.Minute)
		}
		s.entries[entry.snapshot.Path] = current
	}
	return true
}
