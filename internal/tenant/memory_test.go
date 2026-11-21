package tenant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testMiB = int64(1 << 20)
	testGiB = int64(1 << 30)
)

func runningVM(limit, guestTotal, guestAvailable int64) InstanceMemory {
	return InstanceMemory{Status: "Running", Limit: limit, GuestTotal: guestTotal, GuestAvailable: guestAvailable}
}

// lab1 on 2026-11-20: a 15 GiB host with 3.1 GiB available and swap in use,
// one VM at its 10 GiB ceiling whose guest reports MemTotal 9890 MiB and
// MemAvailable 3783 MiB (6.1 GiB working set, 3.9 GiB cache).
func lab1() (MemoryState, string) {
	id := strings.Repeat("a", 32)
	return MemoryState{HostTotal: 15 * testGiB, HostAvailable: 3174 * testMiB, Ceiling: 10 * testGiB,
		Instances: map[string]InstanceMemory{id: runningVM(10*testGiB, 9890*testMiB, 3783*testMiB)}}, id
}

// shrinkGuest applies a change as a guest that gives back page cache: its
// working set stays and its MemTotal follows the balloon.
func applyChange(state *MemoryState, change memoryChange) {
	entry := state.Instances[change.id]
	delta := change.to - entry.Limit
	entry.Limit, entry.GuestTotal, entry.GuestAvailable = change.to, entry.GuestTotal+delta, entry.GuestAvailable+delta
	state.Instances[change.id] = entry
}

func TestMemoryPlanShrinksLab1HostTowardTheWorkingSet(t *testing.T) {
	// The host keeps what the VM gives back: shrinking stops once the host
	// has twice its 2.25 GiB reserve available again.
	state, id := lab1()
	var limits []int64
	for range 10 {
		changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil)
		if len(changes) == 0 {
			break
		}
		change := changes[0]
		if len(changes) != 1 || change.id != id || change.from-change.to > memoryShrinkStep || change.reason != "host memory is low" {
			t.Fatalf("unexpected plan %+v", changes)
		}
		state.HostAvailable += change.from - change.to
		applyChange(&state, change)
		limits = append(limits, change.to/testMiB)
	}
	if !slices.Equal(limits, []int64{9728, 9216, 8704}) {
		t.Fatalf("lab1 limits %v", limits)
	}

	// Other host processes take what the VM gives back: the VM still stops at
	// its working set plus headroom and never goes below it.
	state, id = lab1()
	limits = nil
	for range 10 {
		changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil)
		if len(changes) == 0 {
			break
		}
		applyChange(&state, changes[0])
		limits = append(limits, changes[0].to/testMiB)
	}
	used := (9890 - 3783) * testMiB
	if !slices.Equal(limits, []int64{9728, 9216, 8704, 8192}) || state.Instances[id].Limit < used+memoryHeadroom {
		t.Fatalf("lab1 under lasting pressure %v", limits)
	}
	// Memory stall alone also counts as pressure.
	state, _ = lab1()
	state.HostAvailable = 10 * testGiB
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 0 {
		t.Fatalf("shrank without host pressure: %+v", changes)
	}
	state.HostStall = 12
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 1 || changes[0].to != 9728*testMiB {
		t.Fatalf("memory stall ignored: %+v", changes)
	}
	// A larger reserve reclaims earlier.
	state.HostStall, state.HostAvailable = 0, 6*testGiB
	if changes := planMemory(state, 4*testGiB, DefaultMemoryMin, nil, nil, nil); len(changes) != 1 {
		t.Fatalf("configured reserve ignored: %+v", changes)
	}
}

func TestMemoryPlanSharesTheHostBetweenRunningVMs(t *testing.T) {
	ids := []string{strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)}
	state := MemoryState{HostTotal: 15 * testGiB, HostAvailable: 8 * testGiB, Ceiling: 10 * testGiB, Instances: map[string]InstanceMemory{
		// A busy guest: 6.1 GiB working set, so it needs 8 GiB.
		ids[0]: runningVM(10*testGiB, 9890*testMiB, 3783*testMiB),
	}}
	// One VM: its share is the ceiling and nothing moves without pressure.
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 0 {
		t.Fatalf("single VM resized without pressure: %+v", changes)
	}
	// A second VM boots at the ceiling with a 1 GiB working set. The share is
	// (15 GiB - 2.25 GiB reserve) / 2, rounded down to 6400 MiB; 512 MiB steps
	// from 10 GiB stop 256 MiB above it, within the hysteresis.
	state.Instances[ids[1]] = runningVM(10*testGiB, 9890*testMiB, 8866*testMiB)
	final := map[string]int64{}
	for range 20 {
		changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil)
		if len(changes) == 0 {
			break
		}
		for _, change := range changes {
			if change.from-change.to > memoryShrinkStep || change.reason != "fair share of the host" {
				t.Fatalf("unexpected change %+v", change)
			}
			applyChange(&state, change)
			final[change.id] = change.to / testMiB
		}
	}
	if final[ids[0]] != 8192 || final[ids[1]] != 6656 {
		t.Fatalf("two VMs settled at %v", final)
	}
	// A third: 13056 MiB / 3 = 4352 MiB each, reached within the hysteresis;
	// the busy guest keeps what it needs.
	state.Instances[ids[2]] = runningVM(10*testGiB, 9890*testMiB, 8866*testMiB)
	for range 20 {
		changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil)
		if len(changes) == 0 {
			break
		}
		for _, change := range changes {
			applyChange(&state, change)
		}
	}
	if state.Instances[ids[0]].Limit != 8192*testMiB || state.Instances[ids[1]].Limit != 4608*testMiB || state.Instances[ids[2]].Limit != 4608*testMiB {
		t.Fatalf("three VMs settled at %+v", state.Instances)
	}
	// When a VM stops, nothing grows back by itself.
	delete(state.Instances, ids[2])
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 0 {
		t.Fatalf("grew without need: %+v", changes)
	}
}

func TestMemoryPlanCountsFrozenAndUnresizableVMs(t *testing.T) {
	running, frozen, busy, silent := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 32)
	state := MemoryState{HostTotal: 32 * testGiB, HostAvailable: 20 * testGiB, Ceiling: 16 * testGiB, Instances: map[string]InstanceMemory{
		running: runningVM(16*testGiB, 15*testGiB, 13*testGiB),
		frozen:  {Status: "Frozen", Limit: 8 * testGiB},
		busy:    runningVM(4*testGiB, 3*testGiB, 2*testGiB),
		silent:  {Status: "Running", Limit: 4 * testGiB},
		"other": {Status: "Stopped", Limit: 16 * testGiB},
	}}
	// Reserve 15% (4915 MiB); 16 GiB is pinned by the frozen, busy and silent
	// VMs, so the one resizable VM may keep 11468 MiB, rounded down to 11264.
	changes := planMemory(state, 0, DefaultMemoryMin, map[string]bool{busy: true}, nil, nil)
	if len(changes) != 1 || changes[0].id != running || changes[0].to != 16*testGiB-memoryShrinkStep {
		t.Fatalf("unexpected plan %+v", changes)
	}
	for range 20 {
		changes = planMemory(state, 0, DefaultMemoryMin, map[string]bool{busy: true}, nil, nil)
		if len(changes) == 0 {
			break
		}
		applyChange(&state, changes[0])
	}
	if state.Instances[running].Limit != 11264*testMiB || state.Instances[busy].Limit != 4*testGiB || state.Instances[silent].Limit != 4*testGiB {
		t.Fatalf("settled at %+v", state.Instances)
	}
}

func TestMemoryPlanGrowsAShortGuestAtOnce(t *testing.T) {
	id := strings.Repeat("a", 32)
	// Under host pressure, with 200 MiB available inside: the guest grows to
	// its working set plus headroom in one step.
	state := MemoryState{HostTotal: 15 * testGiB, HostAvailable: testGiB, HostStall: 30, Ceiling: 10 * testGiB,
		Instances: map[string]InstanceMemory{id: runningVM(4*testGiB, 3900*testMiB, 200*testMiB)}}
	changes := planMemory(state, 0, DefaultMemoryMin, nil, map[string]bool{id: true}, nil)
	// 3700 MiB used + 1 GiB headroom + 196 MiB kernel memory, rounded up.
	if len(changes) != 1 || changes[0].to != 5120*testMiB || changes[0].reason != "guest needs memory" {
		t.Fatalf("short guest plan %+v", changes)
	}
	// A shortfall smaller than the shrink hysteresis still grows.
	state.Instances[id] = runningVM(4*testGiB, 3900*testMiB, 900*testMiB)
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 1 || changes[0].to != 4352*testMiB {
		t.Fatalf("small shortfall plan %+v", changes)
	}
	// Growth stops at the ceiling and at host memory minus the reserve.
	state.Instances[id] = runningVM(12*testGiB, 12*testGiB-100*testMiB, 0)
	state.Ceiling = 64 * testGiB
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 1 || changes[0].to != 15*testGiB-2304*testMiB {
		t.Fatalf("host growth cap %+v", changes)
	}
	state.Ceiling = 10 * testGiB
	state.Instances[id] = runningVM(9*testGiB, 9*testGiB-100*testMiB, 0)
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 1 || changes[0].to != 10*testGiB {
		t.Fatalf("ceiling growth cap %+v", changes)
	}
	// A hotplugged VM may still grow, but never shrinks.
	state.Ceiling, state.HostStall = 10*testGiB, 30
	state.Instances[id] = InstanceMemory{Status: "Running", Limit: 10 * testGiB, GuestTotal: 9890 * testMiB, GuestAvailable: 9 * testGiB, Hotplugged: true}
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 0 {
		t.Fatalf("hotplugged VM shrank: %+v", changes)
	}
}

func TestMemoryPlanHysteresisAndBounds(t *testing.T) {
	id := strings.Repeat("a", 32)
	state := MemoryState{HostTotal: 15 * testGiB, HostAvailable: testGiB, Ceiling: 10 * testGiB,
		Instances: map[string]InstanceMemory{id: runningVM(8448*testMiB, 8098*testMiB, 1991*testMiB)}}
	// The lab1 working set needs 8192 MiB: 256 MiB above it is below the
	// 512 MiB hysteresis.
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 0 {
		t.Fatalf("change below hysteresis: %+v", changes)
	}
	// An idle guest never goes below -memory-min.
	state.Instances[id] = runningVM(3*testGiB, 2900*testMiB, 2800*testMiB)
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 1 || changes[0].to != 2560*testMiB {
		t.Fatalf("idle guest plan %+v", changes)
	}
	state.Instances[id] = runningVM(2560*testMiB, 2460*testMiB, 2400*testMiB)
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 1 || changes[0].to != 2048*testMiB {
		t.Fatalf("idle guest second step %+v", changes)
	}
	state.Instances[id] = runningVM(2048*testMiB, 1948*testMiB, 1900*testMiB)
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 0 {
		t.Fatalf("shrank below the minimum: %+v", changes)
	}
	// A host smaller than the reserve plus the ceiling caps VMs below it.
	small := MemoryState{HostTotal: 6 * testGiB, HostAvailable: 5 * testGiB, Ceiling: 8 * testGiB,
		Instances: map[string]InstanceMemory{id: runningVM(8*testGiB, 7900*testMiB, 7*testGiB)}}
	if changes := planMemory(small, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 1 || changes[0].to != 7680*testMiB {
		t.Fatalf("small host plan %+v", changes)
	}
	// A profile below the gateway minimum is never resized.
	tiny := MemoryState{HostTotal: 6 * testGiB, HostAvailable: testGiB, Ceiling: 900 * testMiB,
		Instances: map[string]InstanceMemory{id: runningVM(900*testMiB, 800*testMiB, 700*testMiB)}}
	if changes := planMemory(tiny, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 0 {
		t.Fatalf("tiny profile plan %+v", changes)
	}
}

// memoryRuntime adds elastic memory to the fake runtime.
type memoryRuntime struct {
	*fakeRuntime
	mu      sync.Mutex
	state   MemoryState
	set     []memoryChange
	failSet error
	elastic bool
}

func (m *memoryRuntime) ElasticMemory() bool { return m.elastic }

func (m *memoryRuntime) MemoryState(context.Context) (MemoryState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.state
	state.Instances = make(map[string]InstanceMemory)
	for id, entry := range m.state.Instances {
		state.Instances[id] = entry
	}
	return state, nil
}

func (m *memoryRuntime) SetMemory(_ context.Context, id string, size int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.set = append(m.set, memoryChange{id: id, from: m.state.Instances[id].Limit, to: size})
	if m.failSet != nil {
		return m.failSet
	}
	applyChange(&m.state, memoryChange{id: id, to: size})
	return nil
}

func (m *memoryRuntime) changes() []memoryChange {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.set)
}

func TestTenantElasticMemoryResizesWithoutFaultingWorkspaces(t *testing.T) {
	base := newFixture(t, false)
	base.server.Close()
	runtime := &memoryRuntime{fakeRuntime: base.runtime, elastic: true}
	base.config.Runtime, base.config.MemoryInterval = runtime, 10*time.Minute
	base.openServer()
	login, _ := base.login("register", "alice")
	record := base.create(login, "first")
	base.connect(login, record.ID)
	foreign := strings.Repeat("f", 32)
	state, _ := lab1()
	runtime.state = MemoryState{HostTotal: state.HostTotal, HostAvailable: state.HostAvailable, Ceiling: state.Ceiling, Instances: map[string]InstanceMemory{
		record.ID: runningVM(10*testGiB, 9890*testMiB, 3783*testMiB),
		// An owned-looking instance without a portal record is never resized.
		foreign: runningVM(10*testGiB, 9890*testMiB, 9000*testMiB),
	}}

	runtime.failSet = errors.New("Failed setting memory to 9728MiB (currently 10000MiB) as it was taking too long")
	base.server.balanceOnce()
	base.server.balanceOnce()
	if changes := runtime.changes(); len(changes) != 1 || changes[0].id != record.ID || changes[0].to != 9728*testMiB {
		t.Fatalf("failed shrink was not paused: %+v", changes)
	}
	base.state(login, record.ID, ready)

	runtime.mu.Lock()
	runtime.failSet = nil
	runtime.mu.Unlock()
	base.server.mu.Lock()
	for id := range base.server.memory.pause {
		base.server.memory.pause[id] = time.Time{}
	}
	base.server.mu.Unlock()
	base.server.balanceOnce()
	if changes := runtime.changes(); len(changes) != 2 || changes[1].to != 9728*testMiB {
		t.Fatalf("shrink not retried after its pause: %+v", changes)
	}
	// A workspace being prepared keeps its size.
	base.server.mu.Lock()
	base.server.starting[record.ID] = true
	base.server.mu.Unlock()
	base.server.balanceOnce()
	base.server.mu.Lock()
	delete(base.server.starting, record.ID)
	base.server.mu.Unlock()
	if changes := runtime.changes(); len(changes) != 2 {
		t.Fatalf("resized a workspace during preparation: %+v", changes)
	}
	base.server.balanceOnce()
	if changes := runtime.changes(); len(changes) != 3 || changes[2].to != 9216*testMiB || runtime.state.Instances[foreign].Limit != 10*testGiB {
		t.Fatalf("unexpected changes %+v", changes)
	}
	base.state(login, record.ID, ready)
}

func TestTenantElasticMemoryConfiguration(t *testing.T) {
	fixture := newFixture(t, false)
	for _, change := range []func(*Config){
		func(c *Config) { c.MemoryInterval = time.Second },
		func(c *Config) { c.MemoryInterval = time.Hour },
		func(c *Config) { c.MemoryInterval = -time.Minute },
		func(c *Config) { c.MemoryReserve = -1 },
		func(c *Config) { c.MemoryMin = 512 << 20 },
	} {
		config := fixture.config
		config.StateDir = filepath.Join(t.TempDir(), "state")
		change(&config)
		if server, err := NewServer(config); err == nil {
			server.Close()
			t.Fatal("invalid elastic memory configuration accepted")
		}
	}
	for _, elastic := range []bool{false, true} {
		config := fixture.config
		config.StateDir = filepath.Join(t.TempDir(), "state")
		config.Runtime, config.MemoryInterval = &memoryRuntime{fakeRuntime: fixture.runtime, elastic: elastic}, 15*time.Second
		server, err := NewServer(config)
		if err != nil {
			t.Fatal(err)
		}
		if (server.memory.runtime != nil) != elastic {
			t.Fatalf("elastic memory enabled=%v for a runtime that reports %v", server.memory.runtime != nil, elastic)
		}
		server.Close()
	}
}

func TestTenantIdleCoolingWaitsForAResize(t *testing.T) {
	fixture := newCoolingFixture(t)
	login, _ := fixture.login("register", "alice")
	record := fixture.create(login, "resizing")
	fixture.connect(login, record.ID)
	fixture.server.mu.Lock()
	fixture.server.memory.changes = map[string]bool{record.ID: true}
	fixture.server.mu.Unlock()
	fixture.advance(11 * time.Minute)
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 0 {
		t.Fatal("workspace frozen during a memory change")
	}
	fixture.server.mu.Lock()
	delete(fixture.server.memory.changes, record.ID)
	fixture.server.mu.Unlock()
	fixture.server.coolOnce()
	if fixture.cooler.count("freeze", record.ID) != 1 {
		t.Fatal("workspace not frozen after the memory change")
	}
}

func writeTestFile(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestMemoryPlanReturnsMemoryToASilentGuest(t *testing.T) {
	id, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	state := MemoryState{HostTotal: 15 * testGiB, HostAvailable: 8 * testGiB, Ceiling: 10 * testGiB, Instances: map[string]InstanceMemory{
		id:    {Status: "Running", Limit: 6 * testGiB, GuestSilent: true},
		other: runningVM(4*testGiB, 3900*testMiB, 2*testGiB),
	}}
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, nil); len(changes) != 0 {
		t.Fatalf("one missed probe resized: %+v", changes)
	}
	silent := map[string]bool{id: true}
	changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, silent)
	if len(changes) != 1 || changes[0].id != id || changes[0].to != 10*testGiB || changes[0].reason != "guest did not answer" {
		t.Fatalf("silent guest plan %+v", changes)
	}
	// One step per sweep while the host is short.
	state.HostAvailable = testGiB
	changes = planMemory(state, 0, DefaultMemoryMin, nil, nil, silent)
	if len(changes) != 2 || changes[0].id != id || changes[0].to != 6656*testMiB {
		t.Fatalf("silent guest under pressure %+v", changes)
	}
	// A VM being prepared or cooled, or already at its ceiling, is left alone.
	if changes := planMemory(state, 0, DefaultMemoryMin, map[string]bool{id: true}, nil, silent); len(changes) != 1 || changes[0].id != other {
		t.Fatalf("busy silent guest resized: %+v", changes)
	}
	state.Instances[id] = InstanceMemory{Status: "Running", Limit: 10 * testGiB, GuestSilent: true}
	if changes := planMemory(state, 0, DefaultMemoryMin, nil, nil, silent); len(changes) != 1 || changes[0].id != other {
		t.Fatalf("silent guest at its ceiling resized: %+v", changes)
	}
}

func TestTenantElasticMemoryRestoresAGuestThatStopsAnswering(t *testing.T) {
	base := newFixture(t, false)
	base.server.Close()
	runtime := &memoryRuntime{fakeRuntime: base.runtime, elastic: true}
	base.config.Runtime, base.config.MemoryInterval = runtime, 10*time.Minute
	base.openServer()
	login, _ := base.login("register", "alice")
	record := base.create(login, "quiet")
	base.connect(login, record.ID)
	runtime.state = MemoryState{HostTotal: 15 * testGiB, HostAvailable: 8 * testGiB, Ceiling: 10 * testGiB, Instances: map[string]InstanceMemory{
		record.ID: {Status: "Running", Limit: 6 * testGiB, GuestSilent: true},
	}}
	base.server.balanceOnce()
	if changes := runtime.changes(); len(changes) != 0 {
		t.Fatalf("resized after one missed probe: %+v", changes)
	}
	// It answers again: the count starts over.
	runtime.mu.Lock()
	runtime.state.Instances[record.ID] = runningVM(6*testGiB, 5900*testMiB, 3*testGiB)
	runtime.mu.Unlock()
	base.server.balanceOnce()
	runtime.mu.Lock()
	runtime.state.Instances[record.ID] = InstanceMemory{Status: "Running", Limit: 6 * testGiB, GuestSilent: true}
	runtime.mu.Unlock()
	base.server.balanceOnce()
	if changes := runtime.changes(); len(changes) != 0 {
		t.Fatalf("missed probes counted across an answer: %+v", changes)
	}
	base.server.balanceOnce()
	if changes := runtime.changes(); len(changes) != 1 || changes[0].to != 10*testGiB {
		t.Fatalf("silent guest not restored: %+v", changes)
	}
	base.state(login, record.ID, ready)
}
