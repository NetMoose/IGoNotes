package handlers

import (
	"io/fs"
	"net/http"
	"strings"
)

// SPAHandler обрабатывает раздачу статических файлов Svelte (Vite)
// и реализует fallback на index.html для роутинга на стороне клиента.
type SPAHandler struct {
	staticFS fs.FS
}

// NewSPAHandler создает новый обработчик SPA
func NewSPAHandler(staticFS fs.FS) *SPAHandler {
	return &SPAHandler{staticFS: staticFS}
}

func (h *SPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Убираем ведущий слэш для поиска в fs.FS
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	// Пытаемся открыть файл
	f, err := h.staticFS.Open(path)
	if err == nil {
		f.Close()
		// Файл существует, отдаем его
		http.FileServer(http.FS(h.staticFS)).ServeHTTP(w, r)
		return
	}

	// Если файл не найден (например, это SPA роут типа /settings), 
	// отдаем index.html
	r.URL.Path = "/"
	http.FileServer(http.FS(h.staticFS)).ServeHTTP(w, r)
}
