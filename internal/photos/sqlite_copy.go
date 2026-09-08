package photos

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/openclaw/crawlkit/store"
)

type sqliteFileState struct {
	exists      bool
	size        int64
	modifiedNS  int64
	contentHash [sha256.Size]byte
	info        os.FileInfo
}

// CopySQLite reads source files only; SQLite opens only the private copy.
func CopySQLite(ctx context.Context, liveDBPath string, tempPrefix string) (string, func(), error) {
	return copySQLiteWithVerifier(ctx, liveDBPath, tempPrefix, verifySQLiteSnapshot)
}

func copySQLiteWithVerifier(ctx context.Context, liveDBPath, tempPrefix string, verify func(context.Context, string) error) (string, func(), error) {
	resolved, err := filepath.EvalSymlinks(liveDBPath)
	if err != nil {
		return "", func() {}, err
	}
	liveDBPath = resolved
	dir, err := os.MkdirTemp("", tempPrefix)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dest := filepath.Join(dir, filepath.Base(liveDBPath))
	for attempt := 1; attempt <= 5; attempt++ {
		if err := ctx.Err(); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("snapshot SQLite source %s: %w", liveDBPath, err)
		}
		_ = os.Remove(dest)
		_ = os.Remove(dest + "-wal")
		_ = os.Remove(dest + "-shm")
		_ = os.Remove(dest + "-journal")
		before, err := statSQLiteFiles(liveDBPath)
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		copied, err := copySQLiteFiles(ctx, liveDBPath, dest, before)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			cleanup()
			return "", func() {}, err
		}
		after, err := hashSQLiteFiles(ctx, liveDBPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			cleanup()
			return "", func() {}, err
		}
		if sqliteFilesStable(before, copied, after) {
			if err := verify(ctx, dest); err == nil {
				return dest, cleanup, nil
			}
			if err := ctx.Err(); err != nil {
				cleanup()
				return "", func() {}, fmt.Errorf("snapshot SQLite source %s: %w", liveDBPath, err)
			}
		}
	}
	cleanup()
	if err := ctx.Err(); err != nil {
		return "", func() {}, fmt.Errorf("snapshot SQLite source %s: %w", liveDBPath, err)
	}
	return "", func() {}, fmt.Errorf("create consistent SQLite snapshot: source kept changing during 5 attempts")
}

func statSQLiteFiles(dbPath string) (map[string]sqliteFileState, error) {
	out := map[string]sqliteFileState{}
	for _, suffix := range []string{"", "-wal", "-journal"} {
		path := dbPath + suffix
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			out[suffix] = sqliteFileState{}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect SQLite source %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("SQLite source must be a regular file")
		}
		out[suffix] = sqliteFileState{exists: true, size: info.Size(), modifiedNS: info.ModTime().UnixNano(), info: info}
	}
	if !out[""].exists {
		return nil, fmt.Errorf("SQLite source does not exist: %s", dbPath)
	}
	return out, nil
}

func copySQLiteFiles(ctx context.Context, sourceDB, destDB string, before map[string]sqliteFileState) (map[string]sqliteFileState, error) {
	out := map[string]sqliteFileState{}
	for _, suffix := range []string{"", "-wal", "-journal"} {
		if !before[suffix].exists {
			out[suffix] = sqliteFileState{}
			continue
		}
		digest, err := copyFileWithHash(ctx, sourceDB+suffix, destDB+suffix, before[suffix].size)
		if err != nil {
			return nil, err
		}
		state := before[suffix]
		state.contentHash = digest
		out[suffix] = state
	}
	return out, nil
}

func hashSQLiteFiles(ctx context.Context, dbPath string) (map[string]sqliteFileState, error) {
	states, err := statSQLiteFiles(dbPath)
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-journal"} {
		state := states[suffix]
		if !state.exists {
			continue
		}
		file, err := os.Open(dbPath + suffix)
		if err != nil {
			return nil, fmt.Errorf("open SQLite source %s: %w", dbPath+suffix, err)
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, contextReader{ctx, io.LimitReader(file, state.size)})
		closeErr := file.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("hash SQLite source %s: %w", dbPath+suffix, copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close SQLite source %s: %w", dbPath+suffix, closeErr)
		}
		copy(state.contentHash[:], digest.Sum(nil))
		states[suffix] = state
	}
	final, err := statSQLiteFiles(dbPath)
	if err != nil {
		return nil, err
	}
	for suffix, state := range states {
		after := final[suffix]
		if state.exists != after.exists || (state.exists && (!os.SameFile(state.info, after.info) ||
			state.size != after.size || state.modifiedNS != after.modifiedNS)) {
			return nil, os.ErrNotExist
		}
	}
	return states, nil
}

func sqliteFilesStable(before, copied, after map[string]sqliteFileState) bool {
	for _, suffix := range []string{"", "-wal", "-journal"} {
		if before[suffix].exists != after[suffix].exists || copied[suffix].exists != after[suffix].exists {
			return false
		}
		if !after[suffix].exists {
			continue
		}
		if !os.SameFile(before[suffix].info, after[suffix].info) ||
			before[suffix].size != after[suffix].size || before[suffix].modifiedNS != after[suffix].modifiedNS {
			return false
		}
		if copied[suffix].contentHash != after[suffix].contentHash {
			return false
		}
	}
	return true
}

func verifySQLiteSnapshot(ctx context.Context, path string) error {
	// Recover copied hot journals only in the private directory. The source is
	// never opened by SQLite, so its writer locks and sidecars remain untouched.
	db, err := store.Open(ctx, store.Options{Path: path})
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.DB().QueryRowContext(ctx, `pragma quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite snapshot quick check: %s", result)
	}
	return nil
}

func copyFileWithHash(ctx context.Context, source, dest string, size int64) ([sha256.Size]byte, error) {
	var outHash [sha256.Size]byte
	in, err := os.Open(source)
	if err != nil {
		return outHash, fmt.Errorf("open %s: %w", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return outHash, fmt.Errorf("create %s: %w", dest, err)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, digest), contextReader{ctx, io.LimitReader(in, size)})
	closeErr := out.Close()
	if copyErr != nil {
		return outHash, fmt.Errorf("copy %s: %w", source, copyErr)
	}
	if closeErr != nil {
		return outHash, fmt.Errorf("close %s: %w", dest, closeErr)
	}
	copy(outHash[:], digest.Sum(nil))
	return outHash, nil
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
