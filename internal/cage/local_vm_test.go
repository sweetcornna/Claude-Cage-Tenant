package cage

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLocalVMProfilesListsOnlyManifestedVMsOfOneFamily(t *testing.T) {
	family := t.TempDir()
	vm := filepath.Join(family, LocalVMDir, "0123456789abcdef")
	for _, dir := range []string{vm, filepath.Join(family, LocalVMDir, "fedcba9876543210"), filepath.Join(family, LocalVMDir, "not-a-vm")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(vm, "vm.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{family, vm}
	for _, profile := range want {
		if got, err := LocalVMProfiles(profile); err != nil || !slices.Equal(got, want) {
			t.Fatalf("LocalVMProfiles(%q) = %q, %v, want %q", profile, got, err, want)
		}
	}
	other := t.TempDir()
	if got, err := LocalVMProfiles(other); err != nil || !slices.Equal(got, []string{other}) {
		t.Fatalf("unrelated profile = %q, %v", got, err)
	}
	// A VM directory that is not a readable directory is never an empty family.
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, LocalVMDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LocalVMProfiles(broken); err == nil {
		t.Fatalf("unreadable VM directory listed as %q", got)
	}
}
