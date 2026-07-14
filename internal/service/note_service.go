package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"path/filepath"
	"strings"
	"sync"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

// ErrAlreadyExists возвращается, если файл или папка с таким именем уже существует
var ErrAlreadyExists = errors.New("file or directory already exists")

type NoteService struct {
	repo     *repository.NoteRepository
	basePath string
	syncMu   sync.Mutex
}

// NewNoteService создает новый экземпляр NoteService
func NewNoteService(repo *repository.NoteRepository, basePath string) *NoteService {
	return &NoteService{repo: repo, basePath: basePath}
}

// SyncFS сканирует файловую систему и обновляет SQLite
func (s *NoteService) SyncFS() error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	if s.basePath == "" {
		return nil // Нет базы для синхронизации
	}

	// Очищаем старые записи (в идеале нужно делать смарт-синк, но для прототипа сойдет очистка)
	if err := s.repo.ClearAll(); err != nil {
		return err
	}

	err := filepath.Walk(s.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Пропускаем скрытые папки (.git) и assets
		if info.IsDir() && (strings.HasPrefix(info.Name(), ".") || info.Name() == "assets") {
			return filepath.SkipDir
		}

		// Игнорируем сам корень
		if path == s.basePath {
			return nil
		}

		// Вычисляем относительный путь (он же ID)
		relPath, err := filepath.Rel(s.basePath, path)
		if err != nil {
			return err
		}

		// Мы обрабатываем только папки и .md файлы
		isDir := info.IsDir()
		if !isDir && !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		id := relPath
		title := info.Name()
		if !isDir {
			title = strings.TrimSuffix(title, filepath.Ext(title)) // Убираем .md
		}

		nodeType := "file"
		if isDir {
			nodeType = "dir"
		}

		var parentID *string
		parentDir := filepath.Dir(relPath)
		if parentDir != "." && parentDir != "" {
			parentID = &parentDir
		}

		return s.repo.UpsertNode(id, title, relPath, parentID, nodeType)
	})

	return err
}

// GetTree возвращает иерархическое дерево заметок
func (s *NoteService) GetTree() ([]model.NoteNode, error) {
	// 1. Сначала синхронизируем ФС (при реальной работе это можно делать асинхронно или по кнопке)
	if err := s.SyncFS(); err != nil {
		return nil, err
	}

	// 2. Получаем плоский список из БД
	flatNodes, err := s.repo.GetAllNodes()
	if err != nil {
		return nil, err
	}

	// 3. Строим дерево
	nodeMap := make(map[string][]model.NoteNode) // parentID -> list of children

	// Распределяем узлы по их родителям
	for i := range flatNodes {
		pID := flatNodes[i].ParentID
		nodeMap[pID] = append(nodeMap[pID], flatNodes[i])
	}

	// Рекурсивная функция сборки дерева
	var buildTree func(parentID string) []model.NoteNode
	buildTree = func(parentID string) []model.NoteNode {
		children := nodeMap[parentID]
		for i := range children {
			// Для каждого ребенка рекурсивно ищем его детей
			children[i].Children = buildTree(children[i].ID)
		}
		return children
	}

	finalRoot := buildTree("")

	return finalRoot, nil
}

// GetNoteContent читает содержимое заметки с диска
func (s *NoteService) GetNoteContent(id string) (string, error) {
	if s.basePath == "" {
		return "", os.ErrNotExist
	}
	
	// id - это относительный путь к файлу
	fullPath := filepath.Join(s.basePath, id)
	
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// GetAbsoluteFilePath возвращает полный абсолютный путь к файлу в базе
func (s *NoteService) GetAbsoluteFilePath(relPath string) (string, error) {
	if s.basePath == "" {
		return "", os.ErrNotExist
	}
	// Очищаем путь, чтобы предотвратить path traversal
	cleanPath := filepath.Clean(relPath)
	fullPath := filepath.Join(s.basePath, cleanPath)
	
	// Проверяем, что итоговый путь находится внутри basePath
	if !strings.HasPrefix(fullPath, s.basePath) {
		return "", os.ErrPermission
	}
	
	return fullPath, nil
}

// SaveNoteContent сохраняет содержимое заметки на диск
func (s *NoteService) SaveNoteContent(id string, content string) error {
	if s.basePath == "" {
		return os.ErrNotExist
	}
	
	fullPath := filepath.Join(s.basePath, id)
	
	// Создаем директорию, если она вдруг не существует
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return err
	}
	
	return nil
}

// CreateNode создает новый файл или папку
func (s *NoteService) CreateNode(parentID, name, nodeType string) (*model.NoteNode, error) {
	if s.basePath == "" {
		return nil, os.ErrNotExist
	}

	// Защита от выхода за пределы базы
	name = filepath.Base(name) 
	
	if nodeType == "file" && !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}

	relPath := name
	if parentID != "" {
		relPath = filepath.Join(parentID, name)
	}
	
	fullPath := filepath.Join(s.basePath, relPath)

	title := name
	if nodeType == "file" {
		title = strings.TrimSuffix(name, filepath.Ext(name))
	}
	
	node := &model.NoteNode{
		ID:       relPath,
		Name:     title,
		Type:     nodeType,
		Path:     relPath,
		ParentID: parentID,
	}

	// Проверяем существование файла/папки
	if _, err := os.Stat(fullPath); err == nil {
		// Объект уже существует. Возвращаем его данные и специальную ошибку.
		// Если это папка, а хотели создать файл (или наоборот), мы просто вернем как есть, UI разберется.
		info, _ := os.Stat(fullPath)
		if info.IsDir() {
			node.Type = "dir"
		} else {
			node.Type = "file"
		}
		return node, ErrAlreadyExists
	}

	// Создаем физически
	if nodeType == "dir" {
		if err := os.MkdirAll(fullPath, 0755); err != nil {
			return nil, err
		}
	} else {
		// Убедимся что папка существует
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(fullPath, []byte("# "+strings.TrimSuffix(name, ".md")+"\n"), 0644); err != nil {
			return nil, err
		}
	}

	// Сохраняем в БД
	var pID *string
	if parentID != "" {
		pID = &parentID
	}

	if err := s.repo.UpsertNode(relPath, title, relPath, pID, nodeType); err != nil {
		return nil, err
	}

	return node, nil
}

// DeleteNode удаляет файл или папку
func (s *NoteService) DeleteNode(id string) error {
	if s.basePath == "" || id == "" || id == "." {
		return os.ErrInvalid
	}

	fullPath := filepath.Join(s.basePath, id)
	if err := os.RemoveAll(fullPath); err != nil {
		return err
	}

	return s.repo.DeleteNode(id)
}

// RenameNode переименовывает файл или папку
func (s *NoteService) RenameNode(id, newName string) error {
	if s.basePath == "" || id == "" || id == "." || newName == "" {
		return os.ErrInvalid
	}

	oldPath := filepath.Join(s.basePath, id)
	info, err := os.Stat(oldPath)
	if err != nil {
		return err
	}

	isDir := info.IsDir()
	
	// Защита от путей
	newName = filepath.Base(newName)
	
	if !isDir && !strings.HasSuffix(strings.ToLower(newName), ".md") {
		newName += ".md"
	}

	newPath := filepath.Join(filepath.Dir(oldPath), newName)

	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}

	// Синхронизируем ФС с БД, чтобы обновились все пути (особенно важно для папок, т.к. пути детей меняются)
	return s.SyncFS()
}

// SaveAsset сохраняет загруженный файл в директорию assets/images и возвращает его относительный путь
func (s *NoteService) SaveAsset(file io.Reader, originalFilename string) (string, error) {
	if s.basePath == "" {
		return "", os.ErrNotExist
	}

	assetsDir := filepath.Join(s.basePath, "assets", "images")
	if err := os.MkdirAll(assetsDir, 0755); err != nil {
		return "", err
	}

	ext := filepath.Ext(originalFilename)
	base := strings.TrimSuffix(filepath.Base(originalFilename), ext)
	
	// Если имя пустое (например, вставлено из буфера без имени), генерируем
	if base == "" || base == "image" {
		base = fmt.Sprintf("Pasted image %s", time.Now().Format("20060102150405"))
	}

	filename := base + ext
	fullPath := filepath.Join(assetsDir, filename)

	// Если файл с таким именем уже существует, добавляем таймстемп
	if _, err := os.Stat(fullPath); err == nil {
		filename = fmt.Sprintf("%s_%d%s", base, time.Now().Unix(), ext)
		fullPath = filepath.Join(assetsDir, filename)
	}

	outFile, err := os.Create(fullPath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, file); err != nil {
		return "", err
	}

	// Возвращаем относительный путь
	return filepath.Join("assets", "images", filename), nil
}
