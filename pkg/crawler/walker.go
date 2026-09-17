package crawler

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// IgnoreDirs contains common directories we want to skip to save time and memory
var IgnoreDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".venv":        true,
}

// Walk finds all source files in the rootDir and sends them to the filesChannel.
// It runs concurrently in a goroutine and closes the channel when done.
func Walk(rootDir string, filesChan chan<- string) {
	// We span a goroutine so the Walk function returns immediately.
	// this allows the caller to start consuming from the channel rightaway.
	go func() {
		// ensure the channel is closed when the walk finishes, signaling workers to stop.
		defer close(filesChan)
		filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// if we hit permission issues or missing files, ignore and continue
				return nil
			}
			if d.IsDir() {
				// if its a directory -> we want to ignore, tell WalkDir to skip it entirely.
				if IgnoreDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			// We filter but extensions
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".py" || ext == ".go" || ext == ".js" || ext == ".ts" {
				// send the discovered file path into our pipeline
				// this blocks if the channel is full, creating natural backpressure.
				filesChan <- path
			}
			return nil
		})
	}()
}
