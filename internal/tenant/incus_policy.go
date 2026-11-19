package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
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
// come from the trusted profile alone.
func (r *IncusRuntime) pinnedKeys() []string {
	keys := []string{"limits.cpu", "limits.memory"}
	if r.instanceType() == InstanceContainer {
		keys = append(keys, containerKeys...)
	}
	return keys
}

func (r *IncusRuntime) safeInstanceConfig(config map[string]string) bool {
	for key := range config {
		switch {
		case key == "limits.cpu" || key == "limits.memory" || key == "boot.autostart" || key == "boot.autostart.delay" || key == "boot.autostart.priority":
		case key == "security.secureboot" && r.instanceType() == InstanceVM:
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
	index := strings.IndexFunc(value, func(char rune) bool { return char < '0' || char > '9' })
	if index < 0 {
		index = len(value)
	}
	count, err := strconv.ParseInt(value[:index], 10, 64)
	if err != nil || count <= 0 {
		return false
	}
	const maxSize = 1<<63 - 1
	switch value[index:] {
	case "", "B":
		return true
	case "kB":
		return count <= maxSize/1000
	case "MB":
		return count <= maxSize/1000000
	case "GB":
		return count <= maxSize/1000000000
	case "TB":
		return count <= maxSize/1000000000000
	case "KiB":
		return count <= maxSize>>10
	case "MiB":
		return count <= maxSize>>20
	case "GiB":
		return count <= maxSize>>30
	case "TiB":
		return count <= maxSize>>40
	default:
		return false
	}
}
