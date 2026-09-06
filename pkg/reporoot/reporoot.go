// Package reporoot locates the repository root from the current working
// directory by walking up until a directory containing go.mod is found.
package reporoot

import (
	"os"
	"path/filepath"
)

// Find returns the absolute directory that contains go.mod, walking up from
// cwd. It returns "." when no go.mod is found on the way to the filesystem
// root, matching the historical contract of the CLI tools that call it.
func Find() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
