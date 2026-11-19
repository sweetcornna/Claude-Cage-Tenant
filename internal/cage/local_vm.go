package cage

import (
	"encoding/hex"
	"errors"
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

// LocalVMProfiles lists every profile of the desktop VM family that contains
// profile: the family's default profile first, then each VM directory that has
// a regular manifest. Callers use it to keep host resources that VMs would
// otherwise share, such as loopback proxy ports and private storage, assigned
// to exactly one VM. It grants no account or workspace access. A VM directory
// that exists but cannot be read is an error, never an empty family.
func LocalVMProfiles(profile string) ([]string, error) {
	family := LocalVMFamily(profile)
	profiles := []string{family}
	entries, err := os.ReadDir(filepath.Join(family, LocalVMDir))
	if errors.Is(err, os.ErrNotExist) {
		return profiles, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		dir := filepath.Join(family, LocalVMDir, entry.Name())
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || LocalVMFamily(dir) != family {
			continue
		}
		profiles = append(profiles, dir)
	}
	return profiles, nil
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
