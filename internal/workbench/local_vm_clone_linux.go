//go:build linux

package workbench

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneLocalVMFile uses the file system's reflink support. The destination is
// a new inode; a file system without FICLONE reports an error and the caller
// leaves the media to the normal per-profile download path.
func cloneLocalVMFile(source, destination string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())); err != nil {
		_ = dst.Close()
		_ = os.Remove(destination)
		return err
	}
	return dst.Close()
}
