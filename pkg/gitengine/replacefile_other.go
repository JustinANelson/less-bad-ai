//go:build !windows

package gitengine

import "os"

func replaceFileAtomically(source, destination string) error {
	return os.Rename(source, destination)
}
