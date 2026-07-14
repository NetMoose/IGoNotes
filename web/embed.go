package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// GetDistFS возвращает файловую систему с содержимым папки dist
func GetDistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
