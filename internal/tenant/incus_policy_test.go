package tenant

import (
	"maps"
	"testing"
)

func TestIncusProfilePolicy(t *testing.T) {
	runtime := IncusRuntime{cfg: IncusConfig{Project: "cage-tenant", Profile: "default"}}
	profile := incusProfile{
		Name: "default", Config: map[string]string{"limits.cpu": "2", "limits.memory": "1GiB", "boot.autostart": "true"},
		Devices: map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "tenant-pool", "size": "10GiB"}},
	}
	if err := runtime.validateProfile(profile); err != nil {
		t.Fatalf("isolated profile: %v", err)
	}
	for _, change := range []struct {
		name  string
		apply func(incusProfile)
	}{
		{"missing CPU", func(candidate incusProfile) { delete(candidate.Config, "limits.cpu") }},
		{"invalid memory", func(candidate incusProfile) { candidate.Config["limits.memory"] = "0GiB" }},
		{"overflowing size", func(candidate incusProfile) { candidate.Devices["root"]["size"] = "9223372036854775807GiB" }},
		{"missing size", func(candidate incusProfile) { delete(candidate.Devices["root"], "size") }},
		{"external root source", func(candidate incusProfile) { candidate.Devices["root"]["source"] = "/unmanaged" }},
		{"additional disk", func(candidate incusProfile) { candidate.Devices["data"] = map[string]string{"type": "disk"} }},
		{"additional socket", func(candidate incusProfile) { candidate.Devices["socket"] = map[string]string{"type": "unix-char"} }},
		{"proxy device", func(candidate incusProfile) { candidate.Devices["proxy"] = map[string]string{"type": "proxy"} }},
		{"unapproved NIC", func(candidate incusProfile) {
			candidate.Devices["eth0"] = map[string]string{"type": "nic", "network": "tenant-net"}
		}},
		{"raw config", func(candidate incusProfile) { candidate.Config["raw.qemu"] = "configured" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := incusProfile{Name: profile.Name, Config: maps.Clone(profile.Config), Devices: map[string]map[string]string{"root": maps.Clone(profile.Devices["root"])}}
			change.apply(candidate)
			if err := runtime.validateProfile(candidate); err == nil {
				t.Fatal("unsafe profile accepted")
			}
		})
	}
	runtime.cfg.Network = "tenant-net"
	profile.Devices["eth0"] = map[string]string{"type": "nic", "network": "tenant-net", "name": "eth0"}
	if err := runtime.validateProfile(profile); err != nil {
		t.Fatalf("trusted managed NIC: %v", err)
	}
	profile.Devices["eth0"]["parent"] = "unexpected"
	if err := runtime.validateProfile(profile); err == nil {
		t.Fatal("NIC with extra host settings accepted")
	}
}

func TestIncusExpandedInstancePolicy(t *testing.T) {
	runtime := IncusRuntime{cfg: IncusConfig{Project: "cage-tenant", Profile: "default"}}
	profile := incusProfile{
		Name: "default", Config: map[string]string{"limits.cpu": "4", "limits.memory": "8GiB"},
		Devices: map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "tenant-pool", "size": "10GiB"}},
	}
	record := instance{
		Profiles: []string{"default"}, ExpandedConfig: maps.Clone(profile.Config),
		ExpandedDevices: map[string]map[string]string{"root": maps.Clone(profile.Devices["root"])},
	}
	record.ExpandedConfig["volatile.uuid"] = "instance-id"
	record.ExpandedConfig["image.os"] = "Debian"
	if err := runtime.validateInstance(&record, profile); err != nil {
		t.Fatalf("expanded secure example: %v", err)
	}
	for _, change := range []struct {
		name  string
		apply func(*instance)
	}{
		{"extra profile", func(candidate *instance) { candidate.Profiles = []string{"default", "other"} }},
		{"ephemeral instance", func(candidate *instance) { candidate.Ephemeral = true }},
		{"missing memory", func(candidate *instance) { delete(candidate.ExpandedConfig, "limits.memory") }},
		{"different CPU", func(candidate *instance) { candidate.ExpandedConfig["limits.cpu"] = "8" }},
		{"different disk size", func(candidate *instance) { candidate.ExpandedDevices["root"]["size"] = "20GiB" }},
		{"extra device", func(candidate *instance) { candidate.ExpandedDevices["extra"] = map[string]string{"type": "proxy"} }},
		{"raw config", func(candidate *instance) { candidate.ExpandedConfig["raw.qemu"] = "configured" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := record
			candidate.ExpandedConfig = maps.Clone(record.ExpandedConfig)
			candidate.ExpandedDevices = map[string]map[string]string{"root": maps.Clone(record.ExpandedDevices["root"])}
			change.apply(&candidate)
			if err := runtime.validateInstance(&candidate, profile); err == nil {
				t.Fatal("mismatched effective configuration accepted")
			}
		})
	}
}
