package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/handlers"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
	"IGoNotes/web"
)

const gracefulShutdownTimeout = 10 * time.Second

type serverOptions struct {
	configPath string
	port       string
	base       string
	noBrowser  bool
}

func parseServerOptions(args []string, output io.Writer) (serverOptions, error) {
	flags := flag.NewFlagSet("igonotes", flag.ContinueOnError)
	flags.SetOutput(output)

	var options serverOptions
	flags.StringVar(&options.configPath, "config", "", "Каталог конфигурации (по умолчанию системный каталог пользователя)")
	flags.StringVar(&options.port, "port", "8080", "Порт сервера")
	flags.StringVar(&options.base, "base", "", "Имя базы для открытия")
	flags.BoolVar(&options.noBrowser, "no-browser", false, "Не открывать браузер автоматически")
	if err := flags.Parse(args); err != nil {
		err = fmt.Errorf("parse server options: %w", err)
		if errors.Is(err, flag.ErrHelp) {
			return serverOptions{}, err
		}
		return serverOptions{}, &commandLineError{err: err, reported: true}
	}
	return options, nil
}

func runServer(ctx context.Context, args []string) (returnErr error) {
	options, err := parseServerOptions(args, os.Stderr)
	if err != nil {
		return err
	}

	resolvedConfigDir, err := resolveConfigDir(options.configPath, os.UserConfigDir)
	if err != nil {
		return fmt.Errorf("определить каталог конфигурации: %w", err)
	}
	configFile := filepath.Join(resolvedConfigDir, "config.json")
	configService := service.NewConfigService(configFile)

	appDataDir, err := resolveDataDir(os.UserHomeDir)
	if err != nil {
		return fmt.Errorf("определить каталог данных: %w", err)
	}
	basePath, err := service.ResolveStartupBase(configService, options.base, appDataDir)
	if err != nil {
		return fmt.Errorf("выбрать базу заметок: %w", err)
	}

	dbPath := filepath.Join(appDataDir, "metadata.db")
	db, err := repository.InitDB(dbPath)
	if err != nil {
		return fmt.Errorf("инициализировать БД: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			returnErr = errors.Join(returnErr, gitLifecycleError("закрыть БД метаданных", err))
		}
	}()

	noteRepo := repository.NewNoteRepository(db)
	coordinator := service.NewBaseOperationCoordinator()
	noteService := service.NewNoteService(noteRepo, basePath, coordinator)
	defer func() {
		if err := noteService.Close(); err != nil {
			returnErr = errors.Join(returnErr, gitLifecycleError("закрыть базу заметок", err))
		}
	}()

	gitRunner := gitcmd.NewCommandRunner()
	gitClient := gitcmd.NewClient(gitRunner)
	gitStatusRepo := repository.NewGitStatusRepository(db)
	gitValidator := service.NewGitConfigValidator(gitClient)
	settingsService, err := service.NewSettingsServiceWithGit(configService, noteService, coordinator, options.base, log.Default(), gitValidator, gitStatusRepo)
	if err != nil {
		return fmt.Errorf("инициализировать сервис настроек: %w", err)
	}
	gitProbeService := service.NewGitProbeService(settingsService, gitClient)
	gitStatusService := service.NewGitStatusService(settingsService, gitStatusRepo)
	gitOperations := repository.NewGitOperationRepository(db)
	gitService := gitcmd.NewService(gitRunner, gitClient)
	gitManager := service.NewGitManagerWithAutosync(
		gitService, gitStatusRepo, gitOperations, gitProbeService,
		settingsService.GitSnapshot, noteService, coordinator,
		gitSnapshotSource(settingsService.GitSnapshots), settingsService.GitConfigChanges(), log.Default(),
	)
	defer func() {
		if err := gitManager.Close(); err != nil {
			returnErr = errors.Join(returnErr, gitLifecycleError("закрыть менеджер Git", err))
		}
	}()
	configuredSnapshots, err := configuredGitSnapshots(settingsService)
	if err != nil {
		return gitLifecycleError("получить настройки локальных репозиториев Git", err)
	}
	if err := gitManager.RecoverLocal(ctx, configuredSnapshots); err != nil {
		return gitLifecycleError("восстановить локальные репозитории Git", err)
	}
	if err := gitManager.Start(); err != nil {
		return gitLifecycleError("запустить менеджер Git", err)
	}
	stopGitClose := watchGitShutdown(ctx, gitManager)
	defer stopGitClose()
	gitHandler := handlers.NewGitHandlerWithOperations(gitProbeService, settingsService, gitStatusService, gitManager)

	go func() {
		log.Println("Запуск первичной синхронизации файловой системы...")
		if err := noteService.SyncFS(); err != nil {
			log.Printf("Ошибка первичной синхронизации: %v", err)
		} else {
			log.Println("Первичная синхронизация завершена успешно.")
		}
	}()

	noteHandler := handlers.NewNoteHandler(noteService)
	settingsHandler := handlers.NewSettingsHandler(settingsService)
	directoryPicker := service.NewDirectoryPicker(service.ExecCommandRunner{}, runtime.GOOS)
	systemHandler := handlers.NewSystemHandler(directoryPicker)

	distFS, err := web.GetDistFS()
	if err != nil {
		return fmt.Errorf("инициализировать статические файлы фронтенда: %w", err)
	}
	spaHandler := handlers.NewSPAHandler(distFS)

	router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)
	handlers.RegisterGitRoutes(router, gitHandler, settingsService)
	gitConflictHandler := handlers.NewGitConflictHandler(gitManager)
	handlers.RegisterGitConflictRoutes(router, gitConflictHandler, settingsService)
	registerSystemRoutes(router, systemHandler)

	address, url := localServerEndpoint(options.port)
	return serveLocal(ctx, address, newHTTPServer(router), func() {
		log.Printf("Сервер запущен на %s", url)

		if !options.noBrowser {
			log.Printf("Открываем браузер: %s", url)
			if err := openBrowser(url); err != nil {
				log.Printf("Не удалось открыть браузер автоматически: %v", err)
			}
		}
	}, gracefulShutdownTimeout)
}

type gitSnapshotSource func() ([]gitcmd.ConfiguredBase, error)

func (source gitSnapshotSource) OrderedGitSnapshots() ([]gitcmd.ConfiguredBase, error) {
	return source()
}

// Close cancels the manager immediately, even while HTTP shutdown is draining.
// The owner's deferred Close still waits for both worker and scheduler before
// closing their note service and database dependencies.
func watchGitShutdown(ctx context.Context, manager interface{ Close() error }) func() bool {
	return context.AfterFunc(ctx, func() { _ = manager.Close() })
}

type safeGitLifecycleError struct {
	message string
	cause   error
}

func (err *safeGitLifecycleError) Error() string { return err.message }
func (err *safeGitLifecycleError) Unwrap() error { return err.cause }

func gitLifecycleError(message string, cause error) error {
	return &safeGitLifecycleError{message: message, cause: cause}
}

func configuredGitSnapshots(settings *service.SettingsService) ([]gitcmd.ConfiguredBase, error) {
	return settings.GitSnapshots()
}

func runMain() error {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()

	manager := service.NewSystemdUserManager(
		runtime.GOOS,
		service.ExecCommandRunner{},
		exec.LookPath,
		os.UserConfigDir,
		os.Executable,
	)
	return dispatchCommand(ctx, os.Args[1:], os.Stdout, manager, runServer)
}

func main() {
	err := runMain()
	exitCode := commandExitCode(err)
	if exitCode == 0 {
		return
	}
	if shouldLogCommandError(err) {
		log.Print(err)
	}
	os.Exit(exitCode)
}
