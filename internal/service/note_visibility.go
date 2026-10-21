package service

import "strings"

// noteDirectoryVisible is the notes tree's directory policy, shared by scanning,
// Clipper discovery and new note destinations. Apply it to every parent component,
// not to note filenames or server-managed attachment paths such as assets/images.
func noteDirectoryVisible(name string) bool {
	return !strings.HasPrefix(name, ".") && !strings.EqualFold(name, "assets")
}
