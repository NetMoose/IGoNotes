package service

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	root, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, err
	}

	var nodes []model.NoteNode
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Пропускаем скрытые папки (.git) и assets
		if path != "." && entry.IsDir() && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "assets") {
			return fs.SkipDir
		}

		// Игнорируем сам корень
		if path == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Вычисляем относительный путь (он же ID)
		relPath := filepath.FromSlash(path)

		// Мы обрабатываем только папки и .md файлы
		isDir := entry.IsDir()
		if !isDir && !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			return nil
		}

		id := relPath
		title := entry.Name()
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
	return nodes, errors.Join(err, root.Close())
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

	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := rejectRootSymlinkComponents(root, cleanID, false); err != nil {
		return "", err
	}

	data, err := root.ReadFile(cleanID)
	if err != nil {
		return "", normalizeRootError(root, cleanID, false, err)
	}
	return string(data), nil
}

// GetAbsoluteFilePath возвращает информационный полный путь, не являющийся безопасным дескриптором файла.
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
	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := rejectRootSymlinkComponents(root, cleanPath, false); err != nil {
		return "", err
	}
	if _, err := root.Stat(cleanPath); err != nil {
		return "", normalizeRootError(root, cleanPath, false, err)
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

	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		s.baseMu.RUnlock()
		return nil, nil, err
	}
	if err := rejectRootSymlinkComponents(root, cleanPath, false); err != nil {
		closeErr := root.Close()
		s.baseMu.RUnlock()
		return nil, nil, errors.Join(err, closeErr)
	}
	file, err := root.Open(cleanPath)
	if err != nil {
		err = normalizeRootError(root, cleanPath, false, err)
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
	if !filepath.IsLocal(path) {
		return "", ErrInvalidNotePath
	}

	cleanPath := filepath.Clean(path)
	if cleanPath == "." {
		return "", ErrInvalidNotePath
	}
	return cleanPath, nil
}

func rejectRootSymlinkComponents(root *os.Root, relPath string, allowFinal bool) error {
	components := strings.Split(relPath, string(filepath.Separator))
	if allowFinal {
		components = components[:len(components)-1]
	}
	current := ""
	for _, component := range components {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if isRootEscapeError(err) {
				return fmt.Errorf("%w: %v", ErrInvalidNotePath, err)
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink path component %q", ErrInvalidNotePath, current)
		}
	}
	return nil
}

func normalizeRootError(root *os.Root, relPath string, allowFinal bool, err error) error {
	if err == nil {
		return nil
	}
	if isRootEscapeError(err) {
		return fmt.Errorf("%w: %v", ErrInvalidNotePath, err)
	}
	if symlinkErr := rejectRootSymlinkComponents(root, relPath, allowFinal); errors.Is(symlinkErr, ErrInvalidNotePath) {
		return errors.Join(symlinkErr, err)
	}
	return err
}

func isRootEscapeError(err error) bool {
	if err == nil {
		return false
	}
	if err.Error() == "path escapes from parent" {
		return true
	}
	type singleUnwrapper interface{ Unwrap() error }
	if wrapped, ok := err.(singleUnwrapper); ok && isRootEscapeError(wrapped.Unwrap()) {
		return true
	}
	type multiUnwrapper interface{ Unwrap() []error }
	if wrapped, ok := err.(multiUnwrapper); ok {
		for _, nested := range wrapped.Unwrap() {
			if isRootEscapeError(nested) {
				return true
			}
		}
	}
	return false
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

	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := rejectRootSymlinkComponents(root, cleanID, false); err != nil {
		return err
	}

	parent := filepath.Dir(cleanID)
	if err := root.MkdirAll(parent, 0755); err != nil {
		return normalizeRootError(root, parent, false, err)
	}
	if err := root.WriteFile(cleanID, []byte(content), 0644); err != nil {
		return normalizeRootError(root, cleanID, false, err)
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

	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := rejectRootSymlinkComponents(root, relPath, false); err != nil {
		return nil, err
	}

	// Проверяем существование файла/папки
	if info, err := root.Stat(relPath); err == nil {
		// Объект уже существует. Возвращаем его данные и специальную ошибку.
		// Если это папка, а хотели создать файл (или наоборот), мы просто вернем как есть, UI разберется.
		if info.IsDir() {
			node.Type = "dir"
		} else {
			node.Type = "file"
		}
		return node, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, normalizeRootError(root, relPath, false, err)
	}

	// Создаем физически
	parent := filepath.Dir(relPath)
	if err := root.MkdirAll(parent, 0755); err != nil {
		return nil, normalizeRootError(root, parent, false, err)
	}
	if nodeType == "dir" {
		if err := root.Mkdir(relPath, 0755); err != nil {
			if errors.Is(err, os.ErrExist) {
				return node, ErrAlreadyExists
			}
			return nil, normalizeRootError(root, relPath, false, err)
		}
	} else {
		file, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return node, ErrAlreadyExists
			}
			return nil, normalizeRootError(root, relPath, false, err)
		}
		_, writeErr := file.WriteString("# " + strings.TrimSuffix(name, ".md") + "\n")
		if closeErr := file.Close(); writeErr != nil || closeErr != nil {
			return nil, errors.Join(writeErr, closeErr)
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

	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := rejectRootSymlinkComponents(root, cleanID, true); err != nil {
		return err
	}
	if err := root.RemoveAll(cleanID); err != nil {
		return normalizeRootError(root, cleanID, true, err)
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

	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := rejectRootSymlinkComponents(root, cleanID, false); err != nil {
		return err
	}
	info, err := root.Stat(cleanID)
	if err != nil {
		return normalizeRootError(root, cleanID, false, err)
	}

	isDir := info.IsDir()

	// Защита от путей
	newName = filepath.Base(newName)

	if !isDir && !strings.HasSuffix(strings.ToLower(newName), ".md") {
		newName += ".md"
	}

	newPath := filepath.Join(filepath.Dir(cleanID), newName)
	if cleanID == newPath {
		return s.replaceIndexLocked()
	}
	if err := rejectRootSymlinkComponents(root, newPath, false); err != nil {
		return err
	}
	if destinationInfo, err := root.Stat(newPath); err == nil {
		if !os.SameFile(info, destinationInfo) {
			return ErrAlreadyExists
		}
	} else if !os.IsNotExist(err) {
		return normalizeRootError(root, newPath, false, err)
	}

	if err := root.Rename(cleanID, newPath); err != nil {
		return normalizeRootError(root, cleanID, false, err)
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

	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		return "", err
	}
	defer root.Close()
	assetsDir := filepath.Join("assets", "images")
	if err := rejectRootSymlinkComponents(root, assetsDir, false); err != nil {
		return "", err
	}
	if err := root.MkdirAll(assetsDir, 0755); err != nil {
		return "", normalizeRootError(root, assetsDir, false, err)
	}

	ext := filepath.Ext(originalFilename)
	base := strings.TrimSuffix(filepath.Base(originalFilename), ext)

	// Если имя пустое (например, вставлено из буфера без имени), генерируем
	if base == "" || base == "image" {
		base = fmt.Sprintf("Pasted image %s", time.Now().Format("20060102150405"))
	}

	filename := base + ext
	relPath := filepath.Join(assetsDir, filename)
	outFile, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, os.ErrExist) {
		filename = fmt.Sprintf("%s_%d%s", base, time.Now().Unix(), ext)
		relPath = filepath.Join(assetsDir, filename)
		outFile, err = root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	}
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", ErrAlreadyExists
		}
		return "", normalizeRootError(root, relPath, false, err)
	}

	_, writeErr := io.Copy(outFile, file)
	closeErr := outFile.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.Join(writeErr, closeErr)
	}

	// Возвращаем относительный путь
	return filepath.Join("assets", "images", filename), nil
}
