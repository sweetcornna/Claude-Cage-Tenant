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

func TestIncusSizeBytes(t *testing.T) {
	for value, want := range map[string]int64{
		"1": 1, "512B": 512, "2kB": 2000, "3MB": 3e6, "4GB": 4e9, "1TB": 1e12,
		"2KiB": 2 << 10, "4096MiB": 4 << 30, "10GiB": 10 << 30, "1TiB": 1 << 40, "8388607TiB": 8388607 << 40,
	} {
		if got, ok := incusSizeBytes(value); !ok || got != want {
			t.Errorf("%q = %d %v, want %d", value, got, ok, want)
		}
	}
	for _, value := range []string{"", "0", "0GiB", "-1GiB", "+1GiB", "1.5GiB", "50%", "1 GiB", "1GiB ", "1gib", "1Gi", "8388608TiB", "9223372036854775807GiB", "99999999999999999999"} {
		if got, ok := incusSizeBytes(value); ok {
			t.Errorf("%q accepted as %d", value, got)
		}
	}
}

func TestIncusVMMemoryCeilingPolicy(t *testing.T) {
	runtime := IncusRuntime{cfg: IncusConfig{Project: "cage-tenant", Profile: "default"}}
	profile := incusProfile{
		Name: "default", Config: map[string]string{"limits.cpu": "4", "limits.memory": "10GiB"},
		Devices: map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "tenant-pool", "size": "10GiB"}},
	}
	check := func(local string) error {
		record := instance{Profiles: []string{"default"}, Config: map[string]string{"limits.memory": local}, ExpandedConfig: maps.Clone(profile.Config),
			ExpandedDevices: map[string]map[string]string{"root": maps.Clone(profile.Devices["root"])}}
		record.ExpandedConfig["limits.memory"] = local
		return runtime.validateInstance(&record, profile)
	}
	// What the balancer writes, and the same sizes in other units.
	for _, local := range []string{"10GiB", "10240MiB", "9728MiB", "2048MiB", "1GiB", "1073741824", "5GB"} {
		if err := check(local); err != nil {
			t.Errorf("instance memory %s refused: %v", local, err)
		}
	}
	for _, local := range []string{"", "10241MiB", "11GiB", "1023MiB", "512MiB", "50%", "1.5GiB", "0", "-2GiB", "10GiB ", "9223372036854775807GiB"} {
		if check(local) == nil {
			t.Errorf("instance memory %q accepted", local)
		}
	}
	// A profile below the minimum admits nothing below itself.
	profile.Config["limits.memory"] = "768MiB"
	if check("768MiB") != nil || check("512MiB") == nil || check("1GiB") == nil {
		t.Fatal("small profile ceiling not enforced")
	}
	// limits.cpu stays pinned exactly.
	profile.Config["limits.memory"] = "10GiB"
	record := instance{Profiles: []string{"default"}, ExpandedConfig: map[string]string{"limits.cpu": "2", "limits.memory": "4GiB"},
		ExpandedDevices: map[string]map[string]string{"root": maps.Clone(profile.Devices["root"])}}
	if runtime.validateInstance(&record, profile) == nil {
		t.Fatal("instance CPU override accepted alongside a memory override")
	}
	// Containers keep the exact profile memory.
	containers := IncusRuntime{cfg: IncusConfig{Project: "cage-tenant", Profile: "default", InstanceType: InstanceContainer}}
	record.ExpandedConfig["limits.cpu"] = "4"
	if containers.validateInstance(&record, profile) == nil {
		t.Fatal("container memory override accepted")
	}
}

func TestIncusFreePageReportingOverride(t *testing.T) {
	vms := IncusRuntime{cfg: IncusConfig{Project: "cage-tenant", Profile: "default"}}
	containers := IncusRuntime{cfg: IncusConfig{Project: "cage-tenant", Profile: "default", InstanceType: InstanceContainer}}
	profile := func(raw string) incusProfile {
		config := map[string]string{"limits.cpu": "2", "limits.memory": "2GiB"}
		if raw != "" {
			config["raw.qemu.conf"] = raw
		}
		return incusProfile{Name: "default", Config: config, Devices: map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "tenant-pool", "size": "10GiB"}}}
	}
	canonical := "[device \"qemu_balloon\"]\nfree-page-reporting = \"on\""
	for _, raw := range []string{canonical, canonical + "\n", "\n" + canonical + "\n"} {
		if err := vms.validateProfile(profile(raw)); err != nil {
			t.Fatalf("free page reporting override %q refused: %v", raw, err)
		}
	}
	for _, raw := range []string{
		canonical + "\ndeflate-on-oom = \"on\"",
		"[device \"qemu_balloon\"]\nfree-page-reporting = \"off\"",
		"[device \"qemu_balloon\"]\r\nfree-page-reporting = \"on\"",
		"[device \"qemu_balloon\"]\nfree-page-reporting=on",
		"[global]\ndriver = \"virtio-balloon-pci\"",
		canonical + "\n[device \"qemu_rng\"]\nrng = \"\"",
	} {
		if vms.validateProfile(profile(raw)) == nil {
			t.Errorf("raw.qemu.conf %q accepted", raw)
		}
	}
	if containers.validateProfile(profile(canonical)) == nil {
		t.Fatal("raw.qemu.conf accepted for containers")
	}
	if vms.validateProfile(incusProfile{Name: "default", Config: map[string]string{"limits.cpu": "2", "limits.memory": "2GiB", "raw.qemu": "-device virtio-balloon"},
		Devices: profile("").Devices}) == nil {
		t.Fatal("raw.qemu accepted")
	}
	// The instance's effective value must come from the profile.
	trusted := profile(canonical)
	record := instance{Profiles: []string{"default"}, ExpandedConfig: maps.Clone(trusted.Config), ExpandedDevices: map[string]map[string]string{"root": maps.Clone(trusted.Devices["root"])}}
	if err := vms.validateInstance(&record, trusted); err != nil {
		t.Fatalf("instance with the profile override: %v", err)
	}
	if err := vms.validateInstance(&record, profile("")); err == nil {
		t.Fatal("instance-local raw.qemu.conf accepted")
	}
	delete(record.ExpandedConfig, "raw.qemu.conf")
	if err := vms.validateInstance(&record, trusted); err == nil {
		t.Fatal("instance without the profile override accepted")
	}
}
