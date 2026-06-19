package eval

import (
	"os"
	"path/filepath"
	"strings"
)

// lookupCommand finds the full path for a command name using PATH + PATHEXT.
// Returns (path, true) on success, ("", false) if not found.
func lookupCommand(name string) (string, bool) {
	pathext := pathExtensions()

	// If name contains a path separator, resolve directly.
	if strings.ContainsAny(name, `/\`) {
		return resolveWithExt(name, pathext)
	}

	// Search each directory in PATH.
	pathDirs := filepath.SplitList(os.Getenv("PATH"))
	for _, dir := range pathDirs {
		full := filepath.Join(dir, name)
		if p, ok := resolveWithExt(full, pathext); ok {
			return p, true
		}
	}
	return "", false
}

// resolveWithExt tries path as-is, then path+each PATHEXT extension.
func resolveWithExt(path string, exts []string) (string, bool) {
	// Try exact name first (works for extensionless scripts on non-Windows, or already has ext)
	if isExecutable(path) {
		return path, true
	}
	// Try with each PATHEXT extension
	for _, ext := range exts {
		candidate := path + ext
		if isExecutable(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// pathExtensions returns the list of extensions to try, from PATHEXT env var.
// Falls back to a sensible Windows default.
func pathExtensions() []string {
	pe := os.Getenv("PATHEXT")
	if pe == "" {
		pe = ".EXE;.CMD;.BAT;.PS1"
	}
	parts := strings.Split(pe, string(os.PathListSeparator))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, strings.ToUpper(p))
		}
	}
	return out
}
