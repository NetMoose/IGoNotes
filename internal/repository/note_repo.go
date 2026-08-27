package repository

import (
	"database/sql"

	"IGoNotes/internal/model"
)

type NoteRepository struct {
	db *sql.DB
}

func NewNoteRepository(db *sql.DB) *NoteRepository {
	return &NoteRepository{db: db}
}

// UpsertNode вставляет или обновляет узел (файл/папку) в базе данных
func (r *NoteRepository) UpsertNode(id, title, path string, parentID *string, nodeType string) error {
	query := `
	INSERT INTO notes (id, title, path, parent_id, type)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		title = excluded.title,
		path = excluded.path,
		parent_id = excluded.parent_id,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := r.db.Exec(query, id, title, path, parentID, nodeType)
	return err
}

// GetAllNodes получает все узлы (папки и файлы) для построения дерева
func (r *NoteRepository) GetAllNodes() ([]model.NoteNode, error) {
	rows, err := r.db.Query("SELECT id, title, path, type, parent_id FROM notes ORDER BY type ASC, title ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []model.NoteNode
	for rows.Next() {
		var node model.NoteNode
		var parentID sql.NullString
		if err := rows.Scan(&node.ID, &node.Name, &node.Path, &node.Type, &parentID); err != nil {
			return nil, err
		}

		if parentID.Valid {
			node.ParentID = parentID.String
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

func (r *NoteRepository) ReplaceAll(nodes []model.NoteNode) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM notes"); err != nil {
		return err
	}

	stmt, err := tx.Prepare("INSERT INTO notes (id, title, path, parent_id, type) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, node := range nodes {
		var parentID any
		if node.ParentID != "" {
			parentID = node.ParentID
		}
		if _, err := stmt.Exec(node.ID, node.Name, node.Path, parentID, node.Type); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ClearAll удаляет все записи (полезно при полной синхронизации, если мы не отслеживаем удаления точечно)
func (r *NoteRepository) ClearAll() error {
	_, err := r.db.Exec("DELETE FROM notes")
	return err
}

// DeleteNode удаляет узел из базы данных по его ID
func (r *NoteRepository) DeleteNode(id string) error {
	// Т.к. включены внешние ключи (теоретически, хотя мы их не включали прагмой),
	// или просто каскадно удаляем. SQLite по умолчанию не включает foreign keys.
	// Удалим сам узел, и все дочерние узлы (используя LIKE или рекурсивно).
	// В простом случае, если мы удаляем директорию физически, при следующей синхронизации все очистится.
	// Но чтобы UI реагировал сразу, удалим все, чей ID начинается с удаляемого пути.
	_, err := r.db.Exec("DELETE FROM notes WHERE id = ? OR id LIKE ?", id, id+"/%")
	return err
}
