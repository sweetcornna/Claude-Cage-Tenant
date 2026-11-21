# Super Cage automatic resource allocation — design

Status: design for branch resource-allocation (base origin/super-cage-redesign f30a5d4).
Scope: three levels — A gateway host <-> workspace VMs (tenant edition), B machine <-> Cage guest (host agent),
C inside the Linux guest. CPU where it is cheap. Status that the UI shows without interpretation.

## 0. What went wrong (evidence, 2026-11-21)

Workspace VM 8 GiB (4 GiB available), guest booted with -m 3072 (old automatic sizing: host.Cap halves the
7 GiB MemTotal to 3 GiB, AutomaticMemoryGiB(3) = 3), 3 vCPUs (6 / 2). One renderer reached 1.7 GB; guest
available 48 MB, zram (MemTotal/4 = 727 MiB) full; every SSH-based probe timed out; the balloon target was
already at the 3 GiB maximum so level B could not help; status said pressure=true forever and the network
recovery waited on it. Half of the machine was idle.

Root causes, one per level:
- B: ceiling fixed at half of the machine, chosen at boot; no way to use the idle half.
- C: one pool, no priorities: the browser starved sshd sessions, the probes, the browser agent.
- B/status: "pressure" was the guest-wide PSI, so a contained browser would still have blocked recovery;
  a failed sample said "自动回收内存已暂停" and jumped the target to the maximum.

## 1. Common definitions

All sizes in bytes, rounded to 256 MiB units (unit) unless stated.

- Working set W = MemTotal - MemAvailable (+ kernel memory outside MemTotal where the outer level pays it).
  MemAvailable counts reclaimable page cache and reclaimable slab as available, so a page-cache-heavy
  workload has a small W: reading files cannot raise demand. (Not gameable by page cache.)
- Headroom h(W) = max(1 GiB, W/4) (unchanged from today at both levels).
- Instantaneous demand d = W + h(W) [+ kernel term], clamped to [guarantee, ceiling].
- Shortage boost: when the consumer is short — PSI memory "some avg10" >= 10 %, or swap-in activity since
  the last sample (pswpin advanced), or available below h/4 — d = max(d, current + max(512 MiB, current/4)).
  That covers file-backed thrashing (refaults) that W does not show. A tenant can fake it only inside its
  own VM, which at most raises its own demand up to its own ceiling; max-min fairness below caps it at the
  water level whenever others are short.
- Held demand D: D = d when d >= D_prev (rises at once); otherwise D_prev is kept for the hold window, then
  decays by one shrink step per round. Constant d => constant D => constant allocation.
- Cold swap is not demand: swap used counts only while pages are being swapped in (pswpin advanced).
  (Today level B adds all swap used to "used", so one burst kept the target high for ever.)

## 2. Level A — gateway host <-> workspace VMs (internal/tenant)

Pure function: tenant.AllocateMemory(AllocationInput) -> []Allocation{ID, From, To, Demand, Fair, Reason}.

Inputs per round (every -memory-interval, default 15 s): host total H, available Ha, PSI stall Hs; reserve
R = -memory-reserve or max(2 GiB, 15 % H); profile ceiling; per instance: status, limit L, guest
MemTotal/MemAvailable, guest PSI and pswpin (new, read in the same incus exec), active (used within 1 min),
hotplugged, silent count, fixed (starting/cooling/foreign), hold-shrink (shrink failed < 1 min ago);
per-instance state: held demand D and the time it was set.

1. Guarantee g = clamp(-memory-min (default 2 GiB), 1 GiB, ceiling). Ceiling c = min(profile ceiling, H - R) >= g.
2. Pinned P = sum of limits of frozen, fixed and silent instances (they keep their size this round).
   Capacity C = H - R - P.
3. Water-filling (max-min fair): f_i = max(g_i, min(D_i, lambda)), lambda = largest level with
   sum f_i <= C (bisection over sorted demands; exact). If sum g > C, f = g (overcommitted guarantees: status
   says so). Properties: sum f <= C when sum g <= C; f_i >= g_i; f_i <= D_i; if some f_i < D_i then
   sum f = C (work-conserving, nothing idle while someone is short); the smallest unmet demand is met first;
   f is monotone in every D_j for its own i and anti-monotone in the others.
4. Grow: L_i < f_i -> to roundUp(f_i) at once, also under host pressure (f is already fair; a guest OOM is
   worse than host reclaim). Reason "guest needs memory" / "fair share of a short host".
5. Shrink — only memory above the floor F_i = max(f_i, W_i + 256 MiB, g_i), never below the working set:
   a. capacity needed: N = sum_j max(L_j, f_j) - C > 0. Take N from instances with L > F, inactive first,
      then active, within a tier proportional to slack (L - F), at most step per instance per round.
   b. idle return: an inactive instance with L - max(D, g) >= step returns one step per round toward
      max(D, g). D only decays after the 5-minute hold, so a VM idle for 5 minutes starts to give back;
      an active VM keeps its excess (its page cache is in use) unless (a) or (c).
   c. host pressure (Ha < 2R or Hs >= 10): release N_p = max(2R - Ha, step) by tiers:
      T1 inactive above d_i (instantaneous demand), T2 active above d_i (proportional to slack),
      T3 inactive above W_i + 256 MiB, T4 active above W_i + 256 MiB. Never below W + 256 MiB, never
      below g. Step under pressure 1 GiB per instance per round.
   Shrinks smaller than the step (512 MiB) are not issued (hysteresis); hotplugged and hold-shrink
   instances only grow.
6. Silent guest (sample failed): hold. After 2 silent rounds it grows back (to the ceiling, or one step per
   round under host pressure) — unchanged: a guest that stops answering after a shrink may be thrashing.
   A failed sample never shrinks.
7. Frozen (cooled) instances are pinned: their balloon cannot move while vCPUs are paused; idle cooling
   already pushes their memory to swap (memory.reclaim). Idle-first in T1/T3 is the "cooled or idle first".

Constants: unit 256 MiB, step 512 MiB (1 GiB under pressure), hold 5 min, active window 1 min, headroom
max(1 GiB, W/4), interval 15 s.

Why the levels do not feed back: A's demand for a machine is the machine's working set, which contains the
nested guest's resident memory; B keeps the guest near its own demand. A grows the machine only when the
machine's working set rises (the guest really uses more); B grows the guest only on guest demand, never
because the machine grew; B shrinks the guest when the machine shrinks only down to the guest's demand.
The chain "A shrinks idle machine -> B returns guest cache -> A shrinks more" is monotone and stops at the
demands (each step only takes memory above a working set + headroom); it runs only for inactive machines or
under host pressure. No step of either level is triggered by the other level giving memory back.

8. Nested demand (the machine runs a Cage guest): the machine's working set already contains the guest's
   resident memory (QEMU anonymous memory), so guest page cache is demand to A unless B reclaims it; the
   "not gameable by page cache" property of A holds for nested guests through B's reclaim (B keeps the
   guest within d + 512 MiB after 30 s of headroom). B also publishes what the guest wants and the machine
   cannot back now (short_bytes) in <dir of -guest-control-path>/machine-memory.json; A reads it in the
   same incus exec and adds it to the machine's instantaneous demand (fresh < 30 s, bounded by the
   ceiling, tenant-controlled like meminfo, so it can at most claim its own ceiling under max-min). Without
   it a short guest on a parked machine could only grow by the machine's headroom per A round; with it A
   covers the guest's want in one round (<= 15 s + balloon time).
9. Reading the guest: one incus exec of /bin/sh -c 'head meminfo; head pressure 2>/dev/null; grep vmstat;
   head machine-memory.json 2>/dev/null; exit 0': missing files are tolerated (old kernels without PSI,
   machines without a Cage guest), only meminfo is required.
Legacy policy reachable with CAGE_TENANT_MEMORY_POLICY=legacy in the gateway's environment (no new flag:
cmd/cage-tenant is outside this change).

## 3. Level B — machine <-> Cage guest (internal/cage, host agent)

### 3.1 Ceiling (boot-time maximum, -m) follows the machine

Automatic mode (default for new environments and for files without an explicit user choice):
  machine total Mt = boot physical memory of the machine: macOS/Windows: installed RAM; Linux:
  sum of zone "present" pages in /proc/zoneinfo (a ballooned workspace VM reports a smaller MemTotal,
  but the gateway may grow it back to its boot size; the ceiling is about what the machine can become).
  Reserve for the machine's own needs Rm:
    dedicated machine (Linux without a graphical session: the tenant workspace VM, a server): max(1.5 GiB, Mt/8)
    desktop (macOS, Windows, Linux with DISPLAY/WAYLAND_DISPLAY):                         max(4 GiB, Mt/4)
    override: CAGE_MACHINE_ROLE=dedicated|desktop.
  M = clamp(floorGiB(Mt - Rm), 2 GiB, 32 GiB), in whole GiB. An explicit AGENT_VM_HOST_SHARE (an admin
  choice) still caps it at Mt/share. The -m path: allocation.Limits() -> vm.Lima.Start -> Edit ->
  lima.yaml has no host.Cap on it (host.Cap only clamps explicit CLI flags and the base build).
  8 GiB workspace VM -> 6 GiB (was 3). 16 GiB Mac -> 12 (was 8). 32 GiB -> 24 (was 12). 4 GiB machine -> 2.
  vCPUs: dedicated n-1 (>=1); desktop n - max(2, n/4) (>=2, <=n). 6-CPU workspace VM -> 5 (was 3).
  macOS guests keep their fixed-memory cap (8 GiB, no balloon) and get the same vCPU rule; Windows guests
  (Hyper-V) keep their own ceiling code unchanged.
Manual mode: the user's GiB/CPUs exactly (range raised to the automatic maximum); target logic below still
runs under that ceiling. The old fixed behaviour is reachable by choosing a manual value.

### 3.2 Existing environments: explicit choice vs. old default

resource-limits.json written by this version carries "chosen": "user" (set by the API on every save; older
versions ignore unknown fields). A file without it and with automatic=false is classified:
  - legacy default: memory_gib and cpus equal what the old automatic formula gave on this machine
    (memory: old AutomaticMemoryGiB(share(m)) for some m between the machine's current MemTotal and its
    boot size, cpus: old CPU share). Matching the old formula cannot prove the user did not choose it, so
    the classification is a pure function returning the origin, and what to do with it is one switch
    (LegacyDefaultAction = "offer" | "apply", default "offer"): "offer" keeps the saved values and puts a
    one-step offer in the status: allocation_origin="legacy_default", legacy_allocation={memory_gib,cpus},
    notice "当前的 3 GB 内存 / 3 个 CPU 与旧版默认值相同，可能不是手动选择的。改为自动（推荐）后，内存上限
    可到 6 GB、CPU 5 个，需要重启环境。", offer {allocation:{automatic:true}, apply:true}. "apply" would
    treat it as automatic with the same notice in the past tense. Owner decision; default is the safe one.
  - otherwise: an explicit user choice, never changed; status advice suggests automatic when short.
  - no file (the common case, also likely the production one: the old automatic formula also yields 3/3
    on that machine): automatic, the new sizing applies at the next environment start.
The file is never rewritten by the classification (no write in a read path, no race with a save; rollback
leaves the old version reading the same file). One step to move any environment to automatic: POST /api/environment/resources
{"allocation":{"automatic":true},"apply":true} (save and restart in one call; "apply" is additive — without
it the call only saves, as today).

### 3.3 Target (balloon), every 2 s

Pure function: cage.PlanGuestMemory(GuestInput, *GuestState) -> GuestDecision{Target, Wanted, Capacity,
Demand, Reason, Short, ...}.

Inputs: guest sample (MemTotal, MemAvailable, MemFree, swap, pswpin, OOM kills, balloon, PSI global and per
class), machine reading (Linux: /proc/meminfo MemTotal/MemAvailable/PSI; other OSes: pressure flag only),
boot maximum M, previous target P, quiet (guest CPU < 5 %), state (held demand, idle-since, failures).

- floor g = min(M, 2 GiB).
- demand d = W + h(W) + 256 MiB (kernel and balloon bookkeeping outside MemTotal) [+ swap used only while
  swapping in], roundUp, clamp [g, M]. Pressure = available < max(512 MiB, MemTotal/8) or protected PSI >= 1;
  severe = available < max(256 MiB, MemTotal/16) or protected PSI >= 10. "Protected PSI" is the stall of the
  control and session cgroups (section 4); a throttled browser alone is not guest pressure.
- Machine capacity K (Linux machines): K = min(M, Rg + Ma - margin), Rg = MemTotal - MemFree of the guest
  (its resident estimate), Ma = machine MemAvailable, margin = clamp(machine total/64, 256 MiB, 512 MiB) —
  deliberately below A's minimum headroom (1 GiB) so a machine parked by A at W + headroom never pins the
  guest (advisor finding: with total/20 a 32 GiB machine had a 1.6 GiB margin against 1 GiB of headroom,
  K < P, a deadlock). When the guest wants more than K, B publishes short_bytes = wanted - K for A.
  macOS / Windows (no reliable "available"): K = M while the machine is calm; on an elevated pressure
  signal (darwin kern.memorystatus_vm_pressure_level >= 2 "warn", Windows free < 20 %) K = max(d,
  old share = half the machine): growth past the old ceiling stops early and the excess cache is returned.
- Grow (pressure or d > P): severe -> min(M, K) at once; pressure -> min(M, K, max(d, P + max(2 GiB, P/2)))
  (geometric); otherwise d. Under machine pressure: pressure -> max(P, min(d, K)); else hold.
- Machine shrinking: P > K -> target max(d, K) at once (only guest cache/free is taken: never below d).
  This is how the host agent adapts when the gateway shrinks the machine, before the machine swaps; the
  gateway shrinks at most 512 MiB per 15 s and never below the machine's working set + 1 GiB, so B has
  several samples to react.
- Shrink (idle return): after sustained headroom P >= d + 512 MiB for 30 s (10 s quiet, 0 under machine
  pressure), reclaim a quarter of the excess (half when quiet or machine pressure), >= 256 MiB, never below
  d, and only once the balloon reflects the previous reclaim (unchanged rules).
- Sample failure: hold for 2 failures in a row; from the 3rd, grow one 1 GiB step per failure toward
  min(M, K_last) (a stalled guest may be out of memory; K keeps the machine safe). Never shrink on failure.
  Status: "暂时读不到环境的内存用量（第 N 次），保持当前分配 X GB" / "…已逐步放宽到 X GB". The first good
  sample resumes normal operation; nothing pauses for ever.
- Short: pressure while P >= min(M, K) — the guest cannot get more without a human or without the gateway.

## 4. Level C — inside the Linux guest (provisioning, internal/cage)

cgroup v2 tree (all runtime state, recreated on every environment start by the start script):
  -.slice
  ├─ system.slice         control plane: sshd, lima-guestagent, journald, display, editor, governor
  │                       CPUWeight=200, MemoryLow = dynamic (control use + 128 MiB, <= 25 % of guest)
  ├─ user.slice           control plane, part 2: SSH sessions WITHOUT a terminal — the host agent's
  │  │                    resource probe, the shared control session (guest-session.py), the CLI channel,
  │  │                    one-shot scripts. CPUWeight=200, MemoryLow dynamic
  │  ├─ user-<cage>.slice MemoryLow = same value as user.slice (claims the whole parent protection)
  │  └─ user-<cage-browser>.slice   (Ubuntu: snap Chromium lives here) MemoryHigh/Max = browser limits
  ├─ cage-sessions.slice  new: terminals. The guest governor adopts every process of the cage user that
  │                       has a controlling terminal and CAGE_WORKBENCH=1 in its environment, and every
  │                       claude / codex process tree, into a transient scope cage-term-<pid>.scope here
  │                       (PID 1 StartTransientUnit with PIDs, through /run/systemd/private: systemd moves
  │                       and tracks them). CPUWeight=100, MemoryLow = sessions use + 256 MiB (<= 50 % G),
  │                       MemoryHigh = G - control protection - 256 MiB (soft: a runaway build or session
  │                       is reclaimed/throttled before it can starve the probes; never a hard max)
  └─ cage-browser.slice   new. CPUWeight=50, IOWeight=50
     └─ cage-browser.service  Delegate=memory cpu, OOMScoreAdjust=200 (below Chromium's renderer scores,
                              >= 300, which Chromium may raise: a renderer dies before the agent)
        ├─ agent/      node browser agent, IBus, wrappers. memory.low 256 MiB
        └─ chromium/   Debian Chromium (moved before exec by /etc/chromium.d/zz-cage-cgroup;
                       governor sweep as fallback). memory.high / memory.max = browser limits

Browser limits (pure function cage.PlanGuestClasses, computed by the host agent, applied in the guest):
  G = guest attainable size min(M, K) (not the current target, so the browser may grow while B grows the
  guest, and shrinks first when the machine shrinks).
  protected = control use + sessions use + 512 MiB (new sessions, probes).
  Why three tiers need the adoption step: probes and terminals both arrive as SSH sessions of the same
  user; MemoryLow on their common parent does not protect a new tiny probe scope from a 3 GB sibling.
  browser high = clamp(G - protected, 768 MiB, 75 % G); browser max = high + max(256 MiB, G/16).
  Evidence case (G = 3 GiB, control 0.4 + sessions 0.55): high 1.55 GiB, max 1.73 GiB: the 1.7 GB tab
  is reclaimed into zram and throttled, then killed alone at max; control and sessions keep their memory.
  New sizing (G = 6 GiB): high 4.5 GiB: the same tab simply fits.

Channel: the host agent's probe writes the limits into /run/cage-resources/limits (cage-writable runtime
directory) when they change; the guest governor (cage-resource-governor.service, root, system.slice,
fail-open: every cgroup step is best effort, the browser starts regardless,
OOMScoreAdjust=-900, MemoryMin=32M, python3, 2 s loop) applies them: raw writes in the delegated browser
subtree (Debian), systemctl set-property --runtime for slices (Ubuntu browser slice, MemoryLow of
system/user slices), with hysteresis (>= 64 MiB change). Without a fresh limits file (no host agent, stale
> 60 s) it computes the same formula from its own MemTotal. Class usage (memory.current, memory.pressure)
is read by the probe directly from cgroupfs (world-readable) and reported in the status.

OOM policy (cheapest loss first):
  1. reclaim: browser above memory.high is reclaimed into zram and throttled (no kill);
  2. (follow-up, needs browser-agent endpoint) discard hidden background tabs;
  3. browser at memory.max: cgroup-local OOM kills the largest browser process (the runaway renderer —
     the tab shows a crash page, others survive); memory.oom.group stays 0;
  4. global OOM order: renderers (>= 300) -> browser main/agent (200) -> editor (adj 100) -> sessions and
     others by size (0)
     -> guest agent, governor (-900), sshd listener (-1000, set by sshd).
  A claude session is not lowered below the control plane on purpose: tiny control processes with a higher
  score than a big session would be killed one after another without freeing memory.
zram: disksize = min(MemTotal/2, 4 GiB) (was /4, 2 GiB), priority 100; vm.swappiness=100 (runtime sysctl):
anonymous browser memory is compressed before file cache of the sessions is dropped.
CPU: CPUWeight 200 / 100 / 50 as above; the browser cannot starve sshd or the probes of CPU.

Browser-agent endpoint (follow-up, not in this branch; browser-agent.mjs is owned elsewhere):
  POST /memory/reclaim {"level":"moderate"|"critical"} -> {"discarded":[ids],"purged":n}
  moderate: for every non-visible tab: HeapProfiler.collectGarbage, Memory.simulatePressureNotification
  {level:"moderate"}; critical: additionally close hidden background tabs that are not playing audio, not
  login/private, not pinned to a split pane, remembering URL+title and recreating them on activation
  (Chrome's own discard semantics). The governor calls it when the browser is above memory.high for 10 s
  (moderate) or at 90 % of memory.max (critical); 404 -> skipped.

Container mode (containerenv): no VM boundary; the outer cgroup (host user slice MemoryMax, CPUQuota,
TasksMax from the server deployment) is authoritative and unchanged; no zram, no swap; the slice setup is
attempted only when the container's systemd owns a writable cgroup subtree, otherwise skipped; level B does
not run (no balloon). macOS/Windows guests: level B does not run (no Linux probe); level C does not apply;
their sizing code is untouched except the shared vCPU rule for macOS guests.

## 5. Status (additive; ResourceStatus version stays 1)

cage-resources.json (host agent) gains:
  capacity_bytes (K), demand_bytes (d), wanted_bytes, reason (code), summary (Chinese sentence),
  next_action (Chinese), short (bool), possible_bytes (automatic ceiling this machine supports),
  restart_required (bool: possible > maximum), advice {message, memory_bytes, restart_required},
  machine {total_bytes, boot_bytes, available_bytes, reserve_bytes, pressure},
  classes [{name: control|sessions|browser, current_bytes, protected_bytes, high_bytes, max_bytes,
            stall_percent}], browser_pressure, failures.
  "pressure" keeps its name and now means the guest's protected classes are short (falls back to guest-wide
  PSI on guests without the slices). network_recovery.go (owned elsewhere) consumes it: with this change a
  browser contained at its limit no longer postpones the automatic network recheck.
  Size stays well under the 16 KiB read limit (classes: 3 entries, short strings).
/api/environment/resources gains: allocation_origin, legacy_allocation, notice, automatic_memory_gib,
automatic_cpus, advice (from the observation or from sizing when stopped), possible_memory_bytes.
A caller that wants "short of memory, N GB possible": observation.short && observation.advice != null ->
advice.message ("可以把内存调到 N GB，需要重启环境"), advice.memory_bytes, advice.restart_required.

## 6. Rollback to 0.1.15

Gateway host: state left = instance-local limits.memory on running VMs, exact MiB sizes between 1 GiB and
the profile ceiling: exactly what 0.1.15's balancer writes and its elasticMemory check accepts. No new Incus
keys, no profile change. Steps: stop the new cage-tenant; install the 0.1.15 binary; start. (For releases
before 0.1.15 only: incus config unset cage-<id> limits.memory for each instance, VMs stopped first.)
Desktop / workspace: settings file unchanged by classification; the extra "chosen" key is ignored by 0.1.15.
lima.yaml memory/cpus are recomputed by whichever version starts the environment. Guest: the new slice,
governor unit, drop-ins and limits live in /run (gone at the next boot); the persisted changes are the
cage-browser.service unit and cage-memory-start (rewritten by 0.1.15's component update, which it asks for
because the guest manifest revision differs), /usr/local/bin/cage-resource-governor and
/etc/chromium.d/zz-cage-cgroup (inert without the delegated unit: the hook only moves itself when the
chromium leaf exists and is writable). Steps: install 0.1.15; open the environment; accept the component
update ("环境组件需要更新"), which restarts the browser; restart the environment once so /run is cleared and
the old -m applies.
