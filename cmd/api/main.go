package main

import (
	"flag"
	"log"
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

	appDataDir := filepath.Join(os.Getenv("HOME"), ".igonotes")
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
	configHandler := handlers.NewConfigHandler(configService)

	// Инициализация статики (фронтенд)
	distFS, err := web.GetDistFS()
	if err != nil {
		log.Fatal("Ошибка инициализации статических файлов фронтенда:", err)
	}
	spaHandler := handlers.NewSPAHandler(distFS)

	// Маршрутизация
	http.HandleFunc("/api/info", noteHandler.GetInfo)

	http.HandleFunc("/api/notes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			noteHandler.CreateNote(w, r)
		} else {
			noteHandler.GetNotes(w, r)
		}
	})

	http.HandleFunc("/api/note", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			noteHandler.DeleteNote(w, r)
		} else {
			noteHandler.GetNote(w, r)
		}
	})

	http.HandleFunc("/api/sync", noteHandler.SyncNotes)

	http.HandleFunc("/api/raw", noteHandler.GetRawFile)

	http.HandleFunc("/api/save", noteHandler.SaveNote)
	http.HandleFunc("/api/rename", noteHandler.RenameNote)
	http.HandleFunc("/api/assets", noteHandler.UploadAsset)

	// API для работы с конфигурацией
	http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			configHandler.GetConfig(w, r)
		} else {
			configHandler.SaveConfig(w, r)
		}
	})

	// Фронтенд (обрабатывает все остальные запросы)
	http.Handle("/", spaHandler)

	address := ":" + *port
	url := "http://localhost" + address
	log.Printf("Сервер запущен на %s", url)

	if !*noBrowser {
		log.Printf("Открываем браузер: %s", url)
		if err := openBrowser(url); err != nil {
			log.Printf("Не удалось открыть браузер автоматически: %v", err)
		}
	}

	if err := http.ListenAndServe(address, nil); err != nil {
		log.Fatal(err)
	}
}
