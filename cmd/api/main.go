package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"IGoNotes/internal/handlers"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
	"IGoNotes/web"
)

func localServerEndpoint(port string) (string, string) {
	address := net.JoinHostPort("127.0.0.1", port)
	return address, "http://" + address
}

func serveLocal(
	address string,
	handler http.Handler,
	ready func(),
	serve func(net.Listener, http.Handler) error,
) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	ready()
	return serve(listener, handler)
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}

func main() {
	// Определение CLI-флагов
	configPath := flag.String("config", "", "Каталог конфигурации (по умолчанию системный каталог пользователя)")
	port := flag.String("port", "8080", "Порт сервера")
	base := flag.String("base", "", "Имя базы для открытия")
	noBrowser := flag.Bool("no-browser", false, "Не открывать браузер автоматически")
	flag.Parse()

	resolvedConfigDir, err := resolveConfigDir(*configPath, os.UserConfigDir)
	if err != nil {
		log.Fatal("Ошибка определения каталога конфигурации: ", err)
	}
	configFile := filepath.Join(resolvedConfigDir, "config.json")
	configService := service.NewConfigService(configFile)

	appDataDir, err := resolveDataDir(os.UserHomeDir)
	if err != nil {
		log.Fatal("Ошибка определения каталога данных: ", err)
	}
	basePath, err := service.ResolveStartupBase(configService, *base, appDataDir)
	if err != nil {
		log.Fatal("Ошибка выбора базы заметок: ", err)
	}

	// Инициализация базы данных
	dbPath := filepath.Join(appDataDir, "metadata.db")
	db, err := repository.InitDB(dbPath)
	if err != nil {
		log.Fatal("Ошибка инициализации БД:", err)
	}
	defer db.Close()

	noteRepo := repository.NewNoteRepository(db)

	// Инициализация сервисов
	noteService := service.NewNoteService(noteRepo, basePath)
	defer func() {
		if err := noteService.Close(); err != nil {
			log.Printf("Ошибка закрытия базы заметок: %v", err)
		}
	}()
	settingsService, err := service.NewSettingsService(configService, noteService, *base, log.Default())
	if err != nil {
		log.Fatal("Ошибка инициализации сервиса настроек: ", err)
	}

	// Запускаем первичную синхронизацию базы с диском при старте программы
	go func() {
		log.Println("Запуск первичной синхронизации файловой системы...")
		if err := noteService.SyncFS(); err != nil {
			log.Printf("Ошибка первичной синхронизации: %v", err)
		} else {
			log.Println("Первичная синхронизация завершена успешно.")
		}
	}()

	// Создаем обработчики
	noteHandler := handlers.NewNoteHandler(noteService)
	settingsHandler := handlers.NewSettingsHandler(settingsService)

	// Инициализация статики (фронтенд)
	distFS, err := web.GetDistFS()
	if err != nil {
		log.Fatal("Ошибка инициализации статических файлов фронтенда:", err)
	}
	spaHandler := handlers.NewSPAHandler(distFS)

	// Маршрутизация
	router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)

	address, url := localServerEndpoint(*port)
	if err := serveLocal(address, router, func() {
		log.Printf("Сервер запущен на %s", url)

		if !*noBrowser {
			log.Printf("Открываем браузер: %s", url)
			if err := openBrowser(url); err != nil {
				log.Printf("Не удалось открыть браузер автоматически: %v", err)
			}
		}
	}, http.Serve); err != nil {
		log.Fatal(err)
	}
}
