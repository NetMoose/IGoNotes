package repository

import (
	"database/sql"
	"errors"
	"sync"

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
	commit, rollback, operationErr, rollbackErr := r.BeginReplaceAll(nodes)
	if operationErr != nil {
		return errors.Join(operationErr, rollbackErr)
	}
	if err := commit(); err != nil {
		return errors.Join(err, rollback())
	}
	return nil
}

func (r *NoteRepository) BeginReplaceAll(nodes []model.NoteNode) (commit func() error, rollback func() error, operationErr error, rollbackErr error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, nil, err, nil
	}

	if _, err := tx.Exec("DELETE FROM notes"); err != nil {
		return rollbackReplacePreparation(tx, nil, err)
	}

	stmt, err := tx.Prepare("INSERT INTO notes (id, title, path, parent_id, type) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return rollbackReplacePreparation(tx, nil, err)
	}

	for _, node := range nodes {
		var parentID any
		if node.ParentID != "" {
			parentID = node.ParentID
		}
		if _, err := stmt.Exec(node.ID, node.Name, node.Path, parentID, node.Type); err != nil {
			return rollbackReplacePreparation(tx, stmt, err)
		}
	}
	if err := stmt.Close(); err != nil {
		return nil, nil, err, tx.Rollback()
	}

	finalizer := &replaceFinalizer{tx: tx}
	return finalizer.commit, finalizer.rollback, nil, nil
}

func rollbackReplacePreparation(tx *sql.Tx, stmt *sql.Stmt, operationErr error) (func() error, func() error, error, error) {
	if stmt != nil {
		operationErr = errors.Join(operationErr, stmt.Close())
	}
	return nil, nil, operationErr, tx.Rollback()
}

type replaceFinalizer struct {
	mu             sync.Mutex
	tx             *sql.Tx
	commitCalled   bool
	commitErr      error
	rollbackCalled bool
	rollbackErr    error
}

func (f *replaceFinalizer) commit() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commitCalled {
		return f.commitErr
	}
	if f.rollbackCalled {
		return sql.ErrTxDone
	}
	f.commitCalled = true
	f.commitErr = f.tx.Commit()
	return f.commitErr
}

func (f *replaceFinalizer) rollback() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rollbackCalled {
		return f.rollbackErr
	}
	if f.commitCalled && f.commitErr == nil {
		return sql.ErrTxDone
	}
	f.rollbackCalled = true
	f.rollbackErr = f.tx.Rollback()
	return f.rollbackErr
}

// ClearAll удаляет все записи (полезно при полной синхронизации, если мы не отслеживаем удаления точечно)
func (r *NoteRepository) ClearAll() error {
	_, err := r.db.Exec("DELETE FROM notes")
	return err
}

// DeleteNode удаляет узел из базы данных по его ID
func (r *NoteRepository) DeleteNode(id string) error {
	_, err := r.db.Exec(
		"DELETE FROM notes WHERE id = ? OR substr(id, 1, length(?) + 1) = ? || '/'",
		id, id, id,
	)
	return err
}
