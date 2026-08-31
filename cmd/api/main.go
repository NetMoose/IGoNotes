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
			returnErr = errors.Join(returnErr, fmt.Errorf("закрыть БД метаданных: %w", err))
		}
	}()

	noteRepo := repository.NewNoteRepository(db)
	noteService := service.NewNoteService(noteRepo, basePath)
	defer func() {
		if err := noteService.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("закрыть базу заметок: %w", err))
		}
	}()

	settingsService, err := service.NewSettingsService(configService, noteService, options.base, log.Default())
	if err != nil {
		return fmt.Errorf("инициализировать сервис настроек: %w", err)
	}

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
