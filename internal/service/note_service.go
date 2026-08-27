package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"IGoNotes/internal/model"
)

var (
	// ErrAlreadyExists возвращается, если файл или папка с таким именем уже существует
	ErrAlreadyExists   = errors.New("file or directory already exists")
	ErrInvalidNotePath = errors.New("invalid note path")
)

type noteRepository interface {
	UpsertNode(id, title, path string, parentID *string, nodeType string) error
	GetAllNodes() ([]model.NoteNode, error)
	ReplaceAll([]model.NoteNode) error
	DeleteNode(id string) error
}

type noteScanner func(string) ([]model.NoteNode, error)

type LockedFile struct {
	*os.File
	once   sync.Once
	unlock func()
}

func (f *LockedFile) Close() error {
	var err error
	f.once.Do(func() {
		err = f.File.Close()
		f.unlock()
	})
	return err
}

type NoteService struct {
	repo     noteRepository
	basePath string
	// baseMu is acquired before repository/SQL; repositories and scanners never call NoteService.
	baseMu          sync.RWMutex
	initialSyncDone chan struct{}
	once            sync.Once
	scan            noteScanner
	beforeReadLock  func()
}

// NewNoteService создает новый экземпляр NoteService
func NewNoteService(repo noteRepository, basePath string) *NoteService {
	return &NoteService{
		repo:            repo,
		basePath:        basePath,
		initialSyncDone: make(chan struct{}),
		scan:            scanNotes,
	}
}

func (s *NoteService) GetBasePath() string {
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()
	return s.basePath
}

// SyncFS сканирует файловую систему и обновляет SQLite
func (s *NoteService) SyncFS() error {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	defer s.once.Do(func() { close(s.initialSyncDone) })

	return s.replaceIndexLocked()
}

func (s *NoteService) SwitchBase(target string) error {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	if target == "" {
		if err := s.repo.ReplaceAll(nil); err != nil {
			return err
		}
		s.basePath = ""
		s.once.Do(func() { close(s.initialSyncDone) })
		return nil
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("base path is not a directory: %s", target)
	}

	cleanTarget := filepath.Clean(target)
	nodes, err := s.scan(cleanTarget)
	if err != nil {
		return err
	}
	if err := s.repo.ReplaceAll(nodes); err != nil {
		return err
	}

	s.basePath = cleanTarget
	s.once.Do(func() { close(s.initialSyncDone) })
	return nil
}

func (s *NoteService) replaceIndexLocked() error {
	nodes, err := s.scan(s.basePath)
	if err != nil {
		return err
	}
	return s.repo.ReplaceAll(nodes)
}

func scanNotes(basePath string) ([]model.NoteNode, error) {
	if basePath == "" {
		return nil, nil
	}

	walkRoot, err := filepath.EvalSymlinks(basePath)
	if err != nil {
		return nil, err
	}
	walkRoot = filepath.Clean(walkRoot)
	info, err := os.Stat(walkRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("base path is not a directory: %s", basePath)
	}

	var nodes []model.NoteNode
	err = filepath.Walk(walkRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Пропускаем скрытые папки (.git) и assets
		if path != walkRoot && info.IsDir() && (strings.HasPrefix(info.Name(), ".") || info.Name() == "assets") {
			return filepath.SkipDir
		}

		// Игнорируем сам корень
		if path == walkRoot {
			return nil
		}

		// Вычисляем относительный путь (он же ID)
		relPath, err := filepath.Rel(walkRoot, path)
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

		parentID := ""
		parentDir := filepath.Dir(relPath)
		if parentDir != "." && parentDir != "" {
			parentID = parentDir
		}

		nodes = append(nodes, model.NoteNode{
			ID:       id,
			Name:     title,
			Path:     relPath,
			ParentID: parentID,
			Type:     nodeType,
		})
		return nil
	})
	return nodes, err
}

// GetTree возвращает иерархическое дерево заметок
func (s *NoteService) GetTree() ([]model.NoteNode, error) {
	// Дожидаемся завершения первичной синхронизации
	<-s.initialSyncDone
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()

	// Получаем плоский список из БД (без полного сканирования ФС)
	flatNodes, err := s.repo.GetAllNodes()
	if err != nil {
		return nil, err
	}

	// Строим дерево
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
	cleanID, err := cleanRelativeNotePath(id, false)
	if err != nil {
		return "", err
	}
	if s.beforeReadLock != nil {
		s.beforeReadLock()
	}
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()

	if s.basePath == "" {
		return "", os.ErrNotExist
	}

	// id - это относительный путь к файлу
	fullPath := filepath.Join(s.basePath, cleanID)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// GetAbsoluteFilePath возвращает полный абсолютный путь к файлу в базе
func (s *NoteService) GetAbsoluteFilePath(relPath string) (string, error) {
	cleanPath, err := cleanRelativeNotePath(relPath, false)
	if err != nil {
		return "", err
	}
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()

	if s.basePath == "" {
		return "", os.ErrNotExist
	}
	return filepath.Join(s.basePath, cleanPath), nil
}

func (s *NoteService) OpenRawFile(relPath string) (*LockedFile, os.FileInfo, error) {
	cleanPath, err := cleanRelativeNotePath(relPath, false)
	if err != nil {
		return nil, nil, err
	}
	s.baseMu.RLock()
	if s.basePath == "" {
		s.baseMu.RUnlock()
		return nil, nil, os.ErrNotExist
	}

	if err := rejectSymlinkComponents(s.basePath, cleanPath); err != nil {
		s.baseMu.RUnlock()
		return nil, nil, err
	}

	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		s.baseMu.RUnlock()
		return nil, nil, err
	}
	file, err := root.Open(cleanPath)
	if err != nil {
		if symlinkErr := rejectSymlinkComponents(s.basePath, cleanPath); errors.Is(symlinkErr, ErrInvalidNotePath) {
			err = errors.Join(symlinkErr, err)
		}
		closeErr := root.Close()
		s.baseMu.RUnlock()
		return nil, nil, errors.Join(err, closeErr)
	}
	if err := root.Close(); err != nil {
		_ = file.Close()
		s.baseMu.RUnlock()
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		s.baseMu.RUnlock()
		return nil, nil, err
	}

	return &LockedFile{File: file, unlock: s.baseMu.RUnlock}, info, nil
}

func cleanRelativeNotePath(path string, allowEmpty bool) (string, error) {
	if path == "" {
		if allowEmpty {
			return "", nil
		}
		return "", ErrInvalidNotePath
	}
	if filepath.IsAbs(path) {
		return "", ErrInvalidNotePath
	}

	cleanPath := filepath.Clean(path)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", ErrInvalidNotePath
	}
	return cleanPath, nil
}

func rejectSymlinkComponents(basePath, relPath string) error {
	current := basePath
	for _, component := range strings.Split(relPath, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink path component %q", ErrInvalidNotePath, current)
		}
	}
	return nil
}

// SaveNoteContent сохраняет содержимое заметки на диск
func (s *NoteService) SaveNoteContent(id string, content string) error {
	cleanID, err := cleanRelativeNotePath(id, false)
	if err != nil {
		return err
	}
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()

	if s.basePath == "" {
		return os.ErrNotExist
	}

	fullPath := filepath.Join(s.basePath, cleanID)

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
	cleanParentID, err := cleanRelativeNotePath(parentID, true)
	if err != nil {
		return nil, err
	}
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()

	if s.basePath == "" {
		return nil, os.ErrNotExist
	}

	// Защита от выхода за пределы базы
	name = filepath.Base(name)

	if nodeType == "file" && !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}

	relPath := name
	if cleanParentID != "" {
		relPath = filepath.Join(cleanParentID, name)
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
		ParentID: cleanParentID,
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
	if cleanParentID != "" {
		pID = &cleanParentID
	}

	if err := s.repo.UpsertNode(relPath, title, relPath, pID, nodeType); err != nil {
		return nil, err
	}

	return node, nil
}

// DeleteNode удаляет файл или папку
func (s *NoteService) DeleteNode(id string) error {
	cleanID, err := cleanRelativeNotePath(id, false)
	if err != nil {
		return err
	}
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()

	if s.basePath == "" {
		return os.ErrInvalid
	}

	fullPath := filepath.Join(s.basePath, cleanID)
	if err := os.RemoveAll(fullPath); err != nil {
		return err
	}

	return s.repo.DeleteNode(cleanID)
}

// RenameNode переименовывает файл или папку
func (s *NoteService) RenameNode(id, newName string) error {
	cleanID, err := cleanRelativeNotePath(id, false)
	if err != nil {
		return err
	}
	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	if s.basePath == "" || newName == "" {
		return os.ErrInvalid
	}

	oldPath := filepath.Join(s.basePath, cleanID)
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
	if filepath.Clean(oldPath) == filepath.Clean(newPath) {
		return s.replaceIndexLocked()
	}
	if destinationInfo, err := os.Stat(newPath); err == nil {
		if !os.SameFile(info, destinationInfo) {
			return ErrAlreadyExists
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}

	// Синхронизируем ФС с БД, чтобы обновились все пути (особенно важно для папок, т.к. пути детей меняются)
	return s.replaceIndexLocked()
}

// SaveAsset сохраняет загруженный файл в директорию assets/images и возвращает его относительный путь
func (s *NoteService) SaveAsset(file io.Reader, originalFilename string) (string, error) {
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()

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
