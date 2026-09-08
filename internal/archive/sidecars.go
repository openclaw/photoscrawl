package archive

import (
	"errors"
	"os"
	"path/filepath"
)

func inspectArchiveSidecars(path string, missing bool) error {
	base, err := archiveSidecarBase(path, 0)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(base + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if missing || !info.Mode().IsRegular() {
			return errors.New("archive has an unowned or non-regular SQLite sidecar")
		}
		linked, err := archiveHardlinked(base+suffix, info)
		if err != nil {
			return err
		}
		if linked {
			return errors.New("archive has a hardlinked SQLite sidecar")
		}
	}
	return nil
}

func archiveSidecarBase(path string, links int) (string, error) {
	if links > 255 {
		return "", errors.New("too many symbolic links in archive path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	info, statErr := os.Lstat(path)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return archiveSidecarBase(target, links+1)
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = archiveSidecarBase(parent, links)
	return filepath.Join(resolved, filepath.Base(path)), err
}
