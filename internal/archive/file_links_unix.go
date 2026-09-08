//go:build unix

package archive

import (
	"os"
	"syscall"
)

func archiveHardlinked(_ string, info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1, nil
}
