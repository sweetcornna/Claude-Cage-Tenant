package workbench

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

type networkRecovery struct {
	RunID, Configuration string
	PauseVersion         uint64
	Epoch                uint64
	Next                 time.Time
	Attempts             int
	pressureLogged       bool
	// pressureSince is when memory pressure first postponed this attempt
	// after it was due. The attempt runs anyway recoveryPressureWait later.
	pressureSince time.Time
}

// recoveryPressureWait bounds how long memory pressure postpones a due
// automatic recheck. A guest that stays short of memory still gets its full
// check (the same one a user runs by hand), and a failed attempt backs off
// before pressure can postpone the next one again.
var recoveryPressureWait = 60 * time.Second

// networkRecoveryCheck is the full check an automatic recovery runs; tests
// replace it so no guest is needed.
var networkRecoveryCheck = func(s *Server) error { return s.runSafetyCheck() }

// pendingRecoveryLocked reports whether s.recovery still describes the next
// transition of the current gate. Called with s.mu held.
func (s *Server) pendingRecoveryLocked() (networkRecovery, bool) {
	r := s.recovery
	if r.RunID == "" || s.networkPaused || s.closing || s.safety.pauseVersion != r.PauseVersion || s.safety.Gate == nil || s.safety.Gate.RunID != r.RunID || s.safety.Gate.Configuration != r.Configuration || s.safety.Gate.Mode == cage.GateOpen {
		return r, false
	}
	if s.safety.Gate.Mode == cage.GateClosed && s.safety.Gate.Epoch != r.Epoch {
		return r, false // A newer closure must not promise this recovery will run.
	}
	job := s.jobs.snapshot()
	if s.safety.Gate.Mode != cage.GateClosed && (!job.Running || job.Kind != "network-recovery") {
		return r, false // A manual check owns the next transition.
	}
	return r, true
}

// Called with s.mu held. Reuse the ordinary network status text on every
// platform; no independent timer or cached permission is introduced.
func (s *Server) networkRecoveryMessageLocked(now time.Time) string {
	r, pending := s.pendingRecoveryLocked()
	if !pending {
		return ""
	}
	job := s.jobs.snapshot()
	if job.Running {
		if job.Kind == "network-recovery" {
			return "正在执行自动完整重检，通过后恢复联网"
		}
		return "自动重检正在等待当前操作完成"
	}
	if !s.ready {
		return "自动重检正在等待环境就绪"
	}
	if now.Before(r.Next) {
		seconds := (r.Next.Sub(now) + time.Second - 1) / time.Second
		return fmt.Sprintf("自动重检将在 %d 秒后尝试", seconds)
	}
	if !r.pressureSince.IsZero() {
		if left := r.pressureSince.Add(recoveryPressureWait).Sub(now); left > 0 {
			return fmt.Sprintf("环境内存不足，自动重检最多再等待 %d 秒，之后仍会进行", (left+time.Second-1)/time.Second)
		}
		return "环境内存仍不足，自动重检即将进行"
	}
	if r.pressureLogged {
		return "自动重检正在等待内存压力缓解"
	}
	return "自动重检正在等待后台调度"
}

func (s *Server) scheduleNetworkRecovery(state cage.GateStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.networkPaused || s.closing || state.Mode != cage.GateClosed || state.Report == nil {
		return
	}
	s.recovery = networkRecovery{RunID: state.RunID, Configuration: state.Configuration, Epoch: state.Epoch, PauseVersion: s.safety.pauseVersion, Next: time.Now().Add(15 * time.Second)}
	s.refreshNetworkExplanationLocked(time.Now())
	diagnostics.Log("warn", "network", "temporary monitoring failure; full recheck scheduled: "+state.Reason)
}

func recoveryMatches(r networkRecovery, state cage.GateStatus, configuration string, paused bool, generation uint64) bool {
	return r.RunID != "" && !paused && generation == r.PauseVersion && state.RunID == r.RunID && state.Configuration == r.Configuration && configuration == r.Configuration && state.Epoch == r.Epoch && state.Mode == cage.GateClosed
}

// Called with s.op held. Admission uses the ordinary job queue and never changes
// saved network intent. The worker rechecks after acquiring s.op as well.
func (s *Server) recoverNetwork() {
	s.mu.Lock()
	r := s.recovery
	closing, paused, ready, jobRunning := s.closing, s.networkPaused, s.ready, s.jobs.snapshot().Running
	s.mu.Unlock()
	if r.RunID == "" {
		return
	}
	timing := s.startMonitorTiming()
	defer s.recordMonitorTiming("recovery", timing)
	// Read memory before any early return: the status explains it while the
	// recheck waits for any reason.
	resources, err := cage.ReadResourceStatus(s.instanceDir())
	now := time.Now()
	memory := memoryNote(resources, err, now)
	s.mu.Lock()
	s.safety.memory = memory
	s.refreshNetworkExplanationLocked(now)
	s.mu.Unlock()
	switch {
	case closing || paused:
		timing.result = "cancelled"
		return
	case !ready:
		timing.result = "environment_not_ready"
		return
	case jobRunning:
		timing.result = "job_running"
		return
	case now.Before(r.Next):
		timing.result = "retry_not_due"
		return
	}
	// The resource manager refreshes UpdatedAt even when a sample fails.
	// Only a recent pressure measurement should postpone the full gate check,
	// and only for recoveryPressureWait: a guest that stays short of memory
	// would otherwise never get its network back.
	guestPressure := resources.Pressure && now.Sub(resources.ObservedAt) <= 10*time.Second
	if err == nil && (guestPressure || resources.HostPressure) {
		s.mu.Lock()
		current := s.recovery == r
		logWait := current && r.pressureSince.IsZero()
		if logWait {
			s.recovery.pressureLogged = true
			s.recovery.pressureSince = now
			s.refreshNetworkExplanationLocked(now)
		}
		// The job must carry the recovery exactly as stored, or it is dropped.
		r = s.recovery
		s.mu.Unlock()
		if !current {
			timing.result = "superseded"
			return
		}
		if logWait {
			diagnostics.Log("info", "network", fmt.Sprintf("automatic recheck waiting for memory pressure to subside (guest=%t, host=%t); runs anyway after %s", guestPressure, resources.HostPressure, recoveryPressureWait))
		}
		if waited := now.Sub(r.pressureSince); waited < recoveryPressureWait {
			timing.result = "memory_pressure"
			return
		}
		timing.stages = append(timing.stages, "pressure_wait_elapsed=true")
		diagnostics.Log("warn", "network", fmt.Sprintf("memory pressure persisted for %s; running the automatic recheck anyway (guest=%t, host=%t, attempt=%d)", recoveryPressureWait, guestPressure, resources.HostPressure, r.Attempts+1))
	}
	_, _ = s.submitJob("network-recovery", func() error { return s.runNetworkRecovery(r, func() error { return networkRecoveryCheck(s) }) })
}

func (s *Server) runNetworkRecovery(expected networkRecovery, recheck func() error) error {
	s.mu.Lock()
	stopped := s.closing || s.networkPaused || s.safety.pauseVersion != expected.PauseVersion
	if stopped {
		s.recovery = networkRecovery{}
	}
	s.mu.Unlock()
	if stopped {
		return context.Canceled
	}
	client, err := s.gateClient()
	if err != nil {
		return s.finishNetworkRecovery(expected, err)
	}
	ctx, cancel := context.WithTimeout(s.operationContext(), 5*time.Second)
	state, err := client.Status(ctx)
	cancel()
	if err != nil {
		return s.finishNetworkRecovery(expected, err)
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return s.finishNetworkRecovery(expected, err)
	}
	s.mu.Lock()
	valid := !s.closing && s.recovery == expected && recoveryMatches(expected, state, cfg.Fingerprint(), s.networkPaused, s.safety.pauseVersion)
	if !valid {
		s.recovery = networkRecovery{}
	}
	s.mu.Unlock()
	if !valid {
		return context.Canceled
	}
	if err := s.operationContext().Err(); err != nil {
		return err
	}
	s.setMessage("正在重新检查环境与出口，通过后自动恢复联网…")
	err = recheck()
	return s.finishNetworkRecovery(expected, err)
}

func (s *Server) finishNetworkRecovery(expected networkRecovery, err error) error {
	// A failed full check changes the gate epoch. Keep that exact epoch for the
	// next attempt, while preserving user/configuration changes as cancellation.
	var current cage.GateStatus
	if err != nil {
		if client, e := s.gateClient(); e == nil {
			ctx, cancel := context.WithTimeout(s.lifetimeContext(), 3*time.Second)
			current, _ = client.Status(ctx)
			cancel()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.refreshNetworkExplanationLocked(time.Now())
	if s.recovery != expected {
		return err
	}
	if err == nil || s.closing || s.networkPaused || s.safety.pauseVersion != expected.PauseVersion || errors.Is(err, context.Canceled) {
		s.recovery = networkRecovery{}
		return err
	}
	if current.RunID != "" {
		if current.RunID != expected.RunID || current.Configuration != expected.Configuration || current.Mode != cage.GateClosed {
			s.recovery = networkRecovery{}
			return err
		}
		s.recovery.Epoch = current.Epoch
	}
	s.recovery.Attempts++
	delay := time.Duration(min(300, 15<<min(s.recovery.Attempts, 5))) * time.Second
	s.recovery.Next = time.Now().Add(delay)
	// Pressure may postpone the next attempt again, for at most
	// recoveryPressureWait after this backoff.
	s.recovery.pressureLogged = false
	s.recovery.pressureSince = time.Time{}
	// Show the latest full-check failure rather than the initial monitoring
	// error, which may describe a different missing sample by this point.
	s.safety.monitorError = diagnostics.Redact(usererror.Display("自动完整重检", err, "等待下一次重检；仍失败时导出诊断包。").Error())
	diagnostics.Log("warn", "network", fmt.Sprintf("automatic recheck attempt %d failed; next attempt in %s", s.recovery.Attempts, delay))
	return err
}

// memorySnapshot is what the network status says about the guest's memory.
type memorySnapshot struct {
	At     time.Time
	Short  bool
	Reason string
	Advice []string
}

// memoryNote builds the user-facing memory reason and advice from the
// resource manager's status. It is the one place the network explanation
// takes its memory wording from: concrete sizing advice published in the
// resource status (for example a suggested memory size) belongs here.
func memoryNote(status cage.ResourceStatus, err error, now time.Time) memorySnapshot {
	note := memorySnapshot{At: now}
	if err != nil {
		return note
	}
	switch {
	case status.Pressure && status.Guest != nil && now.Sub(status.ObservedAt) <= 30*time.Second:
		note.Short = true
		note.Reason = fmt.Sprintf("环境内存不足（可用约 %s，共 %s）", humanBytes(status.Guest.Available), humanBytes(status.Guest.Total))
	case status.Pressure && status.Error != "":
		// A starving guest also fails to answer the resource probe; keep its
		// last pressured reading rather than report the memory as fine.
		note.Short = true
		note.Reason = "环境内存不足，资源读数也暂时超时"
	case status.HostPressure:
		note.Short = true
		note.Reason = "运行环境的这台机器内存不足"
		note.Advice = []string{"关闭这台机器上其他占用内存较多的程序"}
		return note
	}
	if note.Short {
		note.Advice = []string{
			"在隔离浏览器中关闭或刷新占用内存较多的标签页，或结束环境里不需要的程序",
			"在资源设置中查看环境内存；需要时调高内存上限（重启环境后生效）",
		}
	}
	return note
}

func (s *Server) readMemorySnapshot() memorySnapshot {
	status, err := cage.ReadResourceStatus(s.instanceDir())
	return memoryNote(status, err, time.Now())
}

// withMemoryReason leads text with the memory shortage that likely caused it.
func withMemoryReason(memory memorySnapshot, text string) string {
	if !memory.Short {
		return text
	}
	return memory.Reason + "，" + text
}

func humanBytes(n uint64) string {
	if n < 1<<30 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%.1f GB", math.Floor(float64(n)/float64(1<<30)*10)/10)
}

// Network explanation states. The UI and the phone show reason, next and
// advice as they are; state lets them pick an icon or an action.
const (
	// The gate is open, but the last monitoring round could not confirm the
	// guest. The lease is not extended; it ends at lease_until unless a later
	// round confirms the guest first.
	networkUnconfirmed = "unconfirmed"
	// Closed; an automatic full recheck is due but waits for memory pressure
	// to subside, at most until recheck_at.
	networkWaitingMemory = "waiting_memory"
	// Closed; an automatic full recheck is scheduled (recheck_at, when known).
	networkRecheckScheduled = "recheck_scheduled"
	// The automatic full recheck is running.
	networkRechecking = "rechecking"
	// Closed with no automatic recheck: the user must act and check again.
	networkCheckRequired = "check_required"
	// The user paused networking.
	networkPausedState = "paused"
)

// networkExplanation says why networking is closed or at risk and what will
// happen next, in the product's words. Times are absolute; clients count down.
type networkExplanation struct {
	State       string     `json:"state"`
	Reason      string     `json:"reason"`
	Next        string     `json:"next"`
	RecheckAt   *time.Time `json:"recheck_at,omitempty"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	MemoryShort bool       `json:"memory_short"`
	Advice      []string   `json:"advice,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// refreshNetworkExplanationLocked recomputes safety.Explanation from the
// current state. Called with s.mu held on every network transition and every
// monitor and recovery round, so the stored value is at most a round old.
func (s *Server) refreshNetworkExplanationLocked(now time.Time) {
	s.safety.Explanation = s.networkExplanationLocked(now)
}

func (s *Server) networkExplanationLocked(now time.Time) *networkExplanation {
	gate := s.safety.Gate
	if s.closing || gate == nil {
		return nil
	}
	memory := s.safety.memory
	if now.Sub(memory.At) > 30*time.Second {
		memory = memorySnapshot{}
	}
	e := &networkExplanation{MemoryShort: memory.Short, Advice: memory.Advice, UpdatedAt: now.UTC()}
	if s.networkPaused {
		e.State, e.Reason, e.Next, e.Advice = networkPausedState, "外网已手动暂停", "离线终端和文件仍可使用；重新检测通过后恢复联网。", nil
		return e
	}
	reason := s.networkErrorLocked()
	if reason == "" {
		reason = gate.Reason
	}
	if gate.Mode == cage.GateOpen {
		d := s.activeDeferralLocked(now)
		if d == nil {
			return nil
		}
		// The lease's end as the gate times it, shown on today's wall clock.
		until := now.Add(d.Deadline.Sub(now)).UTC()
		e.State, e.LeaseUntil = networkUnconfirmed, &until
		e.Reason = withMemoryReason(memory, "联网监测暂时"+unconfirmedStage(d.Stage))
		e.Next = "外网暂时保持开放，但联网许可没有延长。系统会继续重试；许可到期前仍无法确认时，外网会自动暂停并安排完整重检。"
		return e
	}
	if s.proxyNetworkLost {
		e.State, e.Reason = networkRecheckScheduled, reason
		e.Next = "外网暂停中。电脑的网络恢复后，会自动重新连接代理并重新检测。"
		return e
	}
	if r, pending := s.pendingRecoveryLocked(); pending {
		// A temporary failure: the memory shortage is its likely cause.
		e.Reason = withMemoryReason(memory, reason)
		job := s.jobs.snapshot()
		switch {
		case job.Running && job.Kind == "network-recovery":
			e.State, e.Next = networkRechecking, "正在执行自动完整重检，通过后自动恢复联网。"
		case job.Running:
			e.State, e.Next = networkRecheckScheduled, "外网暂停中。自动完整重检会在当前操作完成后进行，通过后自动恢复联网。"
		case !s.ready:
			e.State, e.Next = networkRecheckScheduled, "外网暂停中。自动完整重检会在环境就绪后进行，通过后自动恢复联网。"
		case !r.pressureSince.IsZero():
			at := r.pressureSince.Add(recoveryPressureWait)
			if at.Before(now) {
				at = now
			}
			at = at.UTC()
			e.State, e.RecheckAt = networkWaitingMemory, &at
			e.Next = "外网暂停中。自动完整重检正在等待内存压力缓解，最迟" + roughWait(at.Sub(now)) + "也会进行，通过后自动恢复联网。"
		default:
			at := r.Next
			if at.Before(now) {
				at = now
			}
			at = at.UTC()
			e.State, e.RecheckAt = networkRecheckScheduled, &at
			e.Next = "外网暂停中。自动完整重检将在" + roughWait(at.Sub(now)) + "进行，通过后自动恢复联网。"
		}
		return e
	}
	if gate.Mode != cage.GateClosed || s.jobs.snapshot().Running {
		// A check or another operation owns the next transition; its
		// progress is the job's message.
		return nil
	}
	if gate.Report != nil && (gate.Reason == gateLeaseExpired || s.windowsGuest != nil && gate.Reason == windowsPacketDisconnectedReason) {
		// The monitor schedules the recheck on its next round.
		e.State, e.Reason = networkRecheckScheduled, withMemoryReason(memory, reason)
		e.Next = "外网暂停中。马上安排自动完整重检，通过后自动恢复联网。"
		return e
	}
	// No automatic recheck is scheduled. Memory stays in memory_short and
	// advice; the reason is the closure's own cause.
	e.State, e.Reason = networkCheckRequired, reason
	e.Next = "外网保持关闭，目前没有安排自动重检。处理上述问题后请重新检测。"
	return e
}

// roughWait words a wait that clients also show as a countdown; the stored
// text may be a monitor round old.
func roughWait(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "片刻后"
	case d < 90*time.Second:
		return fmt.Sprintf("约 %d 秒后", (int(d.Seconds())+4)/5*5)
	default:
		return fmt.Sprintf("约 %d 分钟后", int(math.Round(d.Minutes())))
	}
}
