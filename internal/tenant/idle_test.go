package tenant

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// coolingRuntime adds Incus-like power states to the fake runtime.
type coolingRuntime struct {
	*fakeRuntime
	mu     sync.Mutex
	status map[string]string
	cpu    map[string]time.Duration
	moves  map[string]int
	// stopGate, when set, holds every Stop until it is closed.
	stopGate    chan struct{}
	stopEntered chan string
}

func (c *coolingRuntime) Prepare(ctx context.Context, spec LaunchSpec) (*Workspace, error) {
	workspace, err := c.fakeRuntime.Prepare(ctx, spec)
	if err == nil {
		c.mu.Lock()
		c.status[spec.VMID] = "Running"
		c.mu.Unlock()
	}
	return workspace, err
}

func (c *coolingRuntime) Inventory(context.Context) (map[string]InstanceUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	usage := make(map[string]InstanceUsage)
	for id, status := range c.status {
		usage[id] = InstanceUsage{Status: status, CPU: c.cpu[id]}
	}
	return usage, nil
}

func (c *coolingRuntime) move(verb string, spec LaunchSpec, from, to string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.moves[verb+" "+spec.VMID]++
	if c.status[spec.VMID] == from || from == "" {
		c.status[spec.VMID] = to
	}
	return nil
}

func (c *coolingRuntime) Freeze(_ context.Context, spec LaunchSpec) error {
	return c.move("freeze", spec, "Running", "Frozen")
}
func (c *coolingRuntime) Thaw(_ context.Context, spec LaunchSpec) error {
	return c.move("thaw", spec, "Frozen", "Running")
}
func (c *coolingRuntime) Stop(ctx context.Context, spec LaunchSpec) error {
	if c.stopGate != nil {
		c.stopEntered <- spec.VMID
		select {
		case <-c.stopGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.move("stop", spec, "", "Stopped")
}

func (c *coolingRuntime) count(verb, id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.moves[verb+" "+id]
}

func (c *coolingRuntime) state(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status[id]
}

func (c *coolingRuntime) spend(id string, cpu time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cpu[id] += cpu
}

type coolingFixture struct {
	*portalFixture
	cooler *coolingRuntime
	clock  atomic.Int64
}

func newCoolingFixture(t *testing.T) *coolingFixture {
	base := newFixture(t, false)
	base.server.Close()
	fixture := &coolingFixture{portalFixture: base, cooler: &coolingRuntime{
		fakeRuntime: base.runtime, status: make(map[string]string), cpu: make(map[string]time.Duration), moves: make(map[string]int),
	}}
	base.config.Runtime = fixture.cooler
	base.config.FreezeAfter, base.config.StopAfter, base.config.BusyCPU = 10*time.Minute, time.Hour, 0.05
	base.openServer()
	fixture.clock.Store(time.Now().UnixNano())
	base.server.now = func() time.Time { return time.Unix(0, fixture.clock.Load()) }
	return fixture
}

func (f *coolingFixture) advance(d time.Duration) { f.clock.Add(int64(d)) }

func TestTenantIdleCoolingFreezesAndWakesOnRequest(t *testing.T) {
	fixture := newCoolingFixture(t)
	login, _ := fixture.login("register", "alice")
	record := fixture.create(login, "first")
	cookie := fixture.connect(login, record.ID)

	fixture.server.coolOnce()
	fixture.advance(9 * time.Minute)
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 0 {
		t.Fatal("workspace frozen before its idle time")
	}
	fixture.advance(2 * time.Minute)
	fixture.cooler.spend(record.ID, time.Second)
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 1 || fixture.cooler.state(record.ID) != "Frozen" {
		t.Fatal("idle workspace was not frozen")
	}
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 1 || fixture.cooler.count("stop", record.ID) != 0 {
		t.Fatal("frozen workspace moved again before its stop time")
	}
	fixture.state(login, record.ID, ready)

	response := fixture.request("GET", vmOrigin(record.ID), "/plain", "", cookie)
	wantStatus(t, response, 200, "")
	if response.Body.String() != record.ID || fixture.cooler.count("thaw", record.ID) != 1 || fixture.cooler.state(record.ID) != "Running" {
		t.Fatal("request did not resume the frozen workspace")
	}
	// The resume counts as activity: the next sweep keeps it running.
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 1 {
		t.Fatal("resumed workspace was frozen again at once")
	}
}

func TestTenantIdleCoolingKeepsBusyGuestRunning(t *testing.T) {
	fixture := newCoolingFixture(t)
	login, _ := fixture.login("register", "alice")
	record := fixture.create(login, "agent")
	fixture.connect(login, record.ID)

	fixture.server.coolOnce()
	fixture.advance(11 * time.Minute)
	// An agent working without a browser: 20% of one CPU.
	fixture.cooler.spend(record.ID, 132*time.Second)
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 0 {
		t.Fatal("busy guest was frozen")
	}
	fixture.advance(11 * time.Minute)
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 1 {
		t.Fatal("guest that went quiet was not frozen")
	}
}

func TestTenantIdleCoolingEndsStreamsBeforeFreeze(t *testing.T) {
	fixture := newCoolingFixture(t)
	login, _ := fixture.login("register", "alice")
	record := fixture.create(login, "streams")
	cookie := fixture.connect(login, record.ID)

	finished := make(chan int, 1)
	go func() {
		finished <- fixture.request("GET", vmOrigin(record.ID), "/stream", "", cookie).Code
	}()
	waitFixture(t, func() bool {
		select {
		case code := <-finished:
			t.Fatalf("stream ended early with %d", code)
		default:
		}
		return fixture.runtime.guests[record.ID].dials.Load() >= 2
	})
	time.Sleep(50 * time.Millisecond)
	fixture.advance(11 * time.Minute)
	fixture.server.coolOnce()
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("freeze left a stream open to a frozen guest")
	}
	if fixture.cooler.count("freeze", record.ID) != 1 {
		t.Fatal("idle workspace with an open but silent stream was not frozen")
	}
}

func TestTenantIdleCoolingStopsAndReopens(t *testing.T) {
	fixture := newCoolingFixture(t)
	login, _ := fixture.login("register", "alice")
	record := fixture.create(login, "cold")
	fixture.connect(login, record.ID)

	fixture.advance(11 * time.Minute)
	fixture.server.coolOnce()
	fixture.advance(50 * time.Minute)
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 1 || fixture.cooler.state(record.ID) != "Stopped" || fixture.cooler.count("stop", record.ID) != 1 {
		t.Fatal("long idle frozen workspace was not stopped")
	}
	fixture.state(login, record.ID, stopped)
	fixture.runtime.mu.Lock()
	before := fixture.runtime.calls[record.ID]
	fixture.runtime.mu.Unlock()
	fixture.connect(login, record.ID)
	fixture.runtime.mu.Lock()
	after := fixture.runtime.calls[record.ID]
	fixture.runtime.mu.Unlock()
	if after != before+1 || fixture.cooler.state(record.ID) != "Running" {
		t.Fatal("stopped workspace did not boot again from the portal")
	}
}

func TestTenantIdleCoolingSlowStopDoesNotBlockOtherWorkspaces(t *testing.T) {
	fixture := newCoolingFixture(t)
	login, _ := fixture.login("register", "alice")
	slow, other := fixture.create(login, "slow"), fixture.create(login, "other")
	fixture.connect(login, slow.ID)
	otherCookie := fixture.connect(login, other.ID)
	fixture.cooler.mu.Lock()
	fixture.cooler.status[slow.ID] = "Frozen"
	fixture.cooler.mu.Unlock()
	fixture.cooler.stopGate, fixture.cooler.stopEntered = make(chan struct{}), make(chan string, 1)
	fixture.advance(2 * time.Hour)
	swept := make(chan struct{})
	go func() {
		fixture.server.coolOnce()
		close(swept)
	}()
	select {
	case <-fixture.cooler.stopEntered:
	case <-time.After(4 * time.Second):
		// The other workspace's freeze ran first; its stop is next.
		<-fixture.cooler.stopEntered
	}
	served := make(chan int, 1)
	go func() { served <- fixture.request("GET", vmOrigin(other.ID), "/plain", "", otherCookie).Code }()
	select {
	case code := <-served:
		if code != 200 {
			t.Fatalf("request during another workspace's stop: %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request waited behind another workspace's stop")
	}
	close(fixture.cooler.stopGate)
	<-swept
}

func TestTenantIdleCoolingResumesWorkspaceFrozenBeforeRestart(t *testing.T) {
	fixture := newCoolingFixture(t)
	login, _ := fixture.login("register", "alice")
	record := fixture.create(login, "restart")
	fixture.cooler.mu.Lock()
	fixture.cooler.status[record.ID] = "Frozen"
	fixture.cooler.mu.Unlock()
	fixture.server.coolOnce()
	fixture.connect(login, record.ID)
	if fixture.cooler.count("thaw", record.ID) != 1 || fixture.cooler.state(record.ID) != "Running" {
		t.Fatal("workspace frozen by an earlier gateway was not resumed on open")
	}
}

func TestTenantIdleCoolingConfiguration(t *testing.T) {
	fixture := newFixture(t, false)
	for _, change := range []func(*Config){
		func(c *Config) { c.FreezeAfter = -time.Minute },
		func(c *Config) { c.FreezeAfter = 30 * time.Second },
		func(c *Config) { c.FreezeAfter, c.StopAfter = time.Hour, time.Minute },
		func(c *Config) { c.StopAfter = -time.Hour },
		func(c *Config) { c.BusyCPU = -1 },
	} {
		config := fixture.config
		config.StateDir = filepath.Join(t.TempDir(), "state")
		change(&config)
		if server, err := NewServer(config); err == nil {
			server.Close()
			t.Fatal("invalid idle cooling configuration accepted")
		}
	}
	config := fixture.config
	config.StateDir = filepath.Join(t.TempDir(), "state")
	config.FreezeAfter, config.StopAfter = 15*time.Minute, 0
	server, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if server.cooling.runtime != nil {
		t.Fatal("cooling enabled for a runtime that cannot freeze")
	}
}

func TestIncusContainerPolicy(t *testing.T) {
	containers := IncusRuntime{cfg: IncusConfig{Project: "cage-tenant", Profile: "default", InstanceType: InstanceContainer}}
	vms := IncusRuntime{cfg: IncusConfig{Project: "cage-tenant", Profile: "default"}}
	profile := incusProfile{
		Name: "default",
		Config: map[string]string{"limits.cpu": "2", "limits.memory": "2GiB", "limits.memory.swap": "2GiB", "limits.processes": "4096",
			"security.nesting": "true", "security.idmap.isolated": "true"},
		Devices: map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "tenant-pool", "size": "10GiB"}},
	}
	if err := containers.validateProfile(profile); err != nil {
		t.Fatalf("container profile: %v", err)
	}
	if vms.validateProfile(profile) == nil {
		t.Fatal("container keys accepted for virtual machines")
	}
	for _, key := range []string{"security.privileged", "raw.lxc", "raw.idmap", "security.secureboot", "linux.kernel_modules"} {
		candidate := incusProfile{Name: profile.Name, Config: maps.Clone(profile.Config), Devices: profile.Devices}
		candidate.Config[key] = "true"
		if containers.validateProfile(candidate) == nil {
			t.Fatalf("container profile accepted %s", key)
		}
	}
	record := instance{Profiles: []string{"default"}, ExpandedConfig: maps.Clone(profile.Config), ExpandedDevices: map[string]map[string]string{"root": maps.Clone(profile.Devices["root"])}}
	if err := containers.validateInstance(&record, profile); err != nil {
		t.Fatalf("container instance: %v", err)
	}
	for _, key := range []string{"security.nesting", "limits.memory.swap", "limits.processes", "security.idmap.isolated"} {
		candidate := record
		candidate.ExpandedConfig = maps.Clone(record.ExpandedConfig)
		candidate.ExpandedConfig[key] = "changed"
		if containers.validateInstance(&candidate, profile) == nil {
			t.Fatalf("instance override of %s accepted", key)
		}
	}
	candidate := record
	candidate.ExpandedConfig = maps.Clone(record.ExpandedConfig)
	candidate.ExpandedConfig["security.syscalls.intercept.mknod"] = "true"
	if containers.validateInstance(&candidate, profile) == nil {
		t.Fatal("instance-only isolation setting accepted")
	}
	if _, err := NewIncus(IncusConfig{Executable: "/usr/bin/incus", Project: "cage-tenant", Profile: "default", ControlPath: "/home/cage/.claude-cage/control.json", InstanceType: "lxc"}); err == nil {
		t.Fatal("unknown instance type accepted")
	}
}

func TestIncusInventoryKeepsOwnedInstances(t *testing.T) {
	owned, other := strings.Repeat("1", 32), strings.Repeat("2", 32)
	fixture := `[
 {"name":"cage-` + owned + `","type":"container","status":"Frozen","config":{"user.cage-tenant.id":"` + owned + `"},"state":{"cpu":{"usage":2500000000},"memory":{"usage":3145728,"swap_usage":26214400}}},
 {"name":"cage-` + other + `","type":"virtual-machine","status":"Running","config":{"user.cage-tenant.id":"` + other + `"},"state":{"cpu":{"usage":1}}},
 {"name":"renamed","type":"container","status":"Running","config":{"user.cage-tenant.id":"` + other + `"}},
 {"name":"cage-` + other + `","type":"container","status":"Running","config":{}}
]`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inventory.json"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "incus")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ \"$1 $2 $3\" = '--force-local query local:/1.0/instances?recursion=2&project=cage-tenant' ] || exit 3\nexec cat "+filepath.Join(dir, "inventory.json")+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runtime := IncusRuntime{cfg: IncusConfig{Executable: script, Project: "cage-tenant", Profile: "default", InstanceType: InstanceContainer}}
	usage, err := runtime.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := InstanceUsage{Status: "Frozen", CPU: 2500 * time.Millisecond, Memory: 3 << 20, Swap: 25 << 20}
	if len(usage) != 1 || usage[owned] != want {
		t.Fatalf("unexpected inventory: %+v", usage)
	}
}
