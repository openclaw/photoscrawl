package archive

import (
	"errors"
	"path/filepath"
	"strings"
)

func archiveFilename(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Match CrawlKit's trim after Abs without changing literal source paths.
	filename := strings.TrimSpace(absolute)
	if filepath.Clean(filename) != filename {
		return "", errors.New("archive filename becomes unstable after trimming whitespace")
	}
	return filename, nil
}
