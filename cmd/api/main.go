package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

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

	// Проверяем, существует ли конфигурация
	if !configService.Exists() {
		log.Println("Конфигурация не найдена. Запуск мастера настройки...")
		// TODO: Реализовать перенаправление на мастер настройки
	}

	// Маршрутизация
	http.HandleFunc("/api/notes", noteService.GetNotes)
	http.HandleFunc("/api/note", noteService.GetNote)
	http.HandleFunc("/api/save", noteService.SaveNote)

	// Статические файлы
	fs := http.FileServer(http.Dir("web/static/"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// Главная страница
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/templates/index.html")
	})

	// API для работы с конфигурацией
	http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			// TODO: Реализовать сохранение конфигурации
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{\"status\": \"saved\"}"))
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	address := ":" + *port
	log.Printf("Сервер запущен на %s", address)
	if err := http.ListenAndServe(address, nil); err != nil {
		log.Fatal(err)
	}
}