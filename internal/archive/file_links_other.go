//go:build !unix && !windows

package archive

import "os"

func archiveHardlinked(_ string, _ os.FileInfo) (bool, error) {
	// These targets do not expose a link count through the supported OS APIs.
	return false, nil
}
