package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

func clipImportFixture(id string) ClipImportRecord {
	now := time.Date(2026, 10, 21, 12, 0, 0, 123, time.UTC)
	return ClipImportRecord{ImportID: id, RequestDigest: strings.Repeat("a", 64), BaseName: "original", BasePath: "/private/base", RootIdentity: "root-1", DestinationRevision: "destination-1", NotePath: "nested/note.md", NoteContentHash: strings.Repeat("b", 64), Journal: ClipImportJournal{Plan: json.RawMessage(`{"note":"nested/note.md","ownership":"private-token","staging":"/private/staging","assets":[]}`), Checkpoint: json.RawMessage(`{}`)}, State: ClipImportPrepared, CreatedAt: now, UpdatedAt: now}
}

func clipImportDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func mustClaimClip(t *testing.T, r *ClipImportRepository, id string) ClipImportRecord {
	t.Helper()
	got, created, err := r.Claim(context.Background(), clipImportFixture(id))
	if err != nil || !created {
		t.Fatalf("claim: created=%v err=%v", created, err)
	}
	return got
}

func TestClipImportClaimReplayAndMismatch(t *testing.T) {
	db, _ := clipImportDB(t)
	r := NewClipImportRepository(db)
	want := mustClaimClip(t, r, "one")
	replay := clipImportFixture("one")
	replay.BaseName, replay.BasePath, replay.RootIdentity = "renamed", "/replacement", "root-2"
	replay.Journal.Plan = json.RawMessage(`{"other":"plan"}`)
	replay.CreatedAt = replay.CreatedAt.Add(time.Hour)
	replay.UpdatedAt = replay.CreatedAt
	got, created, err := r.Claim(context.Background(), replay)
	if err != nil || created || !reflect.DeepEqual(got, want) {
		t.Fatalf("replay changed receipt: %+v %v %v", got, created, err)
	}
	replay.RequestDigest = strings.Repeat("c", 64)
	_, created, err = r.Claim(context.Background(), replay)
	var changed *model.ClipperError
	if created || !errors.As(err, &changed) || changed.Code != model.ClipperRequestChanged {
		t.Fatalf("mismatch: %v %v", created, err)
	}
	got, found, err := r.Get(context.Background(), "one")
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("mismatch modified record: %+v %v", got, err)
	}
}

func TestClipImportConcurrentClaimAcrossConnections(t *testing.T) {
	db, path := clipImportDB(t)
	other, err := InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	repos := []*ClipImportRepository{NewClipImportRepository(db), NewClipImportRepository(other)}
	start := make(chan struct{})
	type outcome struct {
		record  ClipImportRecord
		created bool
		err     error
	}
	results := make(chan outcome, 24)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			<-start
			v, c, e := repos[i%2].Claim(context.Background(), clipImportFixture("shared"))
			results <- outcome{v, c, e}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	creators := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			creators++
		}
		if !reflect.DeepEqual(result.record, clipImportFixture("shared")) {
			t.Fatalf("different claim: %+v", result.record)
		}
	}
	if creators != 1 {
		t.Fatalf("creators=%d", creators)
	}
}

func clipPublicError() *model.APIError {
	return &model.APIError{Code: string(model.ClipperRecoveryRequired), Message: "Manual recovery required", Field: "destination"}
}
func clipResult(v ClipImportRecord) *model.ClipperImportResult {
	return &model.ClipperImportResult{ImportID: v.ImportID, Base: v.BaseName, Path: v.NotePath, Revision: "original-revision", Warnings: []string{"external image"}}
}

func TestClipImportAllowedSequencesAndJournalCAS(t *testing.T) {
	sequences := [][]ClipImportState{
		{ClipImportPublishing, ClipImportSucceeded},
		{ClipImportRejected}, {ClipImportRecoveryRequired, ClipImportRejected},
		{ClipImportRecoveryRequired, ClipImportPublishing, ClipImportSucceeded},
		{ClipImportPublishing, ClipImportRejected},
		{ClipImportPublishing, ClipImportRecoveryRequired, ClipImportPublishing, ClipImportRecoveryRequired, ClipImportRejected},
	}
	for i, sequence := range sequences {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			db, _ := clipImportDB(t)
			r := NewClipImportRepository(db)
			v := mustClaimClip(t, r, "sequence")
			original := v
			checkpoint := json.RawMessage(`{"note":{"owned":true,"published":false},"staging_ready":true}`)
			if err := r.Checkpoint(context.Background(), v.ImportID, v.Version(), checkpoint); err != nil {
				t.Fatal(err)
			}
			if err := r.Checkpoint(context.Background(), v.ImportID, v.Version(), json.RawMessage(`{}`)); !errors.Is(err, ErrClipImportTransition) {
				t.Fatalf("stale checkpoint: %v", err)
			}
			v, _, _ = r.Get(context.Background(), v.ImportID)
			if !reflect.DeepEqual(v.Journal.Plan, original.Journal.Plan) || string(v.Journal.Checkpoint) != string(checkpoint) {
				t.Fatalf("journal: %+v", v.Journal)
			}
			for _, state := range sequence {
				previous := v
				var result *model.ClipperImportResult
				var public *model.APIError
				if state == ClipImportSucceeded {
					result = clipResult(v)
				}
				if state == ClipImportRejected || state == ClipImportRecoveryRequired {
					public = clipPublicError()
				}
				if err := r.Transition(context.Background(), v.ImportID, v.Version(), state, result, public); err != nil {
					t.Fatal(err)
				}
				if err := r.Transition(context.Background(), v.ImportID, previous.Version(), state, result, public); !errors.Is(err, ErrClipImportTransition) {
					t.Fatalf("stale transition: %v", err)
				}
				v, _, _ = r.Get(context.Background(), v.ImportID)
				if !v.UpdatedAt.After(previous.UpdatedAt) {
					t.Fatal("timestamp did not advance")
				}
				immutable := v
				immutable.State = original.State
				immutable.Result = nil
				immutable.Error = nil
				immutable.UpdatedAt = original.UpdatedAt
				immutable.Journal.Checkpoint = original.Journal.Checkpoint
				if !reflect.DeepEqual(immutable, original) {
					t.Fatalf("immutable plan changed: %+v", v)
				}
			}
			for _, state := range []ClipImportState{ClipImportPrepared, ClipImportPublishing, ClipImportSucceeded, ClipImportRejected, ClipImportRecoveryRequired} {
				if err := r.Transition(context.Background(), v.ImportID, v.Version(), state, nil, nil); err == nil {
					t.Fatalf("terminal transition allowed: %s", state)
				}
			}
			if err := r.Checkpoint(context.Background(), v.ImportID, v.Version(), json.RawMessage(`{"modified":true}`)); !errors.Is(err, ErrClipImportTransition) {
				t.Fatalf("terminal checkpoint: %v", err)
			}
		})
	}
}

func TestClipImportReceiptReopenIndependentOfConnectionsAndBinding(t *testing.T) {
	db, path := clipImportDB(t)
	r := NewClipImportRepository(db)
	ctx := context.Background()
	v := mustClaimClip(t, r, "receipt")
	if err := r.Transition(ctx, v.ImportID, v.Version(), ClipImportPublishing, nil, nil); err != nil {
		t.Fatal(err)
	}
	v, _, _ = r.Get(ctx, v.ImportID)
	if err := r.Transition(ctx, v.ImportID, v.Version(), ClipImportSucceeded, clipResult(v), nil); err != nil {
		t.Fatal(err)
	}
	want, _, _ := r.Get(ctx, v.ImportID)
	connections := NewClipperConnectionRepository(db)
	if err := connections.Create(ctx, model.ClipperConnection{ID: "old", Label: "old", CreatedAt: time.Now()}, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM clipper_connections`); err != nil {
		t.Fatal(err)
	}
	if err := connections.Create(ctx, model.ClipperConnection{ID: "new", Label: "new", CreatedAt: time.Now()}, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	r = NewClipImportRepository(reopened)
	replay := clipImportFixture("receipt")
	replay.BaseName = "renamed"
	replay.BasePath = "/rebound"
	replay.RootIdentity = "replacement"
	replay.DestinationRevision = "new"
	got, created, err := r.Claim(ctx, replay)
	if err != nil || created || !reflect.DeepEqual(got, want) {
		t.Fatalf("historical receipt: %+v %v %v", got, created, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("internal record leaks via JSON: %s", encoded)
	}
	encoded, _ = json.Marshal(got.Result)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "ownership") {
		t.Fatalf("result leaks: %s", encoded)
	}
}

func TestClipImportInvalidPreparedPayloads(t *testing.T) {
	cases := map[string]func(*ClipImportRecord){
		"empty id":                   func(v *ClipImportRecord) { v.ImportID = "" },
		"empty base":                 func(v *ClipImportRecord) { v.BaseName = "" },
		"empty root":                 func(v *ClipImportRecord) { v.BasePath = "" },
		"empty identity":             func(v *ClipImportRecord) { v.RootIdentity = "" },
		"empty destination revision": func(v *ClipImportRecord) { v.DestinationRevision = "" },
		"empty note path":            func(v *ClipImportRecord) { v.NotePath = "" },
		"short digest":               func(v *ClipImportRecord) { v.RequestDigest = "abc" },
		"uppercase digest":           func(v *ClipImportRecord) { v.RequestDigest = strings.Repeat("A", 64) },
		"nonhex digest":              func(v *ClipImportRecord) { v.NoteContentHash = strings.Repeat("z", 64) },
		"publishing":                 func(v *ClipImportRecord) { v.State = ClipImportPublishing },
		"success receipt":            func(v *ClipImportRecord) { v.Result = clipResult(*v) },
		"error receipt":              func(v *ClipImportRecord) { v.Error = clipPublicError() },
		"missing plan":               func(v *ClipImportRecord) { v.Journal.Plan = nil },
		"array plan":                 func(v *ClipImportRecord) { v.Journal.Plan = json.RawMessage(`[]`) },
		"null checkpoint":            func(v *ClipImportRecord) { v.Journal.Checkpoint = json.RawMessage(`null`) },
		"trailing json":              func(v *ClipImportRecord) { v.Journal.Plan = json.RawMessage(`{} {}`) },
		"zero creation":              func(v *ClipImportRecord) { v.CreatedAt = time.Time{} },
		"unrepresentable update":     func(v *ClipImportRecord) { v.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"backwards update":           func(v *ClipImportRecord) { v.UpdatedAt = v.CreatedAt.Add(-time.Second) },
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			db, _ := clipImportDB(t)
			r := NewClipImportRepository(db)
			v := clipImportFixture("invalid")
			modify(&v)
			if _, created, err := r.Claim(context.Background(), v); err == nil || created {
				t.Fatalf("invalid claim accepted: %v %v", created, err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM clip_imports`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid claim persisted: %d %v", count, err)
			}
		})
	}
}

func TestClipImportRejectsInvalidTransitionsAndReceiptsWithoutChanges(t *testing.T) {
	db, _ := clipImportDB(t)
	r := NewClipImportRepository(db)
	ctx := context.Background()
	v := mustClaimClip(t, r, "invalid-transitions")
	// A success receipt cannot skip publishing.
	if err := r.Transition(ctx, v.ImportID, v.Version(), ClipImportSucceeded, clipResult(v), nil); !errors.Is(err, ErrClipImportTransition) {
		t.Fatalf("skip publishing: %v", err)
	}
	if err := r.Transition(ctx, v.ImportID, v.Version(), ClipImportPublishing, nil, nil); err != nil {
		t.Fatal(err)
	}
	v, _, _ = r.Get(ctx, v.ImportID)
	cases := []struct {
		name   string
		state  ClipImportState
		result *model.ClipperImportResult
		public *model.APIError
	}{
		{"missing result", ClipImportSucceeded, nil, nil},
		{"mixed receipt", ClipImportSucceeded, clipResult(v), clipPublicError()},
		{"missing error", ClipImportRejected, nil, nil},
		{"missing recovery error", ClipImportRecoveryRequired, nil, nil},
		{"empty code", ClipImportRejected, nil, &model.APIError{Message: "safe"}},
		{"empty message", ClipImportRejected, nil, &model.APIError{Code: "safe"}},
		{"error with result", ClipImportRejected, clipResult(v), clipPublicError()},
		{"regression", ClipImportPrepared, nil, nil},
		{"same state", ClipImportPublishing, nil, nil},
		{"unknown state", ClipImportState("other"), nil, nil},
	}
	for _, field := range []string{"id", "base", "path", "revision"} {
		result := clipResult(v)
		switch field {
		case "id":
			result.ImportID = "other"
		case "base":
			result.Base = "other"
		case "path":
			result.Path = "other.md"
		case "revision":
			result.Revision = ""
		}
		cases = append(cases, struct {
			name   string
			state  ClipImportState
			result *model.ClipperImportResult
			public *model.APIError
		}{field, ClipImportSucceeded, result, nil})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.Transition(ctx, v.ImportID, v.Version(), tc.state, tc.result, tc.public); err == nil {
				t.Fatal("invalid transition accepted")
			}
			got, _, err := r.Get(ctx, v.ImportID)
			if err != nil || !reflect.DeepEqual(got, v) {
				t.Fatalf("receipt mutated: %+v %v", got, err)
			}
		})
	}
	for _, raw := range []string{"", `null`, `[]`, `true`, `{"unterminated":`, `{} {}`} {
		if err := r.Checkpoint(ctx, v.ImportID, v.Version(), json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid checkpoint accepted: %q", raw)
		}
	}
	// The APIError type contains only public fields; no error/cause is accepted.
	public := clipPublicError()
	if err := r.Transition(ctx, v.ImportID, v.Version(), ClipImportRecoveryRequired, nil, public); err != nil {
		t.Fatal(err)
	}
	got, _, err := r.Get(ctx, v.ImportID)
	if err != nil || !reflect.DeepEqual(got.Error, public) {
		t.Fatalf("public error roundtrip: %+v %v", got.Error, err)
	}
	// Recovery is not success until an explicit roll-forward enters publishing.
	if err := r.Transition(ctx, got.ImportID, got.Version(), ClipImportSucceeded, clipResult(got), nil); !errors.Is(err, ErrClipImportTransition) {
		t.Fatalf("recovery bypass: %v", err)
	}
}

func TestClipImportUnfinishedMissingAndCancellation(t *testing.T) {
	db, _ := clipImportDB(t)
	r := NewClipImportRepository(db)
	ctx := context.Background()
	got, found, err := r.Get(ctx, "missing")
	if err != nil || found || !reflect.DeepEqual(got, ClipImportRecord{}) {
		t.Fatalf("missing: %+v %v %v", got, found, err)
	}
	list, err := r.ListUnfinished(ctx)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty list: %+v %v", list, err)
	}
	for _, state := range []ClipImportState{ClipImportPrepared, ClipImportPublishing, ClipImportRecoveryRequired, ClipImportRejected, ClipImportSucceeded} {
		v := mustClaimClip(t, r, string(state))
		if state == ClipImportSucceeded || state == ClipImportPublishing {
			if err := r.Transition(ctx, v.ImportID, v.Version(), ClipImportPublishing, nil, nil); err != nil {
				t.Fatal(err)
			}
			v, _, _ = r.Get(ctx, v.ImportID)
		}
		if state == ClipImportSucceeded {
			err = r.Transition(ctx, v.ImportID, v.Version(), state, clipResult(v), nil)
		}
		if state == ClipImportRejected || state == ClipImportRecoveryRequired {
			err = r.Transition(ctx, v.ImportID, v.Version(), state, nil, clipPublicError())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err = r.ListUnfinished(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("unfinished: %+v %v", list, err)
	}
	for i, state := range []ClipImportState{ClipImportPrepared, ClipImportPublishing, ClipImportRecoveryRequired} {
		if list[i].State != state {
			t.Fatalf("order/states: %+v", list)
		}
	}
	v := clipImportFixture("missing")
	if err := r.Checkpoint(ctx, v.ImportID, v.Version(), json.RawMessage(`{}`)); !errors.Is(err, ErrClipImportTransition) {
		t.Fatalf("missing checkpoint: %v", err)
	}
	if err := r.Transition(ctx, v.ImportID, v.Version(), ClipImportPublishing, nil, nil); !errors.Is(err, ErrClipImportTransition) {
		t.Fatalf("missing transition: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := r.Get(canceled, "prepared"); !errors.Is(err, context.Canceled) {
		t.Fatalf("get cancellation: %v", err)
	}
	if _, err := r.ListUnfinished(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("list cancellation: %v", err)
	}
	if _, _, err := r.Claim(canceled, clipImportFixture("canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("claim cancellation: %v", err)
	}
	v, _, _ = r.Get(ctx, "prepared")
	if err := r.Checkpoint(canceled, v.ImportID, v.Version(), json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("checkpoint cancellation: %v", err)
	}
	if err := r.Transition(canceled, v.ImportID, v.Version(), ClipImportPublishing, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("transition cancellation: %v", err)
	}
	after, _, _ := r.Get(ctx, v.ImportID)
	if !reflect.DeepEqual(after, v) {
		t.Fatal("cancellation modified record")
	}
}

func TestClipImportConcurrentCheckpointAndTransitionCAS(t *testing.T) {
	db, path := clipImportDB(t)
	other, err := InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	repos := []*ClipImportRepository{NewClipImportRepository(db), NewClipImportRepository(other)}
	v := mustClaimClip(t, repos[0], "cas")
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	go func() {
		<-start
		outcomes <- repos[0].Checkpoint(context.Background(), v.ImportID, v.Version(), json.RawMessage(`{"owned":true}`))
	}()
	go func() {
		<-start
		outcomes <- repos[1].Transition(context.Background(), v.ImportID, v.Version(), ClipImportPublishing, nil, nil)
	}()
	close(start)
	success, stale := 0, 0
	for range 2 {
		err := <-outcomes
		if err == nil {
			success++
		} else if errors.Is(err, ErrClipImportTransition) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("CAS successes=%d stale=%d", success, stale)
	}
}

func TestClipImportTimestampUpperBound(t *testing.T) {
	maximum := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	for _, action := range []string{"checkpoint", "transition"} {
		for _, atLimit := range []bool{true, false} {
			name := action + "/last_representable_increment"
			if atLimit {
				name = action + "/overflow_rejected"
			}
			t.Run(name, func(t *testing.T) {
				db, _ := clipImportDB(t)
				r := NewClipImportRepository(db)
				ctx := context.Background()
				prepared := clipImportFixture("timestamp-boundary")
				prepared.UpdatedAt = maximum
				if !atLimit {
					prepared.UpdatedAt = maximum.Add(-time.Nanosecond)
				}
				before, created, err := r.Claim(ctx, prepared)
				if err != nil || !created {
					t.Fatalf("claim representable timestamp: created=%v err=%v", created, err)
				}
				checkpoint := json.RawMessage(`{"owned":true}`)
				if action == "checkpoint" {
					err = r.Checkpoint(ctx, before.ImportID, before.Version(), checkpoint)
				} else {
					err = r.Transition(ctx, before.ImportID, before.Version(), ClipImportPublishing, nil, nil)
				}
				if atLimit && err == nil {
					t.Error("unrepresentable timestamp update accepted")
				} else if !atLimit && err != nil {
					t.Fatalf("last representable increment rejected: %v", err)
				}
				want := before
				if !atLimit {
					want.UpdatedAt = maximum
					if action == "checkpoint" {
						want.Journal.Checkpoint = checkpoint
					} else {
						want.State = ClipImportPublishing
					}
				}
				after, found, err := r.Get(ctx, before.ImportID)
				if err != nil || !found || !reflect.DeepEqual(after, want) {
					t.Errorf("record must remain readable with expected contents: got=%+v found=%v err=%v", after, found, err)
				}
				unfinished, err := r.ListUnfinished(ctx)
				if err != nil || !reflect.DeepEqual(unfinished, []ClipImportRecord{want}) {
					t.Errorf("unfinished list must remain readable: got=%+v err=%v", unfinished, err)
				}
			})
		}
	}
}

func TestClipImportReadsWaitForSQLiteLockAndRespectCancellation(t *testing.T) {
	for _, action := range []string{"get", "list"} {
		t.Run(action, func(t *testing.T) {
			db, path := clipImportDB(t)
			r := NewClipImportRepository(db)
			ctx := context.Background()
			mustClaimClip(t, r, "locked")
			other, err := InitDB(path)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			conn, err := other.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
				t.Fatal(err)
			}
			defer conn.ExecContext(ctx, `ROLLBACK`)
			read := func(ctx context.Context) error {
				if action == "get" {
					_, _, err := r.Get(ctx, "locked")
					return err
				}
				_, err := r.ListUnfinished(ctx)
				return err
			}
			deadline, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
			defer cancel()
			if err := read(deadline); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("read should wait until cancellation, got %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- read(ctx) }()
			select {
			case err := <-done:
				t.Fatalf("read did not wait for lock release: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("read never resumed")
			}
		})
	}
}
