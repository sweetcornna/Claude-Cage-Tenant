//go:build !darwin && !linux

package workbench

import "errors"

var errLocalVMCloneUnsupported = errors.New("copy-on-write file cloning is unavailable")

func cloneLocalVMFile(string, string) error {
	return errLocalVMCloneUnsupported
}
