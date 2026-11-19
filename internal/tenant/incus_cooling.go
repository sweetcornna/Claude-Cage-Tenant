package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Inventory reports every gateway-owned instance in the project from a single
// Incus query, keyed by workspace ID. Instances of another type, or whose
// name and ownership label disagree, are left out.
func (r *IncusRuntime) Inventory(ctx context.Context) (map[string]InstanceUsage, error) {
	body, err := captureIncus(r.localCommand(ctx, "query", "local:/1.0/instances?recursion=2&project="+r.cfg.Project), 64<<20)
	if err != nil {
		return nil, err
	}
	var records []struct {
		Name   string            `json:"name"`
		Type   string            `json:"type"`
		Status string            `json:"status"`
		Config map[string]string `json:"config"`
		State  *struct {
			CPU struct {
				Usage int64 `json:"usage"`
			} `json:"cpu"`
			Memory struct {
				Usage     int64 `json:"usage"`
				SwapUsage int64 `json:"swap_usage"`
			} `json:"memory"`
		} `json:"state"`
	}
	if json.Unmarshal(body, &records) != nil {
		return nil, errors.New("invalid Incus instance inventory")
	}
	usage := make(map[string]InstanceUsage, len(records))
	for _, record := range records {
		id := record.Config[instanceLabel]
		if record.Type != r.instanceType() || !vmIDPattern.MatchString(id) || record.Name != "cage-"+id {
			continue
		}
		entry := InstanceUsage{Status: record.Status}
		if record.State != nil {
			entry.CPU = time.Duration(max(record.State.CPU.Usage, 0))
			entry.Memory = max(record.State.Memory.Usage, 0)
			entry.Swap = max(record.State.Memory.SwapUsage, 0)
		}
		usage[id] = entry
	}
	return usage, nil
}

// Freeze suspends every process of a running instance, then asks the kernel
// to move the instance's memory to swap. Reclaim is best effort: a frozen
// instance that keeps its memory still costs no CPU, and its pages are the
// first the kernel swaps out under pressure.
func (r *IncusRuntime) Freeze(ctx context.Context, spec LaunchSpec) error {
	record, err := r.lookup(ctx, spec)
	if err != nil {
		return err
	}
	if record == nil || record.Status != "Running" {
		return errors.New("Incus instance is not running")
	}
	if err := r.command(ctx, "pause", "local:cage-"+spec.VMID).Run(); err != nil {
		return errors.New("Incus instance freeze failed")
	}
	r.reclaim(ctx, spec.VMID)
	return nil
}

// Thaw resumes a frozen instance. A running instance is left as it is.
func (r *IncusRuntime) Thaw(ctx context.Context, spec LaunchSpec) error {
	record, err := r.lookup(ctx, spec)
	if err != nil {
		return err
	}
	switch {
	case record == nil:
		return errors.New("Incus instance is missing")
	case record.Status == "Running":
		return nil
	case record.Status != "Frozen":
		return errors.New("Incus instance is not frozen")
	}
	if err := r.command(ctx, "start", "local:cage-"+spec.VMID).Run(); err != nil {
		return errors.New("Incus instance resume failed")
	}
	return nil
}

// Stop shuts an instance down and keeps its disk. A frozen instance is resumed
// first: it cannot react to a shutdown request while frozen, and Incus may
// report it as running after a failed stop while it is still frozen, which
// makes later incus exec calls hang.
func (r *IncusRuntime) Stop(ctx context.Context, spec LaunchSpec) error {
	record, err := r.lookup(ctx, spec)
	if err != nil {
		return err
	}
	if record == nil || record.Status == "Stopped" {
		return nil
	}
	name := "local:cage-" + spec.VMID
	if record.Status == "Frozen" {
		if err := r.command(ctx, "start", name).Run(); err != nil {
			return errors.New("Incus instance resume before stop failed")
		}
	}
	if r.command(ctx, "stop", name, "--timeout=60").Run() == nil {
		return nil
	}
	if err := r.command(ctx, "stop", name, "--force").Run(); err != nil {
		return errors.New("Incus instance stop failed")
	}
	return nil
}

const reclaimTimeout = 30 * time.Second

var reclaimDenied sync.Once

// reclaim writes the instance's current memory use to its cgroup's
// memory.reclaim. Asking for more than can be reclaimed is expected: the
// kernel answers EAGAIN after moving what it can. The write runs in a child
// process with a hard deadline, so a kernel that keeps retrying never blocks
// a gateway thread. Writing needs root or CAP_DAC_OVERRIDE; without either
// the gateway logs once and relies on the kernel's own reclaim.
func (r *IncusRuntime) reclaim(ctx context.Context, id string) {
	dir, err := r.cgroupDir(ctx, id)
	if err != nil {
		log.Printf("cage-tenant: workspace %s frozen; memory reclaim skipped: %v", id, err)
		return
	}
	before, err := readCgroupInt(dir + "/memory.current")
	if err != nil || before < 16<<20 {
		return
	}
	target := dir + "/memory.reclaim"
	probe, err := os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		reclaimDenied.Do(func() {
			log.Printf("cage-tenant: memory reclaim needs write access to Incus cgroups (run as root or grant CAP_DAC_OVERRIDE); frozen workspaces keep their memory until the kernel needs it")
		})
		return
	}
	probe.Close()
	ctx, cancel := context.WithTimeout(ctx, reclaimTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s\n' "$1" > "$2"`, "cage-tenant-reclaim", strconv.FormatInt(before, 10), target)
	command.WaitDelay = time.Second
	_ = command.Run()
	after, err := readCgroupInt(dir + "/memory.current")
	if err != nil {
		return
	}
	log.Printf("cage-tenant: workspace %s frozen; resident memory %.1f MiB -> %.1f MiB", id, float64(before)/(1<<20), float64(after)/(1<<20))
}

// cgroupDir returns the instance's own cgroup v2 directory. The path comes from
// the instance's process and must be the Incus payload group of this project
// and instance; anything else is refused.
func (r *IncusRuntime) cgroupDir(ctx context.Context, id string) (string, error) {
	body, err := captureIncus(r.localCommand(ctx, "query", "local:/1.0/instances/cage-"+id+"/state?project="+r.cfg.Project), 1<<20)
	if err != nil {
		return "", err
	}
	var state struct {
		Pid int `json:"pid"`
	}
	if json.Unmarshal(body, &state) != nil || state.Pid < 1 {
		return "", errors.New("instance has no process")
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", state.Pid))
	if err != nil {
		return "", errors.New("instance cgroup is unreadable")
	}
	want := "lxc.payload." + r.cfg.Project + "_cage-" + id
	for _, line := range strings.Split(string(data), "\n") {
		group, ok := strings.CutPrefix(line, "0::/")
		if !ok {
			continue
		}
		if first, _, _ := strings.Cut(group, "/"); first == want {
			return "/sys/fs/cgroup/" + want, nil
		}
	}
	return "", errors.New("instance is not in its own Incus cgroup")
}

func readCgroupInt(name string) (int64, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}
