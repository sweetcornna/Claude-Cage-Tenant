package tenant

import (
	"context"
	"log"
	"time"
)

// Idle cooling follows the lifecycle of Dormice (github.com/BitMiracle-AI/Dormice):
// an idle workspace moves one step colder per sweep and any use brings it back.
//
//	running ── idle ──▶ frozen ── idle ──▶ stopped
//	full RAM            processes kept,     disk only,
//	                    memory in swap,     cold boot
//	                    resumes in ~30 ms
//
// Both thresholds count on one idle clock. Browser traffic through the gateway
// (requests and stream bytes in either direction), a portal open and guest CPU
// above the busy share all reset it, so an agent that keeps working without a
// browser attached is never frozen under it.

// Cooler is implemented by runtimes that can suspend and stop idle workspaces.
type Cooler interface {
	// Inventory reports every gateway-owned instance, keyed by workspace ID.
	Inventory(context.Context) (map[string]InstanceUsage, error)
	Freeze(context.Context, LaunchSpec) error
	Thaw(context.Context, LaunchSpec) error
	Stop(context.Context, LaunchSpec) error
}

// InstanceUsage is one instance as the runtime sees it.
type InstanceUsage struct {
	// Status is the Incus status: Running, Frozen, Stopped and so on.
	Status string
	// CPU is the cumulative CPU time the instance has used.
	CPU    time.Duration
	Memory int64
	Swap   int64
}

type cpuSample struct {
	at   time.Time
	used time.Duration
}

// cooling is the idle-cooling state of a server. Guarded by the server lock.
type cooling struct {
	runtime Cooler // nil when idle cooling is off
	frozen  map[string]bool
	// changes holds a freeze, thaw or stop in progress; the channel closes
	// when it ends.
	changes  map[string]chan struct{}
	activity map[string]time.Time
	cpu      map[string]cpuSample
}

type coolingMove struct {
	id   string
	spec LaunchSpec
	stop bool
	idle time.Duration
	done chan struct{}
}

type coolingCandidate struct {
	id   string
	stop bool
}

func (s *Server) startCooling() {
	runtime, ok := s.cfg.Runtime.(Cooler)
	if s.cfg.FreezeAfter == 0 || !ok {
		return
	}
	s.cooling = cooling{
		runtime: runtime, frozen: make(map[string]bool), changes: make(map[string]chan struct{}),
		activity: make(map[string]time.Time), cpu: make(map[string]cpuSample),
	}
	interval := min(time.Minute, max(s.cfg.FreezeAfter/4, 5*time.Second))
	s.workers.Go(func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.context.Done():
				return
			case <-ticker.C:
				s.coolOnce()
			}
		}
	})
}

// coolOnce is one sweep: it reads every instance once and moves each idle
// workspace at most one step colder.
func (s *Server) coolOnce() {
	ctx, cancel := context.WithTimeout(s.context, time.Minute)
	usage, err := s.cooling.runtime.Inventory(ctx)
	cancel()
	if err != nil {
		if s.context.Err() == nil {
			log.Printf("cage-tenant: idle cooling could not list instances: %v", err)
		}
		return
	}
	now := s.now()
	var candidates []coolingCandidate
	s.mu.Lock()
	for id := range s.store.VMs {
		instance, found := usage[id]
		if !found {
			delete(s.cooling.cpu, id)
			continue
		}
		busy := s.starting[id] || s.cooling.changes[id] != nil
		last := s.lastActiveLocked(id, now)
		switch instance.Status {
		case "Running":
			if sample, ok := s.cooling.cpu[id]; ok && s.cfg.BusyCPU > 0 && instance.CPU >= sample.used && now.After(sample.at) &&
				float64(instance.CPU-sample.used) >= s.cfg.BusyCPU*float64(now.Sub(sample.at)) {
				s.cooling.activity[id], last = now, now
			}
			s.cooling.cpu[id] = cpuSample{at: now, used: instance.CPU}
			if busy {
				continue
			}
			delete(s.cooling.frozen, id)
			if now.Sub(last) >= s.cfg.FreezeAfter {
				candidates = append(candidates, coolingCandidate{id: id})
			}
		case "Frozen":
			delete(s.cooling.cpu, id)
			if busy {
				continue
			}
			s.cooling.frozen[id] = true
			if s.cfg.StopAfter > 0 && now.Sub(last) >= s.cfg.StopAfter {
				candidates = append(candidates, coolingCandidate{id: id, stop: true})
			}
		default:
			delete(s.cooling.cpu, id)
			if !busy {
				delete(s.cooling.frozen, id)
			}
		}
	}
	s.mu.Unlock()
	// Moves run one at a time, each claimed just before it runs, so a request
	// to one workspace never waits behind another workspace's slow stop.
	for _, candidate := range candidates {
		if s.context.Err() != nil {
			return
		}
		s.mu.Lock()
		move, ok := s.claimCoolingLocked(candidate, s.now())
		s.mu.Unlock()
		if ok {
			s.runCooling(move)
		}
	}
}

// claimCoolingLocked checks a candidate again with fresh activity and, if it
// is still due, reserves it. Caller holds s.mu.
func (s *Server) claimCoolingLocked(candidate coolingCandidate, now time.Time) (coolingMove, bool) {
	record, exists := s.store.VMs[candidate.id]
	if !exists || s.starting[candidate.id] || s.cooling.changes[candidate.id] != nil || s.memory.changes[candidate.id] || s.cooling.frozen[candidate.id] != candidate.stop {
		return coolingMove{}, false
	}
	idle, limit := now.Sub(s.lastActiveLocked(candidate.id, now)), s.cfg.FreezeAfter
	if candidate.stop {
		limit = s.cfg.StopAfter
	}
	if idle < limit {
		return coolingMove{}, false
	}
	return s.beginCoolingLocked(record, candidate.stop, idle), true
}

// lastActiveLocked returns the end of the workspace's last activity. A
// workspace seen for the first time, for example after a gateway restart,
// starts its idle clock now. Caller holds s.mu.
func (s *Server) lastActiveLocked(id string, now time.Time) time.Time {
	last := s.cooling.activity[id]
	if workspace := s.workspaces[id]; workspace != nil {
		if io := workspace.lastActive(); io.After(last) {
			last = io
		}
	}
	if last.IsZero() {
		last = now
		s.cooling.activity[id] = now
	}
	return last
}

// beginCoolingLocked reserves the workspace for a freeze or stop. Requests that
// arrive meanwhile wait for it and then wake the workspace. Live streams end
// now, before the guest stops answering. Caller holds s.mu.
func (s *Server) beginCoolingLocked(record vmRecord, stop bool, idle time.Duration) coolingMove {
	move := coolingMove{id: record.ID, spec: LaunchSpec{VMID: record.ID, SystemID: record.SystemID, Image: record.Image}, stop: stop, idle: idle, done: make(chan struct{})}
	s.cooling.changes[record.ID] = move.done
	if workspace := s.workspaces[record.ID]; workspace != nil {
		if stop {
			delete(s.workspaces, record.ID)
			s.deleteWorkspaceTickets(record.ID)
			workspace.close()
		} else {
			workspace.sleep()
		}
	}
	return move
}

func (s *Server) runCooling(move coolingMove) {
	ctx, cancel := context.WithTimeout(s.context, 3*time.Minute)
	var err error
	if move.stop {
		err = s.cooling.runtime.Stop(ctx, move.spec)
	} else {
		err = s.cooling.runtime.Freeze(ctx, move.spec)
	}
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cooling.changes, move.id)
	close(move.done)
	verb := "freeze"
	if move.stop {
		verb = "stop"
	}
	if err != nil {
		if s.context.Err() == nil {
			log.Printf("cage-tenant: workspace %s idle %s failed: %v", move.id, verb, err)
		}
		// The instance may be half way. Marking it frozen makes the next
		// request check the real state before it is used.
		if !move.stop {
			s.cooling.frozen[move.id] = true
		}
		return
	}
	delete(s.cooling.cpu, move.id)
	if move.stop {
		delete(s.cooling.frozen, move.id)
		delete(s.cooling.activity, move.id)
	} else {
		s.cooling.frozen[move.id] = true
	}
	log.Printf("cage-tenant: workspace %s idle for %s; %s done", move.id, move.idle.Round(time.Second), verb)
}

// coolingLocked reports whether a request must wake the workspace before it
// reaches the guest. Caller holds s.mu.
func (s *Server) coolingLocked(id string) bool {
	return s.cooling.frozen[id] || s.cooling.changes[id] != nil
}

// noteActivityLocked resets the idle clock. Caller holds s.mu.
func (s *Server) noteActivityLocked(id string, now time.Time) {
	if s.cooling.runtime != nil {
		s.cooling.activity[id] = now
	}
}

// wake waits for a freeze or stop in progress and resumes a frozen workspace.
// Concurrent callers share one resume. A workspace that was stopped stays
// stopped: opening it from the portal boots it again.
func (s *Server) wake(ctx context.Context, id string) error {
	for {
		s.mu.Lock()
		if s.cooling.runtime == nil {
			s.mu.Unlock()
			return nil
		}
		if done := s.cooling.changes[id]; done != nil {
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		record, exists := s.store.VMs[id]
		if !exists || !s.cooling.frozen[id] {
			s.mu.Unlock()
			return nil
		}
		done := make(chan struct{})
		s.cooling.changes[id] = done
		s.mu.Unlock()
		// The resume belongs to the server, not to the request that asked for it.
		thaw, cancel := context.WithTimeout(s.context, 30*time.Second)
		err := s.cooling.runtime.Thaw(thaw, LaunchSpec{VMID: id, SystemID: record.SystemID, Image: record.Image})
		cancel()
		s.mu.Lock()
		delete(s.cooling.changes, id)
		close(done)
		if err == nil {
			delete(s.cooling.frozen, id)
			s.cooling.activity[id] = s.now()
		}
		s.mu.Unlock()
		if err != nil && s.context.Err() == nil {
			log.Printf("cage-tenant: workspace %s resume failed: %v", id, err)
		}
		return err
	}
}

// awakeLocked records that a prepared workspace is running. Caller holds s.mu.
func (s *Server) awakeLocked(id string, now time.Time) {
	if s.cooling.runtime != nil {
		delete(s.cooling.frozen, id)
		s.cooling.activity[id] = now
	}
}
