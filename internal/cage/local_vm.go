package cage

import (
	"encoding/hex"
	"os"
	"path/filepath"
)

const LocalVMDir = "vms"

// LocalVMFamily returns the desktop profile that contains this VM's records.
// This only selects a cache for public system images; it grants no account or
// workspace access.
func LocalVMFamily(profile string) string {
	id := filepath.Base(profile)
	group := filepath.Dir(profile)
	if len(id) == 16 && filepath.Base(group) == LocalVMDir {
		if _, err := hex.DecodeString(id); err == nil {
			if info, err := os.Lstat(filepath.Join(profile, "vm.json")); err == nil && info.Mode().IsRegular() {
				return filepath.Dir(group)
			}
		}
	}
	return profile
}

// LinuxImageCache is shared by default-volume profiles and sits outside their
// movable VM state. Custom volumes keep their existing separate caches.
func LinuxImageCache(profile string, storage StorageLocation) string {
	family := LocalVMFamily(profile)
	if storage.Custom || filepath.Clean(storage.Directory) != storageAnchor(profile) || (family == profile && filepath.Base(profile) != ".claude-cage") {
		return ""
	}
	return filepath.Join(family, "linux-image-cache")
}
