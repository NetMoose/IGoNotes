package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		sql: `
			CREATE TABLE IF NOT EXISTS notes (
				id TEXT PRIMARY KEY,
				title TEXT NOT NULL,
				path TEXT NOT NULL,
				parent_id TEXT,
				type TEXT NOT NULL,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);

			CREATE TABLE IF NOT EXISTS tags (
				note_id TEXT,
				tag TEXT,
				FOREIGN KEY(note_id) REFERENCES notes(id) ON DELETE CASCADE,
				UNIQUE(note_id, tag)
			);
		`,
	},
	{
		version: 2,
		sql: `
			CREATE TABLE git_status (
				repository_path TEXT PRIMARY KEY,
				base_name TEXT NOT NULL,
				state TEXT NOT NULL CHECK (state IN ('unconfigured', 'initializing', 'ready', 'syncing', 'error', 'paused', 'conflict', 'needs_reconnect')),
				operation_id TEXT NOT NULL DEFAULT '',
				stage TEXT NOT NULL DEFAULT '',
				ahead INTEGER NOT NULL DEFAULT 0 CHECK (ahead >= 0),
				behind INTEGER NOT NULL DEFAULT 0 CHECK (behind >= 0),
				consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
				last_attempt_unix_ms INTEGER,
				last_success_unix_ms INTEGER,
				changed_paths_json TEXT NOT NULL DEFAULT '[]',
				remote_oid TEXT NOT NULL DEFAULT '',
				error_code TEXT NOT NULL DEFAULT '',
				error_message TEXT NOT NULL DEFAULT '',
				error_field TEXT NOT NULL DEFAULT '',
				updated_at_unix_ms INTEGER NOT NULL
			);
			CREATE INDEX git_status_base_name_idx ON git_status(base_name);
		`,
	},
	{
		version: 3,
		sql: `
			CREATE TABLE git_operations (
				operation_id TEXT PRIMARY KEY,
				base_name TEXT NOT NULL,
				repo_path TEXT NOT NULL,
				config_fingerprint TEXT NOT NULL,
				remote_fingerprint TEXT NOT NULL,
				kind TEXT NOT NULL CHECK (kind IN ('initialize', 'sync')),
				state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'conflict')),
				stage TEXT NOT NULL,
				branch TEXT NOT NULL,
				backup_ref TEXT NOT NULL DEFAULT '',
				local_oid TEXT NOT NULL DEFAULT '',
				candidate_oid TEXT NOT NULL DEFAULT '',
				remote_oid TEXT NOT NULL DEFAULT '',
				push_oid TEXT NOT NULL DEFAULT '',
				changed_paths_json TEXT NOT NULL DEFAULT '[]',
				conflict_paths_json TEXT NOT NULL DEFAULT '[]',
				error_code TEXT NOT NULL DEFAULT '',
				error_message TEXT NOT NULL DEFAULT '',
				error_field TEXT NOT NULL DEFAULT '',
				error_exit_code INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			);

			CREATE UNIQUE INDEX git_operations_one_active_path
			ON git_operations(repo_path)
			WHERE state IN ('queued', 'running');

			CREATE INDEX git_operations_unfinished
			ON git_operations(state, updated_at)
			WHERE state IN ('queued', 'running');
		`,
	},
	{
		version: 4,
		sql: `
			ALTER TABLE git_operations RENAME TO git_operations_plan3;
			DROP INDEX git_operations_one_active_path;
			DROP INDEX git_operations_unfinished;
			CREATE TABLE git_operations (
				operation_id TEXT PRIMARY KEY, base_name TEXT NOT NULL, repo_path TEXT NOT NULL,
				config_fingerprint TEXT NOT NULL, remote_fingerprint TEXT NOT NULL,
				kind TEXT NOT NULL CHECK (kind IN ('initialize', 'sync', 'conflict_complete', 'conflict_abort')),
				state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'conflict')),
				stage TEXT NOT NULL, branch TEXT NOT NULL, backup_ref TEXT NOT NULL DEFAULT '', local_oid TEXT NOT NULL DEFAULT '', candidate_oid TEXT NOT NULL DEFAULT '', remote_oid TEXT NOT NULL DEFAULT '', push_oid TEXT NOT NULL DEFAULT '', changed_paths_json TEXT NOT NULL DEFAULT '[]', conflict_paths_json TEXT NOT NULL DEFAULT '[]', error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '', error_field TEXT NOT NULL DEFAULT '', error_exit_code INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			);
			INSERT INTO git_operations SELECT * FROM git_operations_plan3;
			DROP TABLE git_operations_plan3;
			CREATE UNIQUE INDEX git_operations_one_active_path ON git_operations(repo_path) WHERE state IN ('queued', 'running');
			CREATE INDEX git_operations_unfinished ON git_operations(state, updated_at) WHERE state IN ('queued', 'running');
		`,
	},
	{
		version: 5,
		sql: `
			CREATE TABLE clipper_connections (
				id TEXT NOT NULL PRIMARY KEY CHECK (length(id) > 0),
				label TEXT NOT NULL CHECK (length(label) > 0),
				secret_digest TEXT NOT NULL UNIQUE
					CHECK (length(secret_digest) = 64 AND secret_digest NOT GLOB '*[^0-9a-f]*'),
				created_at TEXT NOT NULL CHECK (length(created_at) > 0),
				revoked_at TEXT CHECK (revoked_at IS NULL OR length(revoked_at) > 0)
			);

			-- Receipts are global by import_id, independent of connection lifetime
			-- and subsequent base renames, root replacements or configuration changes.
			CREATE TABLE clip_imports (
				import_id TEXT NOT NULL PRIMARY KEY CHECK (length(import_id) > 0),
				request_digest TEXT NOT NULL
					CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
				base_name TEXT NOT NULL CHECK (length(base_name) > 0),
				base_path TEXT NOT NULL CHECK (length(base_path) > 0),
				root_identity TEXT NOT NULL CHECK (length(root_identity) > 0),
				destination_revision TEXT NOT NULL CHECK (length(destination_revision) > 0),
				note_path TEXT NOT NULL CHECK (length(note_path) > 0),
				note_content_hash TEXT NOT NULL
					CHECK (length(note_content_hash) = 64 AND note_content_hash NOT GLOB '*[^0-9a-f]*'),
				-- The complete plan (note/assets paths, hashes, ownership and private
				-- disk staging references) must be committed before filesystem writes.
				-- No blob data belongs here. Journal shape is owned by the importer.
				journal_json TEXT NOT NULL
					CHECK (json_valid(journal_json) AND json_type(journal_json) = 'object'),
				state TEXT NOT NULL
					CHECK (state IN ('prepared', 'publishing', 'succeeded', 'rejected', 'recovery_required')),
				result_json TEXT
					CHECK (result_json IS NULL OR (json_valid(result_json) AND json_type(result_json) = 'object')),
				-- Persist only safe public errors; private diagnostics stay out of receipts.
				error_code TEXT NOT NULL DEFAULT '',
				error_message TEXT NOT NULL DEFAULT '',
				error_field TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL CHECK (length(created_at) > 0),
				updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
				CHECK (state != 'succeeded' OR result_json IS NOT NULL),
				CHECK (state NOT IN ('rejected', 'recovery_required') OR (length(error_code) > 0 AND length(error_message) > 0))
			);

			CREATE INDEX clip_imports_unfinished ON clip_imports(state, updated_at)
			WHERE state IN ('prepared', 'publishing', 'recovery_required');
		`,
	},
}

// InitDB инициализирует подключение к SQLite и создает таблицы
func InitDB(dbPath string) (*sql.DB, error) {
	// Убедимся, что директория существует
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	for index, migration := range migrations {
		if migration.version != index+1 {
			return nil, fmt.Errorf("invalid schema migration history: migration version %d at position %d", migration.version, index+1)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	fail := func(err error) (*sql.DB, error) {
		return nil, errors.Join(err, db.Close())
	}

	if _, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fail(fmt.Errorf("failed to initialize schema migrations: %w", err))
	}

	rows, err := db.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return fail(fmt.Errorf("failed to read schema migration history: %w", err))
	}
	var appliedVersions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fail(fmt.Errorf("failed to read schema migration history: %w", err))
		}
		appliedVersions = append(appliedVersions, version)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fail(fmt.Errorf("failed to read schema migration history: %w", err))
	}
	if err := rows.Close(); err != nil {
		return fail(fmt.Errorf("failed to read schema migration history: %w", err))
	}

	for index, version := range appliedVersions {
		if index >= len(migrations) || version != index+1 {
			return fail(fmt.Errorf("invalid schema migration history: version %d at position %d", version, index+1))
		}
	}

	for _, migration := range migrations[len(appliedVersions):] {
		tx, err := db.Begin()
		if err != nil {
			return fail(fmt.Errorf("failed to apply schema migration %d: %w", migration.version, err))
		}
		if _, err := tx.Exec(migration.sql); err != nil {
			return fail(fmt.Errorf("failed to apply schema migration %d: %w", migration.version, errors.Join(err, tx.Rollback())))
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", migration.version); err != nil {
			return fail(fmt.Errorf("failed to record schema migration %d: %w", migration.version, errors.Join(err, tx.Rollback())))
		}
		if err := tx.Commit(); err != nil {
			return fail(fmt.Errorf("failed to apply schema migration %d: %w", migration.version, err))
		}
	}

	return db, nil
}
