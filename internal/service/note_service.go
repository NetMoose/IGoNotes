package service

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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
	BeginReplaceAll([]model.NoteNode) (commit func() error, rollback func() error, operationErr error, rollbackErr error)
	DeleteNode(id string) error
}

type noteScanner func(*os.Root) ([]model.NoteNode, error)

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
	baseRoot *os.Root
	baseErr  error
	closeErr error
	// baseMu is acquired before repository/SQL; repositories and scanners never call NoteService.
	baseMu           sync.RWMutex
	initialSyncDone  chan struct{}
	initialSyncErr   error
	once             sync.Once
	scan             noteScanner
	openRoot         func(string) (*os.Root, error)
	beforeReadLock   func()
	beforeCreateLock func()
	beforeRename     func()
}

// NewNoteService создает новый экземпляр NoteService
func NewNoteService(repo noteRepository, basePath string) *NoteService {
	service := &NoteService{
		repo:            repo,
		basePath:        basePath,
		initialSyncDone: make(chan struct{}),
		scan:            scanNotes,
		openRoot:        os.OpenRoot,
	}
	if basePath != "" {
		service.baseRoot, service.baseErr = os.OpenRoot(basePath)
	}
	return service
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

	err := s.replaceIndexLocked()
	if err != nil {
		s.once.Do(func() {
			s.initialSyncErr = err
			close(s.initialSyncDone)
		})
	}
	return err
}

func (s *NoteService) Close() error {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	s.once.Do(func() { close(s.initialSyncDone) })

	if s.baseRoot == nil {
		err := s.closeErr
		s.closeErr = nil
		s.baseErr = os.ErrClosed
		return err
	}
	err := errors.Join(s.closeErr, s.baseRoot.Close())
	s.baseRoot = nil
	s.baseErr = os.ErrClosed
	s.closeErr = nil
	return err
}

func (s *NoteService) SwitchBase(target string) error {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	if errors.Is(s.baseErr, os.ErrClosed) || errors.Is(s.baseErr, ErrRollbackFailed) {
		return s.baseErr
	}

	candidate, operationErr, rollbackErr := s.prepareBaseSwitchLocked(target)
	if operationErr != nil {
		if rollbackErr != nil {
			return s.baseErr
		}
		return operationErr
	}
	if err := candidate.commit(); err != nil {
		rollbackErr := candidate.rollback()
		s.closeErr = errors.Join(s.closeErr, closeRoot(candidate.root))
		return s.failClosedLocked(
			fmt.Errorf("commit note index: %w", err),
			commitOutcomeError(rollbackErr),
		)
	}
	s.publishBaseSwitchLocked(candidate)
	return nil
}

func (s *NoteService) switchBaseTransaction(target string, store ConfigStore, next *model.Config) (error, error) {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	if errors.Is(s.baseErr, os.ErrClosed) || errors.Is(s.baseErr, ErrRollbackFailed) {
		return s.baseErr, nil
	}
	if store == nil || next == nil {
		return os.ErrInvalid, nil
	}

	candidate, operationErr, rollbackErr := s.prepareBaseSwitchLocked(target)
	if operationErr != nil {
		if rollbackErr != nil {
			return operationErr, rollbackErr
		}
		return fmt.Errorf("switch runtime base: %w", operationErr), nil
	}
	config := cloneConfig(*next)
	if err := store.Save(&config); err != nil {
		operationErr := fmt.Errorf("save settings: %w", err)
		if rollbackErr := candidate.rollback(); rollbackErr != nil {
			s.closeErr = errors.Join(s.closeErr, closeRoot(candidate.root))
			s.failClosedLocked(operationErr, fmt.Errorf("rollback note index: %w", rollbackErr))
			return operationErr, rollbackErr
		}
		s.closeErr = errors.Join(s.closeErr, closeRoot(candidate.root))
		return operationErr, nil
	}
	if err := candidate.commit(); err != nil {
		rollbackErr := commitOutcomeError(candidate.rollback())
		s.closeErr = errors.Join(s.closeErr, closeRoot(candidate.root))
		s.failClosedLocked(fmt.Errorf("commit note index: %w", err), rollbackErr)
		return fmt.Errorf("commit note index: %w", err), rollbackErr
	}
	s.publishBaseSwitchLocked(candidate)
	return nil, nil
}

type baseSwitchCandidate struct {
	path     string
	root     *os.Root
	commit   func() error
	rollback func() error
}

func (s *NoteService) prepareBaseSwitchLocked(target string) (*baseSwitchCandidate, error, error) {
	cleanTarget := ""
	var candidate *os.Root
	if target != "" {
		cleanTarget = filepath.Clean(target)
		var err error
		candidate, err = s.openRoot(cleanTarget)
		if err != nil {
			return nil, err, nil
		}
	}
	nodes, err := s.scan(candidate)
	if err != nil {
		return nil, errors.Join(err, closeRoot(candidate)), nil
	}
	commit, rollback, operationErr, rollbackErr := s.repo.BeginReplaceAll(nodes)
	if operationErr != nil {
		operationErr = errors.Join(operationErr, closeRoot(candidate))
		if rollbackErr != nil {
			s.failClosedLocked(operationErr, fmt.Errorf("rollback note index preparation: %w", rollbackErr))
		}
		return nil, operationErr, rollbackErr
	}
	return &baseSwitchCandidate{path: cleanTarget, root: candidate, commit: commit, rollback: rollback}, nil, nil
}

func (s *NoteService) publishBaseSwitchLocked(candidate *baseSwitchCandidate) {
	oldRoot := s.baseRoot
	s.basePath = candidate.path
	s.baseRoot = candidate.root
	s.baseErr = nil
	// Publication has succeeded, so an old descriptor close error is deferred to Close.
	if oldRoot != nil {
		s.closeErr = errors.Join(s.closeErr, oldRoot.Close())
	}
	s.publishCompleteIndexLocked()
}

func (s *NoteService) publishCompleteIndexLocked() {
	s.initialSyncErr = nil
	s.once.Do(func() { close(s.initialSyncDone) })
}

func (s *NoteService) failClosedLocked(operationErr, rollbackErr error) error {
	s.baseErr = errors.Join(ErrRollbackFailed, operationErr, rollbackErr)
	s.once.Do(func() { close(s.initialSyncDone) })
	return s.baseErr
}

func commitOutcomeError(rollbackErr error) error {
	unknown := errors.New("note index commit outcome is unknown")
	if rollbackErr == nil {
		return unknown
	}
	return errors.Join(unknown, fmt.Errorf("rollback after commit failure: %w", rollbackErr))
}

func closeRoot(root *os.Root) error {
	if root == nil {
		return nil
	}
	return root.Close()
}

func (s *NoteService) replaceIndexLocked() error {
	if s.baseErr != nil {
		return s.baseErr
	}
	nodes, err := s.scan(s.baseRoot)
	if err != nil {
		return err
	}
	commit, rollback, operationErr, rollbackErr := s.repo.BeginReplaceAll(nodes)
	if operationErr != nil {
		if rollbackErr != nil {
			return s.failClosedLocked(operationErr, fmt.Errorf("rollback note index preparation: %w", rollbackErr))
		}
		return operationErr
	}
	if err := commit(); err != nil {
		return s.failClosedLocked(
			fmt.Errorf("commit note index: %w", err),
			commitOutcomeError(rollback()),
		)
	}
	s.publishCompleteIndexLocked()
	return nil
}

func scanNotes(root *os.Root) ([]model.NoteNode, error) {
	if root == nil {
		return nil, nil
	}

	var nodes []model.NoteNode
	err := fs.WalkDir(root.FS(), ".", func(walkPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Пропускаем скрытые папки (.git) и assets
		if walkPath != "." && entry.IsDir() && (strings.HasPrefix(entry.Name(), ".") || strings.EqualFold(entry.Name(), "assets")) {
			return fs.SkipDir
		}

		// Игнорируем сам корень
		if walkPath == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Вычисляем относительный путь (он же ID)
		relPath := walkPath

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
		parentDir := path.Dir(relPath)
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
	if s.baseErr != nil {
		return nil, s.baseErr
	}
	if s.initialSyncErr != nil {
		return nil, s.initialSyncErr
	}

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

	if s.baseErr != nil {
		return "", s.baseErr
	}
	if s.basePath == "" {
		return "", os.ErrNotExist
	}

	data, err := s.baseRoot.ReadFile(rootPath(cleanID))
	if err != nil {
		return "", normalizeRootError(err)
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

	if s.baseErr != nil {
		return "", s.baseErr
	}
	if s.basePath == "" {
		return "", os.ErrNotExist
	}
	if _, err := s.baseRoot.Stat(rootPath(cleanPath)); err != nil {
		return "", normalizeRootError(err)
	}
	return filepath.Join(s.basePath, filepath.FromSlash(cleanPath)), nil
}

func (s *NoteService) OpenRawFile(relPath string) (*LockedFile, os.FileInfo, error) {
	cleanPath, err := cleanRelativeNotePath(relPath, false)
	if err != nil {
		return nil, nil, err
	}
	s.baseMu.RLock()
	if s.baseErr != nil {
		s.baseMu.RUnlock()
		return nil, nil, s.baseErr
	}
	if s.basePath == "" {
		s.baseMu.RUnlock()
		return nil, nil, os.ErrNotExist
	}
	file, err := s.baseRoot.Open(rootPath(cleanPath))
	if err != nil {
		s.baseMu.RUnlock()
		return nil, nil, normalizeRootError(err)
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
	return filepath.ToSlash(cleanPath), nil
}

func rootPath(id string) string {
	return filepath.FromSlash(id)
}

func normalizeRootError(err error) error {
	if err == nil {
		return nil
	}
	if isRootEscapeError(err) {
		return fmt.Errorf("%w: %v", ErrInvalidNotePath, err)
	}
	return err
}

func ensureRootDir(root *os.Root, name string) error {
	rootName := rootPath(name)
	if info, err := root.Stat(rootName); err == nil {
		if info.IsDir() {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return normalizeRootError(err)
	}
	if err := root.MkdirAll(rootName, 0755); err != nil {
		if _, statErr := root.Stat(rootName); statErr != nil && isRootEscapeError(statErr) {
			return normalizeRootError(statErr)
		}
		return normalizeRootError(err)
	}
	return nil
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
	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	if s.baseErr != nil {
		return s.baseErr
	}
	if s.basePath == "" {
		return os.ErrNotExist
	}

	parent := path.Dir(cleanID)
	if err := ensureRootDir(s.baseRoot, parent); err != nil {
		return err
	}
	if err := s.baseRoot.WriteFile(rootPath(cleanID), []byte(content), 0644); err != nil {
		return normalizeRootError(err)
	}

	return nil
}

// CreateNode создает новый файл или папку
func (s *NoteService) CreateNode(parentID, name, nodeType string) (*model.NoteNode, error) {
	cleanParentID, err := cleanRelativeNotePath(parentID, true)
	if err != nil {
		return nil, err
	}
	if s.beforeCreateLock != nil {
		s.beforeCreateLock()
	}
	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	// Защита от выхода за пределы базы
	name = filepath.Base(name)

	if nodeType == "file" && !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}

	relPath := filepath.ToSlash(name)
	if cleanParentID != "" {
		relPath = path.Join(cleanParentID, filepath.ToSlash(name))
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

	if s.baseErr != nil {
		return nil, s.baseErr
	}
	if s.basePath == "" {
		return nil, os.ErrNotExist
	}

	// Проверяем существование файла/папки
	if info, err := s.baseRoot.Stat(rootPath(relPath)); err == nil {
		// Объект уже существует. Возвращаем его данные и специальную ошибку.
		// Если это папка, а хотели создать файл (или наоборот), мы просто вернем как есть, UI разберется.
		if info.IsDir() {
			node.Type = "dir"
		} else {
			node.Type = "file"
		}
		return node, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, normalizeRootError(err)
	}

	// Создаем физически
	parent := path.Dir(relPath)
	if err := ensureRootDir(s.baseRoot, parent); err != nil {
		return nil, err
	}
	if nodeType == "dir" {
		if err := s.baseRoot.Mkdir(rootPath(relPath), 0755); err != nil {
			if errors.Is(err, os.ErrExist) {
				return node, ErrAlreadyExists
			}
			return nil, normalizeRootError(err)
		}
	} else {
		file, err := s.baseRoot.OpenFile(rootPath(relPath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return node, ErrAlreadyExists
			}
			return nil, normalizeRootError(err)
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
	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	if s.baseErr != nil {
		return s.baseErr
	}
	if s.basePath == "" {
		return os.ErrInvalid
	}
	if err := s.baseRoot.RemoveAll(rootPath(cleanID)); err != nil {
		return normalizeRootError(err)
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

	if s.baseErr != nil {
		return s.baseErr
	}
	if s.basePath == "" || newName == "" {
		return os.ErrInvalid
	}
	sourceEntry, err := s.baseRoot.Lstat(rootPath(cleanID))
	if err != nil {
		return normalizeRootError(err)
	}
	info, err := s.baseRoot.Stat(rootPath(cleanID))
	if err != nil {
		return normalizeRootError(err)
	}

	isDir := info.IsDir()

	// Защита от путей
	newName = filepath.Base(newName)

	if !isDir && !strings.HasSuffix(strings.ToLower(newName), ".md") {
		newName += ".md"
	}

	newPath := path.Join(path.Dir(cleanID), filepath.ToSlash(newName))
	if cleanID == newPath {
		return s.replaceIndexLocked()
	}
	if destinationEntry, err := s.baseRoot.Lstat(rootPath(newPath)); err == nil {
		if destinationEntry.Mode()&os.ModeSymlink != 0 {
			if _, err := s.baseRoot.Stat(rootPath(newPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return normalizeRootError(err)
			}
		}
		if !os.SameFile(sourceEntry, destinationEntry) {
			return ErrAlreadyExists
		}
	} else if !os.IsNotExist(err) {
		return normalizeRootError(err)
	}
	// baseMu excludes in-process mutations; os.Root has no portable atomic no-replace rename.
	if s.beforeRename != nil {
		s.beforeRename()
	}

	if err := s.baseRoot.Rename(rootPath(cleanID), rootPath(newPath)); err != nil {
		return normalizeRootError(err)
	}

	// Синхронизируем ФС с БД, чтобы обновились все пути (особенно важно для папок, т.к. пути детей меняются)
	return s.replaceIndexLocked()
}

// SaveAsset сохраняет загруженный файл в директорию assets/images и возвращает его относительный путь
func (s *NoteService) SaveAsset(file io.Reader, originalFilename string) (string, error) {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	if s.baseErr != nil {
		return "", s.baseErr
	}
	if s.basePath == "" {
		return "", os.ErrNotExist
	}
	assetsDir := path.Join("assets", "images")
	if err := ensureRootDir(s.baseRoot, assetsDir); err != nil {
		return "", err
	}

	ext := filepath.Ext(originalFilename)
	base := strings.TrimSuffix(filepath.Base(originalFilename), ext)

	// Если имя пустое (например, вставлено из буфера без имени), генерируем
	if base == "" || base == "image" {
		base = fmt.Sprintf("Pasted image %s", time.Now().Format("20060102150405"))
	}

	var filename string
	var outFile *os.File
	stamp := time.Now().Unix()
	for attempt := 0; ; attempt++ {
		switch attempt {
		case 0:
			filename = base + ext
		case 1:
			filename = fmt.Sprintf("%s_%d%s", base, stamp, ext)
		default:
			filename = fmt.Sprintf("%s_%d_%d%s", base, stamp, attempt, ext)
		}
		relPath := path.Join(assetsDir, filepath.ToSlash(filename))
		var err error
		outFile, err = s.baseRoot.OpenFile(rootPath(relPath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", normalizeRootError(err)
		}
		break
	}

	_, writeErr := io.Copy(outFile, file)
	closeErr := outFile.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.Join(writeErr, closeErr)
	}

	// Возвращаем относительный путь
	return path.Join("assets", "images", filepath.ToSlash(filename)), nil
}
