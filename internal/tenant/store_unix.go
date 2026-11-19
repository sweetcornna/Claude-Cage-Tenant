//go:build !windows

package tenant

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockStore(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func privateFile(info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }

func syncDirectory(file *os.File) error { return file.Sync() }
