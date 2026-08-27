package repository

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// InitDB инициализирует подключение к SQLite и создает таблицы
func InitDB(dbPath string) (*sql.DB, error) {
	// Убедимся, что директория существует
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS notes (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		path TEXT NOT NULL,
		parent_id TEXT,
		type TEXT NOT NULL CHECK(type IN ('file', 'dir')),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS tags (
		note_id TEXT,
		tag TEXT,
		FOREIGN KEY(note_id) REFERENCES notes(id) ON DELETE CASCADE,
		UNIQUE(note_id, tag)
	);
	`
	if _, err = db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return db, nil
}
