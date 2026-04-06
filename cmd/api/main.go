package main

import (
	"log"
	"net/http"

	"IGoNotes/internal/service"
)

func main() {
	// Инициализация сервисов
	noteService := service.NewNoteService()

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

	log.Println("Сервер запущен на :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}
