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

	if got := migrationVersions(t, db); !reflect.DeepEqual(got, []int{1, 2, 3, 4}) {
		t.Fatalf("migration versions = %v, want [1 2 3 4]", got)
	}

	var indexName string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = ? AND name = ?", "index", "git_status_base_name_idx").Scan(&indexName); err != nil {
		t.Fatalf("git status base-name index error = %v", err)
	}
	assertGitOperationSchema(t, db)
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

	if got := migrationVersions(t, db); !reflect.DeepEqual(got, []int{1, 2, 3, 4}) {
		t.Errorf("migration versions = %v, want [1 2 3 4]", got)
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
		if got := migrationVersions(t, db); !reflect.DeepEqual(got, []int{1, 2, 3, 4}) {
			t.Errorf("run %d migration versions = %v, want [1 2 3 4]", run, got)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close run %d: %v", run, err)
		}
	}
}

func TestInitDBMigrationFailureRollsBackSchemaAndVersion(t *testing.T) {
	original := migrations
	migrations = append(append([]migration(nil), migrations...), migration{
		version: 5,
		sql: `
			CREATE TABLE migration_four_marker (id INTEGER PRIMARY KEY);
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
	if got := migrationVersions(t, reopened); !reflect.DeepEqual(got, []int{1, 2, 3, 4}) {
		t.Errorf("migration versions after failure = %v, want [1 2 3 4]", got)
	}
	assertGitOperationSchema(t, reopened)
	var markerCount int
	if err := reopened.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?", "table", "migration_four_marker").Scan(&markerCount); err != nil {
		t.Fatalf("query migration marker: %v", err)
	}
	if markerCount != 0 {
		t.Errorf("migration marker tables = %d, want 0", markerCount)
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
