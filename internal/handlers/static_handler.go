package handlers

import (
	"io/fs"
	"net/http"
	"path"
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
	// Очищаем путь
	cleanPath := path.Clean(r.URL.Path)
	p := strings.TrimPrefix(cleanPath, "/")
	if p == "" {
		p = "index.html"
	}

	if !fs.ValidPath(p) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	// Пытаемся открыть файл
	f, err := h.staticFS.Open(p)
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
