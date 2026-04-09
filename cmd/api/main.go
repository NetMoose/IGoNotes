package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"IGoNotes/internal/handlers"
	"IGoNotes/internal/service"
)

func main() {
	// Определение CLI-флагов
	configPath := flag.String("config", filepath.Join(os.Getenv("HOME"), ".config", "igonotes"), "Путь к конфигурации")
	port := flag.String("port", "8080", "Порт сервера")
	base := flag.String("base", "", "Имя базы для открытия")
	noBrowser := flag.Bool("no-browser", false, "Не открывать браузер автоматически")
	flag.Parse()

	// Создаем полный путь к файлу конфигурации
	configFile := filepath.Join(*configPath, "config.json")

	// Инициализация сервисов
	configService := service.NewConfigService(configFile)
	noteService := service.NewNoteService()

	// Создаем обработчики
	noteHandler := handlers.NewNoteHandler(noteService)
	configHandler := handlers.NewConfigHandler(configService)
	staticHandler := handlers.NewStaticHandler("web/static/")

	// Проверяем, существует ли конфигурация
	if !configService.Exists() {
		log.Println("Конфигурация не найдена. Запуск мастера настройки...")
		// TODO: Реализовать перенаправление на мастер настройки
	}

	// Маршрутизация
	http.HandleFunc("/api/notes", noteHandler.GetNotes)
	http.HandleFunc("/api/note", noteHandler.GetNote)
	http.HandleFunc("/api/save", noteHandler.SaveNote)

	// Обработчик для статических файлов
	http.Handle("/static/", staticHandler)

	// Главная страница
	http.HandleFunc("/", handlers.RootHandler("web/templates"))

	// API для работы с конфигурацией
	http.HandleFunc("/api/config", configHandler.SaveConfig)

	// GET /api/config для получения конфигурации
	http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			configHandler.GetConfig(w, r)
		} else {
			configHandler.SaveConfig(w, r)
		}
	})

	address := ":" + *port
	log.Printf("Сервер запущен на %s", address)
	if err := http.ListenAndServe(address, nil); err != nil {
		log.Fatal(err)
	}
}