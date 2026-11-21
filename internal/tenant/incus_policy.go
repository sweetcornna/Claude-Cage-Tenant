package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

type incusProfile struct {
	Name    string                       `json:"name"`
	Config  map[string]string            `json:"config"`
	Devices map[string]map[string]string `json:"devices"`
}

func (r *IncusRuntime) loadProfile(ctx context.Context) (incusProfile, error) {
	var project struct {
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}
	var profile incusProfile
	body, err := captureIncus(r.localCommand(ctx, "query", "local:/1.0/projects/"+r.cfg.Project), 1<<20)
	if err != nil {
		return profile, err
	}
	if json.Unmarshal(body, &project) != nil || project.Name != r.cfg.Project || project.Config["features.profiles"] != "true" {
		return profile, errors.New("Incus project must have its own profiles")
	}
	body, err = captureIncus(r.localCommand(ctx, "query", "local:/1.0/profiles/"+r.cfg.Profile+"?project="+r.cfg.Project), 1<<20)
	if err != nil {
		return profile, err
	}
	if json.Unmarshal(body, &profile) != nil || profile.Name != r.cfg.Profile {
		return profile, errors.New("trusted Incus profile is unavailable")
	}
	return profile, r.validateProfile(profile)
}

func (r *IncusRuntime) validateProfile(profile incusProfile) error {
	cpu, err := strconv.ParseInt(profile.Config["limits.cpu"], 10, 32)
	if err != nil || cpu < 1 || !positiveIncusSize(profile.Config["limits.memory"]) {
		return errors.New("Incus profile needs explicit CPU and memory limits")
	}
	if !r.safeInstanceConfig(profile.Config) {
		return errors.New("Incus profile contains an unsupported configuration key")
	}
	roots, nics := 0, 0
	for _, device := range profile.Devices {
		switch device["type"] {
		case "disk":
			if len(device) != 4 || device["path"] != "/" || device["pool"] == "" || !positiveIncusSize(device["size"]) {
				return errors.New("Incus profile disk must be a managed root disk with an explicit size")
			}
			roots++
		case "nic":
			if r.cfg.Network == "" || device["network"] != r.cfg.Network || (len(device) != 2 && (len(device) != 3 || device["name"] == "")) {
				return errors.New("Incus profile NIC must use only the configured managed network")
			}
			nics++
		default:
			return errors.New("Incus profile contains an unsupported device type")
		}
	}
	if roots != 1 || nics > 1 {
		return errors.New("Incus profile needs one managed root disk and at most one approved NIC")
	}
	return nil
}

func (r *IncusRuntime) validateInstance(record *instance, profile incusProfile) error {
	if record.Ephemeral {
		return errors.New("Incus instance must have persistent storage")
	}
	if len(record.Profiles) != 1 || record.Profiles[0] != r.cfg.Profile {
		return errors.New("Incus instance must use only the trusted project profile")
	}
	if !r.safeInstanceConfig(record.ExpandedConfig) {
		return errors.New("Incus instance configuration does not match the trusted CPU and memory policy")
	}
	for _, key := range r.pinnedKeys() {
		if record.ExpandedConfig[key] != profile.Config[key] {
			return errors.New("Incus instance configuration does not match the trusted CPU and memory policy")
		}
	}
	if r.instanceType() == InstanceVM && !elasticMemory(record.ExpandedConfig["limits.memory"], profile.Config["limits.memory"]) {
		return errors.New("Incus instance configuration does not match the trusted CPU and memory policy")
	}
	if !maps.EqualFunc(record.ExpandedDevices, profile.Devices, maps.Equal[map[string]string]) {
		return errors.New("Incus instance devices do not match the trusted profile")
	}
	return nil
}

// containerKeys are the extra settings a trusted container profile may use.
// Containers share the host kernel: nesting runs Cage's rootless engine inside
// an unprivileged container, the ID map keys give it its own range, swap lets
// a frozen container leave memory, and limits.processes caps its tasks.
// Privileged containers and every raw.* key stay refused.
var containerKeys = []string{
	"security.nesting", "security.idmap.isolated", "security.idmap.size",
	"security.syscalls.intercept.mknod", "security.syscalls.intercept.setxattr",
	"limits.memory.swap", "limits.memory.swap.priority", "limits.processes",
}

// pinnedKeys are the resource and isolation keys whose effective value must
// come from the trusted profile alone. A VM's limits.memory is not among them:
// the profile value is its ceiling, see elasticMemory.
func (r *IncusRuntime) pinnedKeys() []string {
	if r.instanceType() == InstanceVM {
		return []string{"limits.cpu", "raw.qemu.conf"}
	}
	return append([]string{"limits.cpu", "limits.memory"}, containerKeys...)
}

// MinimumMemory is the smallest instance-local limits.memory the gateway
// accepts on a VM, and so the lowest -memory-min the balancer may use.
const MinimumMemory = 1 << 30

// elasticMemory reports whether a VM's effective limits.memory is the trusted
// profile value, or a smaller instance-local value such as the memory balancer
// sets: an exact Incus size from MinimumMemory (the ceiling when that is
// lower) up to the profile ceiling. Container limits stay pinned: a cgroup
// limit below a container's use starts its OOM killer.
func elasticMemory(value, ceiling string) bool {
	if value == ceiling {
		return true
	}
	limit, ok := incusSizeBytes(value)
	maximum, valid := incusSizeBytes(ceiling)
	return ok && valid && limit >= min(maximum, MinimumMemory) && limit <= maximum
}

// freePageReporting is the only raw.qemu.conf a VM profile may carry. Incus
// 6.0 creates the virtio-balloon device without free page reporting (later
// Incus releases enable it); this override adds that one property, so memory
// a guest frees returns to the host without a balloon change.
const freePageReporting = "[device \"qemu_balloon\"]\nfree-page-reporting = \"on\""

func (r *IncusRuntime) safeInstanceConfig(config map[string]string) bool {
	for key, value := range config {
		switch {
		case key == "limits.cpu" || key == "limits.memory" || key == "boot.autostart" || key == "boot.autostart.delay" || key == "boot.autostart.priority":
		case key == "security.secureboot" && r.instanceType() == InstanceVM:
		case key == "raw.qemu.conf" && r.instanceType() == InstanceVM && strings.TrimSpace(value) == freePageReporting:
		case slices.Contains(containerKeys, key) && r.instanceType() == InstanceContainer:
		default:
			if !strings.HasPrefix(key, "volatile.") && !strings.HasPrefix(key, "image.") && !strings.HasPrefix(key, "user.") {
				return false
			}
		}
	}
	return true
}

func positiveIncusSize(value string) bool {
	_, ok := incusSizeBytes(value)
	return ok
}

var incusSizeUnits = map[string]int64{
	"": 1, "B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
	"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
}

// incusSizeBytes converts a positive whole Incus size such as 8GiB or 500MB
// to bytes. Fractions, percentages, signs, unknown units and sizes that do not
// fit in an int64 are refused.
func incusSizeBytes(value string) (int64, bool) {
	index := strings.IndexFunc(value, func(char rune) bool { return char < '0' || char > '9' })
	if index < 0 {
		index = len(value)
	}
	count, err := strconv.ParseInt(value[:index], 10, 64)
	unit, known := incusSizeUnits[value[index:]]
	if err != nil || count <= 0 || !known || count > math.MaxInt64/unit {
		return 0, false
	}
	return count * unit, true
}

// ParseMemorySize reads a memory size flag written as an Incus size, such as
// 2GiB.
func ParseMemorySize(value string) (int64, bool) {
	return incusSizeBytes(value)
}
