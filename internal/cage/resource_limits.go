package cage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sylvinus/agent-vm/internal/host"
)

// ResourceAllocation is separate from network configuration: changing a VM's
// capacity does not change its network authorization fingerprint.
type ResourceAllocation struct {
	Automatic bool `json:"automatic"`
	MemoryGiB int  `json:"memory_gib"`
	CPUs      int  `json:"cpus"`
}

func ResourceAllocationPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "resource-limits.json")
}

// An automatically sized VM may grow to three eighths of host memory, at least
// AutomaticMemoryFloorGiB and at most AutomaticMemoryCapGiB, and never beyond
// the host share (half of host memory by default), which also bounds a
// manually chosen allocation. The balloon returns what the guest does not use,
// so a larger host lends more without starting every VM with half of it.
const (
	AutomaticMemoryFloorGiB = 8
	AutomaticMemoryCapGiB   = 32
	// AutomaticFixedMemoryGiB caps the automatic memory of a guest without a
	// memory balloon, such as macOS: it keeps everything it boots with.
	AutomaticFixedMemoryGiB = 8
)

// AutomaticMemoryGiB is the memory ceiling of an automatic allocation on a
// host whose share is shareGiB. Inside a tenant VM the host is that VM, so the
// share keeps half of it for the VM's own system.
func AutomaticMemoryGiB(shareGiB int) int {
	return min(shareGiB, max(AutomaticMemoryFloorGiB, host.MemGiB()*3/8), AutomaticMemoryCapGiB)
}

func (a ResourceAllocation) Limits() (memory, cpus int, err error) {
	memory, cpus = ResourceLimits()
	if a.Automatic {
		return AutomaticMemoryGiB(memory), cpus, nil
	}
	if a.MemoryGiB < 2 || a.MemoryGiB > memory || a.CPUs < 1 || a.CPUs > cpus {
		return 0, 0, fmt.Errorf("内存上限须为 2–%d GiB，CPU 须为 1–%d 个逻辑核心", memory, cpus)
	}
	return a.MemoryGiB, a.CPUs, nil
}

func LoadResourceAllocation(configPath string) (ResourceAllocation, error) {
	a := ResourceAllocation{Automatic: true}
	b, err := os.ReadFile(ResourceAllocationPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	return a, nil
}

func ConfiguredResourceLimits(configPath string) (int, int, error) {
	a, err := LoadResourceAllocation(configPath)
	if err != nil {
		return 0, 0, err
	}
	return a.Limits()
}
