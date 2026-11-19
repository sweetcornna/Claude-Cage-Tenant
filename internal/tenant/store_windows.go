package tenant

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockStore(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func privateFile(os.FileInfo) bool { return true }

func syncDirectory(*os.File) error { return nil }
