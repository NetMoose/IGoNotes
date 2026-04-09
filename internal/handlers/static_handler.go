package handlers

import (
	"net/http"
)

// StaticHandler обрабатывает HTTP-запросы к статическим файлам
type StaticHandler struct {
	FileServer http.Handler
}

// NewStaticHandler создает новый экземпляр StaticHandler
func NewStaticHandler(staticDir string) *StaticHandler {
	return &StaticHandler{
		FileServer: http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))),
	}
}

// ServeHTTP обрабатывает запросы к статическим файлам
func (h *StaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.FileServer.ServeHTTP(w, r)
}

// RootHandler обрабатывает запросы к корню сайта
func RootHandler(templateDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, templateDir+"/index.html")
	}
}