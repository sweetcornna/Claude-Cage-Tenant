//go:build darwin

package workbench

import "golang.org/x/sys/unix"

// cloneLocalVMFile uses APFS copy-on-write. The destination is a new inode;
// neither profile can write through the other's file.
func cloneLocalVMFile(source, destination string) error {
	return unix.Clonefile(source, destination, 0)
}
