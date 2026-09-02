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
