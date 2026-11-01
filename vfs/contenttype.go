package vfs

import (
	"path"
	"strings"
)

// ContentTypeFor maps a path's extension to the MIME type aikido uses for
// text files. It returns "" for unknown extensions so each caller picks its
// own fallback.
func ContentTypeFor(filePath string) string {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".html", ".htm":
		return "text/html"
	case ".csv":
		return "text/csv"
	default:
		return ""
	}
}
