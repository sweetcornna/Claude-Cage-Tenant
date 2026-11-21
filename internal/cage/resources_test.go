package cage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/host"
)

func TestResourceLimitsUseExistingHostShare(t *testing.T) {
	oldMem, oldCPU := host.MemGiB, host.CPUs
	t.Cleanup(func() { host.MemGiB, host.CPUs = oldMem, oldCPU })
	for _, tc := range []struct{ memory, cpu, wantMemory, wantCPU int }{{24, 10, 12, 5}, {8, 4, 4, 2}, {4, 2, 2, 1}, {0, 0, 4, 2}} {
		host.MemGiB = func() int { return tc.memory }
		host.CPUs = func() int { return tc.cpu }
		t.Setenv("AGENT_VM_HOST_SHARE", "2")
		memory, cpus := ResourceLimits()
		if memory != tc.wantMemory || cpus != tc.wantCPU {
			t.Fatal(tc, memory, cpus)
		}
	}
}

func TestResourceFailuresKeepDiagnosticCausePrivate(t *testing.T) {
	profile := t.TempDir()
	previous := diagnostics.Default()
	diagnostics.SetDefault(diagnostics.New(profile))
	t.Cleanup(func() { diagnostics.SetDefault(previous) })
	dir := t.TempDir()
	var attempts int
	stop := StartResourceManager(context.Background(), dir, 4<<30, 2, func(context.Context, string) ([]byte, error) {
		attempts++
		if attempts <= 2 {
			return nil, errors.New("resource probe execution failed: exit status 255; stderr=getsockname failed: Not a socket; token=private-probe-token")
		}
		return []byte("invalid metrics"), nil
	}, func(context.Context, uint64) error { t.Error("failed sample changed memory target"); return nil })
	defer stop()
	deadline := time.Now().Add(9 * time.Second)
	var log []byte
	for time.Now().Before(deadline) {
		log, _ = os.ReadFile(filepath.Join(profile, "logs", "events.jsonl"))
		if strings.Contains(string(log), "incomplete guest resource observation") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(string(log), "Not a socket") || !strings.Contains(string(log), "incomplete guest resource observation") {
		t.Fatalf("failure causes missing from diagnostics: %s", log)
	}
	if strings.Contains(string(log), "private-probe-token") || strings.Count(string(log), "Not a socket") != 1 {
		t.Fatalf("secret leaked or repeated failure flooded log: %s", log)
	}
	status, err := ReadResourceStatus(dir)
	if err != nil || !strings.Contains(status.Error, "自动回收内存已暂停") || strings.Contains(status.Error, "Not a socket") || strings.Contains(status.Error, "private-probe-token") {
		t.Fatalf("technical detail escaped to resource status: %+v %v", status, err)
	}
}

func TestMemoryPolicyBurstIdleAndHostPressure(t *testing.T) {
	now := time.Now()
	gib := uint64(1 << 30)
	p := memoryPolicy{maximum: 12 * gib, target: 4 * gib}
	busy := ResourceSample{Total: 4*gib - 128<<20, Available: 128 << 20}
	if got := p.next(now, busy, false, false); got != 12*gib {
		t.Fatalf("burst gets %d", got)
	}
	// A short host still lets a guest on the edge of OOM out of its pressure,
	// but only to its use plus reserve, not to the maximum.
	if got := p.next(now, busy, true, false); got != 5*gib {
		t.Fatalf("host pressure grows to %d", got)
	}
	if got := p.next(now, ResourceSample{Total: 4*gib - 128<<20, Available: 1200 << 20}, true, false); got != 4*gib {
		t.Fatalf("host pressure grows a guest without pressure to %d", got)
	}
	p.target = p.maximum
	idle := ResourceSample{Total: 12*gib - 128<<20, Available: 10 * gib}
	if got := p.next(now, idle, false, false); got != p.maximum {
		t.Fatal("immediate shrink", got)
	}
	if got := p.next(now.Add(29*time.Second), idle, false, false); got != p.maximum {
		t.Fatal("premature shrink", got)
	}
	next := p.next(now.Add(31*time.Second), idle, false, false)
	if next >= p.maximum-256<<20 || next < 2*gib {
		t.Fatalf("reclamation step %d", next)
	}
	p.target = next
	if got := p.next(now.Add(33*time.Second), idle, false, false); got != next {
		t.Fatal("repeated shrink before guest acknowledgement", got)
	}
	idle.Total -= p.maximum - next
	idle.Available -= p.maximum - next
	idle.Balloon = p.maximum - next
	if got := p.next(now.Add(35*time.Second), idle, false, false); got >= next {
		t.Fatal("no reclamation after acknowledgement", got)
	}
	// Repeated active workloads must reset the idle interval.
	p.next(now.Add(36*time.Second), busy, false, false)
	if got := p.next(now.Add(37*time.Second), idle, false, false); got != p.target {
		t.Fatal("shrunk after burst", got)
	}
}

func TestResourcesRejectIncompleteOrImpossibleObservation(t *testing.T) {
	valid := "MemTotal: 4000000\nMemAvailable: 2000000\nSwapTotal: 1000\nSwapFree: 500\noom_kill 3\nsome avg10=0.50 avg60=0.0 total=2\ncpu 10 0 5 20 2 0 0 0 0 0\nboot 12345678-1234-1234-1234-123456789012\n"
	s, err := parseResources([]byte(valid))
	if err != nil || s.Available != 2000000<<10 || s.OOMKills != 3 || s.Stall != .5 {
		t.Fatal(s, err)
	}
	balloon, err := parseResources([]byte(valid + "balloon_inflate 131072\nballoon_deflate 65536\npage_size 4096\n"))
	if err != nil || balloon.Balloon != 256<<20 {
		t.Fatal("balloon acknowledgement lost", balloon, err)
	}
	for _, invalid := range []string{strings.Replace(valid, "MemAvailable: 2000000", "MemAvailable: 5000000", 1), strings.Replace(valid, "MemAvailable: 2000000\n", "", 1), strings.Replace(valid, "oom_kill 3\n", "", 1), strings.Replace(valid, "SwapFree: 500", "SwapFree: 1001", 1)} {
		if _, err := parseResources([]byte(invalid)); err == nil {
			t.Fatal("accepted invalid sample")
		}
	}
}

func TestResourceManagerCancellationAndFreshness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop := StartResourceManager(ctx, t.TempDir(), 4<<30, 2, func(context.Context, string) ([]byte, error) {
		t.Error("read after cancellation")
		return nil, fmt.Errorf("unused")
	}, func(context.Context, uint64) error { t.Error("change after cancellation"); return nil })
	done := make(chan struct{})
	go func() { stop(); stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not join")
	}
	dir := t.TempDir()
	for _, age := range []time.Duration{0, 11 * time.Second} {
		if err := writeResourceStatus(dir, ResourceStatus{Version: 1, UpdatedAt: time.Now().Add(-age)}); err != nil {
			t.Fatal(err)
		}
		_, err := ReadResourceStatus(dir)
		if (err != nil) != (age > 0) {
			t.Fatal(age, err)
		}
	}
}

func TestAutomaticAllocationIsCappedButManualKeepsHostShare(t *testing.T) {
	oldMem, oldCPU := host.MemGiB, host.CPUs
	t.Cleanup(func() { host.MemGiB, host.CPUs = oldMem, oldCPU })
	t.Setenv("AGENT_VM_HOST_SHARE", "2")
	host.CPUs = func() int { return 10 }
	// A 10 GiB tenant VM reports 9 GiB: the nested guest keeps its 4 GiB share.
	for _, tc := range []struct{ host, automatic, share int }{{128, 32, 64}, {64, 24, 32}, {32, 12, 16}, {24, 9, 12}, {16, 8, 8}, {9, 4, 4}, {8, 4, 4}, {4, 2, 2}} {
		host.MemGiB = func() int { return tc.host }
		memory, _, err := ResourceAllocation{Automatic: true}.Limits()
		if err != nil || memory != tc.automatic {
			t.Fatalf("automatic on %d GiB host: %d %v", tc.host, memory, err)
		}
		if share, _ := ResourceLimits(); share != tc.share {
			t.Fatalf("share on %d GiB host: %d", tc.host, share)
		}
		memory, _, err = ResourceAllocation{MemoryGiB: tc.share, CPUs: 2}.Limits()
		if err != nil || memory != tc.share {
			t.Fatalf("manual allocation up to the host share rejected: %d %v", memory, err)
		}
	}
}

func TestMemoryPolicyGrowsGraduallyAndReclaimsQuickly(t *testing.T) {
	now := time.Now()
	gib := uint64(1 << 30)
	maximum := 32 * gib
	p := memoryPolicy{maximum: maximum, target: 3 * gib}
	// A short stall is not an out-of-memory emergency: it must not buy the whole budget.
	brief := ResourceSample{Total: 3*gib - 128<<20, Available: 600 << 20, Stall: 1.5}
	if got := p.next(now, brief, false, false); got <= p.target || got >= maximum/2 {
		t.Fatalf("brief stall changed the target to %d", got)
	}
	// Sustained pressure still reaches the maximum within a few probes.
	probes := 0
	for p.target < maximum && probes < 20 {
		p.target = p.next(now, ResourceSample{Total: p.target - 128<<20, Available: 600 << 20, Stall: 1.5}, false, false)
		probes++
	}
	if p.target != maximum || probes > 8 {
		t.Fatalf("sustained pressure reached %d after %d probes", p.target, probes)
	}
	// An idle guest that booted at the maximum returns its memory within about a minute
	// (30 s idle window plus 2 s probes), assuming the guest acknowledges each step.
	balloon, elapsed := uint64(0), 0
	for ; elapsed <= 120 && p.target > 3*gib; elapsed += 2 {
		total := maximum - balloon
		target := p.next(now.Add(time.Duration(elapsed)*time.Second), ResourceSample{Total: total, Available: total - gib, Balloon: balloon}, false, false)
		p.target, balloon = target, maximum-target
	}
	if p.target > 3*gib || elapsed > 90 {
		t.Fatalf("idle reclamation left %d after %d seconds", p.target, elapsed)
	}
}

func TestMemoryPolicyHostPressureAndQuietGuestReclaimSooner(t *testing.T) {
	now := time.Now()
	gib := uint64(1 << 30)
	maximum := 12 * gib
	// 2 GiB in use: the policy keeps 2 GiB + 1 GiB reserve + kernel memory.
	desired := uint64(3328 << 20)
	sample := func(balloon uint64) ResourceSample {
		total := maximum - balloon
		return ResourceSample{Total: total, Available: total - 2*gib, Balloon: balloon}
	}
	steps := map[string]int{}
	for _, tc := range []struct {
		name                string
		hostPressure, quiet bool
		wait                time.Duration
	}{
		{"short host", true, false, 0},
		{"quiet guest", false, true, 10 * time.Second},
		{"busy guest on a roomy host", false, false, 30 * time.Second},
	} {
		p := memoryPolicy{maximum: maximum, target: maximum}
		if tc.wait > 0 {
			if got := p.next(now, sample(0), tc.hostPressure, tc.quiet); got != maximum {
				t.Fatalf("%s: shrank at once: %d", tc.name, got)
			}
			if got := p.next(now.Add(tc.wait-time.Second), sample(0), tc.hostPressure, tc.quiet); got != maximum {
				t.Fatalf("%s: shrank before %s of headroom: %d", tc.name, tc.wait, got)
			}
		}
		at := now.Add(tc.wait)
		first := p.next(at, sample(0), tc.hostPressure, tc.quiet)
		if first >= maximum {
			t.Fatalf("%s: no reclamation after %s", tc.name, tc.wait)
		}
		// Without the guest's acknowledgement nothing more is asked.
		p.target = first
		if again := p.next(at.Add(2*time.Second), sample(0), tc.hostPressure, tc.quiet); again != first {
			t.Fatalf("%s: second reclamation before acknowledgement: %d", tc.name, again)
		}
		for count := 1; count < 30; count++ {
			at = at.Add(2 * time.Second)
			target := p.next(at, sample(maximum-p.target), tc.hostPressure, tc.quiet)
			if target == p.target {
				steps[tc.name] = count
				break
			}
			p.target = target
		}
		if p.target < desired || p.target >= desired+512<<20 {
			t.Fatalf("%s: settled at %d", tc.name, p.target)
		}
	}
	if steps["short host"] >= steps["busy guest on a roomy host"] || steps["quiet guest"] >= steps["busy guest on a roomy host"] || steps["short host"] > 6 {
		t.Fatalf("reclamation steps %v", steps)
	}
}
