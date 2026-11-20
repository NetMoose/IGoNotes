package repository

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInitDBMigrationsFreshDatabaseInOrder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	if got := migrationVersions(t, db); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("migration versions = %v, want [1 2 3 4 5]", got)
	}

	var indexName string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = ? AND name = ?", "index", "git_status_base_name_idx").Scan(&indexName); err != nil {
		t.Fatalf("git status base-name index error = %v", err)
	}
	assertGitOperationSchema(t, db)
	assertClipperSchema(t, db)
}

func TestInitDBUpgradesLegacySchemaWithoutDataLoss(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db := openRawDB(t, dbPath)
	if _, err := db.Exec(`
		CREATE TABLE notes (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			path TEXT NOT NULL,
			parent_id TEXT,
			type TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE tags (
			note_id TEXT,
			tag TEXT,
			FOREIGN KEY(note_id) REFERENCES notes(id) ON DELETE CASCADE,
			UNIQUE(note_id, tag)
		);
		INSERT INTO notes (id, title, path, parent_id, type, created_at, updated_at)
		VALUES ('legacy.md', 'Legacy', 'legacy.md', NULL, 'file', '2001-02-03 04:05:06', '2007-08-09 10:11:12');
		INSERT INTO tags (note_id, tag) VALUES ('legacy.md', 'preserved');
	`); err != nil {
		t.Fatalf("create legacy database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	if got := migrationVersions(t, db); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Errorf("migration versions = %v, want [1 2 3 4 5]", got)
	}
	assertGitOperationSchema(t, db)
	wantNotes := [][]string{{"legacy.md", "Legacy", "legacy.md", "", "file", "'2001-02-03 04:05:06'", "'2007-08-09 10:11:12'"}}
	if got := repositoryRows(t, db, "SELECT id, title, path, COALESCE(parent_id, ''), type, quote(created_at), quote(updated_at) FROM notes", 7); !reflect.DeepEqual(got, wantNotes) {
		t.Errorf("legacy notes = %#v, want %#v", got, wantNotes)
	}
	if got := repositoryRows(t, db, "SELECT note_id, tag FROM tags", 2); !reflect.DeepEqual(got, [][]string{{"legacy.md", "preserved"}}) {
		t.Errorf("legacy tags = %#v, want preserved tag", got)
	}
}

func TestInitDBRerunDoesNotDuplicateVersions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	for run := 1; run <= 2; run++ {
		db, err := InitDB(dbPath)
		if err != nil {
			t.Fatalf("InitDB() run %d error = %v", run, err)
		}
		if got := migrationVersions(t, db); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
			t.Errorf("run %d migration versions = %v, want [1 2 3 4 5]", run, got)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close run %d: %v", run, err)
		}
	}
}

func TestInitDBMigrationFailureRollsBackSchemaAndVersion(t *testing.T) {
	original := migrations
	migrations = append(append([]migration(nil), migrations...), migration{
		version: 6,
		sql: `
			CREATE TABLE migration_six_marker (id INTEGER PRIMARY KEY);
			INSERT INTO table_that_does_not_exist (id) VALUES (1);
		`,
	})
	defer func() { migrations = original }()

	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := InitDB(dbPath)
	if err == nil {
		if db != nil {
			db.Close()
		}
		t.Fatal("InitDB() error = nil, want migration failure")
	}
	if db != nil {
		db.Close()
		t.Fatal("InitDB() database is non-nil after migration failure")
	}

	reopened := openRawDB(t, dbPath)
	defer reopened.Close()
	if got := migrationVersions(t, reopened); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Errorf("migration versions after failure = %v, want [1 2 3 4 5]", got)
	}
	assertGitOperationSchema(t, reopened)
	var markerCount int
	if err := reopened.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?", "table", "migration_six_marker").Scan(&markerCount); err != nil {
		t.Fatalf("query migration marker: %v", err)
	}
	if markerCount != 0 {
		t.Errorf("migration marker tables = %d, want 0", markerCount)
	}
}

func assertClipperSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for name, kind := range map[string]string{
		"clipper_connections":     "table",
		"clip_imports":            "table",
		"clip_imports_unfinished": "index",
	} {
		var got string
		if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = ? AND name = ?", kind, name).Scan(&got); err != nil {
			t.Errorf("clipper schema object %q: %v", name, err)
		}
	}
}

func TestInitDBUpgradesVersionFourAndPreservesRowsOnReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db := openRawDB(t, dbPath)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	// Apply only the real historical v1..v4 SQL, without invoking today's InitDB.
	for _, m := range migrations[:4] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("create v%d fixture: %v", m.version, err)
		}
		if _, err := db.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO notes (id, title, path, type) VALUES ('legacy.md', 'Legacy', 'legacy.md', 'file');
		INSERT INTO tags (note_id, tag) VALUES ('legacy.md', 'preserved');
		INSERT INTO git_status (repository_path, base_name, state, operation_id, changed_paths_json, updated_at_unix_ms)
		VALUES ('/legacy', 'legacy', 'paused', 'old-operation', '["legacy.md"]', 12345);
		INSERT INTO git_operations (operation_id, base_name, repo_path, config_fingerprint, remote_fingerprint, kind, state, stage, branch, created_at, updated_at)
		VALUES ('old-operation', 'legacy', '/legacy', 'config', 'remote', 'conflict_abort', 'succeeded', 'done', 'main', '2026-10-20T00:00:00Z', '2026-10-20T00:01:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	want := make(map[string][][]string)
	queries := map[string]int{
		"SELECT id, title, path, COALESCE(parent_id, ''), type, quote(created_at), quote(updated_at) FROM notes": 7,
		"SELECT note_id, tag FROM tags": 2,
		"SELECT * FROM git_status":      16,
		"SELECT * FROM git_operations":  22,
		"SELECT version, quote(applied_at) FROM schema_migrations ORDER BY version": 2,
	}
	for query, columns := range queries {
		// Nullable Git timestamps are normalized for the string-row helper.
		if strings.Contains(query, "git_status") {
			if _, err := db.Exec("UPDATE git_status SET last_attempt_unix_ms = 100, last_success_unix_ms = 99"); err != nil {
				t.Fatal(err)
			}
		}
		want[query] = repositoryRows(t, db, query, columns)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		var err error
		db, err = InitDB(dbPath)
		if err != nil {
			t.Fatalf("startup %d: %v", run, err)
		}
		if got := migrationVersions(t, db); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
			t.Errorf("startup %d versions = %v", run, got)
		}
		assertClipperSchema(t, db)
		for query, columns := range queries {
			actualQuery := query
			if strings.Contains(query, "FROM schema_migrations") {
				actualQuery = "SELECT version, quote(applied_at) FROM schema_migrations WHERE version <= 4 ORDER BY version"
			}
			if got := repositoryRows(t, db, actualQuery, columns); !reflect.DeepEqual(got, want[query]) {
				t.Errorf("startup %d changed legacy rows for %s: got %#v, want %#v", run, query, got, want[query])
			}
		}
		if run == 1 {
			insertClipperFixture(t, db)
		} else {
			var journal, result, digest, revoked string
			if err := db.QueryRow("SELECT journal_json, result_json FROM clip_imports WHERE import_id = 'import-1'").Scan(&journal, &result); err != nil {
				t.Fatal(err)
			}
			if journal != clipperTestJournal || result != clipperTestResult {
				t.Errorf("receipt changed on reopen: journal=%s result=%s", journal, result)
			}
			if err := db.QueryRow("SELECT secret_digest, revoked_at FROM clipper_connections WHERE id = 'connection-1'").Scan(&digest, &revoked); err != nil {
				t.Fatal(err)
			}
			if digest != strings.Repeat("a", 64) || revoked != "2026-10-21T01:00:00Z" {
				t.Errorf("connection changed: digest=%q revoked=%q", digest, revoked)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

const clipperTestJournal = `{"note":{"path":"clips/page.md","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","owned":true,"staging":"/private/staging/import-1/note"},"assets":[{"path":"assets/images/page.png","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","owned":false,"staging":"/private/staging/import-1/image"}]}`
const clipperTestResult = `{"import_id":"import-1","base":"original","path":"clips/page.md","revision":"revision-1","warnings":[]}`

func insertClipperFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO clipper_connections (id, label, secret_digest, created_at, revoked_at) VALUES ('connection-1', 'Browser', ?, '2026-10-21T00:00:00Z', '2026-10-21T01:00:00Z')`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO clip_imports (import_id, request_digest, base_name, base_path, root_identity, destination_revision, note_path, note_content_hash, journal_json, state, result_json, created_at, updated_at)
		VALUES ('import-1', ?, 'original', '/original/base', 'device:inode', 'destination-1', 'clips/page.md', ?, ?, 'succeeded', ?, '2026-10-21T00:00:00Z', '2026-10-21T00:01:00Z')`, strings.Repeat("a", 64), strings.Repeat("b", 64), clipperTestJournal, clipperTestResult); err != nil {
		t.Fatal(err)
	}
}

func TestClipperMigrationConstraints(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertClipperFixture(t, db)
	for _, state := range []string{"prepared", "publishing", "succeeded", "rejected", "recovery_required"} {
		t.Run(state, func(t *testing.T) {
			if _, err := db.Exec("UPDATE clip_imports SET state = ?, error_code = 'clipper_recovery_required', error_message = 'Import requires recovery' WHERE import_id = 'import-1'", state); err != nil {
				t.Fatalf("valid durable state %s: %v", state, err)
			}
		})
	}
	for _, test := range []struct{ name, query string }{
		{"unknown state", "UPDATE clip_imports SET state = 'unknown'"},
		{"wire pending is not durable state", "UPDATE clip_imports SET state = 'pending'"},
		{"null state", "UPDATE clip_imports SET state = NULL"},
		{"null import ID", "UPDATE clip_imports SET import_id = NULL"},
		{"empty import ID", "UPDATE clip_imports SET import_id = ''"},
		{"invalid request hash", "UPDATE clip_imports SET request_digest = 'plaintext'"},
		{"nonhex request hash", "UPDATE clip_imports SET request_digest = 'gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg'"},
		{"invalid content hash", "UPDATE clip_imports SET note_content_hash = 'short'"},
		{"missing journal", "UPDATE clip_imports SET journal_json = NULL"},
		{"malformed journal", "UPDATE clip_imports SET journal_json = '{'"},
		{"nonobject journal", "UPDATE clip_imports SET journal_json = '[]'"},
		{"malformed result", "UPDATE clip_imports SET result_json = '{'"},
		{"nonobject result", "UPDATE clip_imports SET result_json = 'null'"},
		{"missing success receipt", "UPDATE clip_imports SET state = 'succeeded', result_json = NULL"},
		{"missing rejection error", "UPDATE clip_imports SET state = 'rejected', error_code = ''"},
		{"missing recovery error", "UPDATE clip_imports SET state = 'recovery_required', error_message = ''"},
		{"plaintext connection secret", "UPDATE clipper_connections SET secret_digest = 'secret'"},
		{"null connection ID", "UPDATE clipper_connections SET id = NULL"},
		{"null connection digest", "UPDATE clipper_connections SET secret_digest = NULL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.Exec(test.query); err == nil {
				t.Errorf("invalid data accepted: %s", test.query)
			}
		})
	}
	// A claim is global, even for a different payload or destination.
	if _, err := db.Exec(`INSERT INTO clip_imports SELECT import_id, ?, 'renamed', '/replacement', root_identity, destination_revision, note_path, note_content_hash, journal_json, state, result_json, error_code, error_message, error_field, created_at, updated_at FROM clip_imports`, strings.Repeat("d", 64)); err == nil {
		t.Error("duplicate import_id with changed digest/destination accepted")
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM clipper_connections"); err != nil {
		t.Fatalf("receipt must not depend on token lifetime: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM clip_imports").Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipt after token deletion: count=%d err=%v", count, err)
	}
}

func TestInitDBRejectsMalformedMigrationHistoriesWithoutChanges(t *testing.T) {
	tests := []struct {
		name     string
		versions []int
	}{
		{name: "missing first version", versions: []int{2}},
		{name: "gap and unknown version", versions: []int{1, 3}},
		{name: "version above highest", versions: []int{1, 2, 99}},
		{name: "nonpositive version", versions: []int{0}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "metadata.db")
			db := openRawDB(t, dbPath)
			if _, err := db.Exec(`
				CREATE TABLE schema_migrations (
					version INTEGER PRIMARY KEY,
					applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
				);
				CREATE TABLE history_marker (value TEXT NOT NULL);
				INSERT INTO history_marker (value) VALUES ('unchanged');
			`); err != nil {
				t.Fatalf("create migration history: %v", err)
			}
			for _, version := range test.versions {
				if _, err := db.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
					t.Fatalf("insert migration version %d: %v", version, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close malformed database: %v", err)
			}

			initialized, err := InitDB(dbPath)
			if err == nil || !strings.Contains(err.Error(), "invalid schema migration history") {
				t.Fatalf("InitDB() error = %v, want invalid schema migration history", err)
			}
			if initialized != nil {
				initialized.Close()
				t.Fatal("InitDB() database is non-nil for malformed history")
			}

			reopened := openRawDB(t, dbPath)
			defer reopened.Close()
			if got := migrationVersions(t, reopened); !reflect.DeepEqual(got, test.versions) {
				t.Errorf("versions after rejection = %v, want unchanged %v", got, test.versions)
			}
			var marker string
			if err := reopened.QueryRow("SELECT value FROM history_marker").Scan(&marker); err != nil {
				t.Fatalf("read history marker: %v", err)
			}
			if marker != "unchanged" {
				t.Errorf("history marker = %q, want unchanged", marker)
			}
			var notesCount int
			if err := reopened.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = ? AND name IN (?, ?, ?)", "table", "notes", "tags", "git_status").Scan(&notesCount); err != nil {
				t.Fatalf("query migrated tables: %v", err)
			}
			if notesCount != 0 {
				t.Errorf("migrated tables after rejection = %d, want 0", notesCount)
			}
		})
	}
}

func TestInitDBRejectsInvalidMigrationDefinitions(t *testing.T) {
	original := migrations
	migrations = []migration{{version: 1, sql: "SELECT 1"}, {version: 3, sql: "SELECT 1"}}
	defer func() { migrations = original }()

	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := InitDB(dbPath)
	if err == nil || !strings.Contains(err.Error(), "invalid schema migration history") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("InitDB() error = %v, want invalid schema migration history", err)
	}
	if db != nil {
		db.Close()
		t.Fatal("InitDB() database is non-nil for invalid migration definitions")
	}

	reopened := openRawDB(t, dbPath)
	defer reopened.Close()
	var schemaObjects int
	if err := reopened.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&schemaObjects); err != nil {
		t.Fatalf("inspect rejected database schema: %v", err)
	}
	if schemaObjects != 0 {
		t.Errorf("schema objects after invalid migration definitions = %d, want 0", schemaObjects)
	}
}

func openRawDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	return db
}

func migrationVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("query migration versions: %v", err)
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatalf("scan migration version: %v", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("migration version rows: %v", err)
	}
	return versions
}

func assertGitOperationSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	want := map[string]string{
		"git_operations":                 "table",
		"git_operations_one_active_path": "index",
		"git_operations_unfinished":      "index",
	}
	for name, objectType := range want {
		var got string
		if err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type = ? AND name = ?",
			objectType,
			name,
		).Scan(&got); err != nil {
			t.Errorf("Git operation schema object %q error = %v", name, err)
		}
	}
}
