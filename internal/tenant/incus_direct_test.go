package tenant

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// directFixture is a VM-mode runtime whose incus CLI is a script and whose
// vsock and exec dials reach one fixture guest, so the connection logic runs
// without Incus. The script's bridge branch stands in for the guest socat: it
// leaves "started" and "stopped" marker files.
type directFixture struct {
	t         *testing.T
	runtime   *IncusRuntime
	calls     string
	dir       string
	instance  string
	vsock     atomic.Int32
	execs     atomic.Int32
	vsockFail atomic.Bool
	// listening makes the vsock dial succeed even before any bridge runs.
	listening atomic.Bool
	// notReady makes the bridge accept a connection and drop it, as socat does
	// while nothing listens on the workbench's port yet.
	notReady atomic.Bool
	// brokenRelay makes vsock connections reach a server that answers HTTP but
	// is not the workbench, so the probe passes and the handshake fails.
	brokenRelay atomic.Bool
	relay       *pipeListener
}

func newDirectFixture(t *testing.T, vsock bool) *directFixture {
	t.Helper()
	id := strings.Repeat("7", 32)
	f := &directFixture{t: t, instance: "local:cage-" + id}
	guest := serveFixture(t, fixtureGuest(id))
	f.relay = serveFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{}") }))
	control := filepath.Join(t.TempDir(), "control.json")
	f.dir = t.TempDir()
	started := filepath.Join(f.dir, "started")
	stopped := filepath.Join(f.dir, "stopped")
	f.runtime, f.calls = fakeIncus(t, `"--force-local --project cage-tenant exec local:cage-`+id+` --mode=non-interactive -- /usr/bin/head -c 8193 -- `+control+`") echo '{"port":45001,"token":"`+fixtureLauncher+`"}' ;;
"--force-local query local:/1.0/instances/cage-`+id+`?project=cage-tenant") echo '{"name":"cage-`+id+`","type":"virtual-machine","config":{"user.cage-tenant.id":"`+id+`","volatile.vsock_id":"42"}}' ;;
*"exec local:cage-`+id+` --mode=non-interactive -- /usr/bin/socat VSOCK-LISTEN:45001,fork,reuseaddr,backlog=128 TCP4:127.0.0.1:45001,connect-timeout=5") echo started >> '`+started+`'; trap "echo stopped >> '`+stopped+`'; exit 0" TERM; sleep 30 & wait ;;`)
	f.runtime.cfg.ControlPath = control
	f.runtime.cfg.Vsock = vsock
	f.runtime.vsockDial = func(ctx context.Context, cid, port uint32) (net.Conn, error) {
		f.vsock.Add(1)
		if cid != 42 || port != 45001 {
			t.Errorf("vsock dial to %d:%d, want 42:45001", cid, port)
		}
		if f.vsockFail.Load() {
			return nil, errors.New("connection reset by peer")
		}
		if _, err := os.Stat(started); err != nil && !f.listening.Load() {
			return nil, errors.New("connection refused")
		}
		if f.notReady.Load() {
			client, server := net.Pipe()
			server.Close()
			return client, nil
		}
		if f.brokenRelay.Load() {
			return f.relay.dial(ctx)
		}
		return guest.dial(ctx)
	}
	f.runtime.execDial = func(ctx context.Context, instance string, port int) (net.Conn, error) {
		f.execs.Add(1)
		if instance != f.instance || port != 45001 {
			t.Errorf("exec dial to %s:%d", instance, port)
		}
		return guest.dial(ctx)
	}
	return f
}

func (f *directFixture) connect() *Workspace {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 10*time.Second)
	defer cancel()
	workspace, err := f.runtime.connect(ctx, f.instance)
	if err != nil {
		f.t.Fatalf("connect: %v\n%v", err, readCalls(f.t, f.calls))
	}
	f.t.Cleanup(workspace.close)
	return workspace
}

func (f *directFixture) marker(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, name))
	return err == nil
}

// requests sends count health checks over the workspace's own transport.
func requests(t *testing.T, workspace *Workspace, count int) {
	t.Helper()
	for range count {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		err := workspace.check(ctx)
		cancel()
		if err != nil {
			t.Fatalf("workbench request: %v", err)
		}
		// A new connection per request, as for a burst of streams.
		workspace.control.CloseIdleConnections()
	}
}

func TestIncusDirectPathDialsWithoutExec(t *testing.T) {
	f := newDirectFixture(t, true)
	workspace := f.connect()
	requests(t, workspace, 5)
	if f.execs.Load() != 0 || f.vsock.Load() < 5 {
		t.Fatalf("exec dials %d, vsock dials %d: want the handshake and every request over vsock", f.execs.Load(), f.vsock.Load())
	}
	if !f.marker("started") {
		t.Fatal("the guest bridge was not started")
	}
	for _, call := range readCalls(t, f.calls) {
		if strings.Contains(call, "socat STDIO") {
			t.Fatalf("a per-connection exec was started: %q", call)
		}
	}
	// The bridge ends with the workspace.
	workspace.close()
	waitFixture(t, func() bool { return f.marker("stopped") })
}

func TestIncusDirectPathIsOffByDefault(t *testing.T) {
	f := newDirectFixture(t, false)
	workspace := f.connect()
	requests(t, workspace, 3)
	if f.vsock.Load() != 0 || f.marker("started") || f.execs.Load() == 0 {
		t.Fatalf("vsock dials %d, bridge started %v, exec dials %d: the default must stay on exec", f.vsock.Load(), f.marker("started"), f.execs.Load())
	}
	for _, call := range readCalls(t, f.calls) {
		if strings.Contains(call, "/1.0/instances/cage-") {
			t.Fatalf("the vsock address was looked up without the option: %q", call)
		}
	}
}

func TestIncusDirectPathReusesAListenerThatIsAlreadyThere(t *testing.T) {
	f := newDirectFixture(t, true)
	f.listening.Store(true)
	workspace := f.connect()
	requests(t, workspace, 2)
	if f.marker("started") || f.execs.Load() != 0 {
		t.Fatalf("a second bridge was started (%v) or exec used (%d) although a listener answered", f.marker("started"), f.execs.Load())
	}
}

func TestIncusDirectPathFallsBackToExecWhenTheBridgeNeverListens(t *testing.T) {
	old := directWait
	directWait = 400 * time.Millisecond
	t.Cleanup(func() { directWait = old })
	f := newDirectFixture(t, true)
	f.vsockFail.Store(true)
	workspace := f.connect()
	requests(t, workspace, 3)
	if f.execs.Load() == 0 {
		t.Fatal("the workspace never used exec after the vsock path failed")
	}
	// The bridge that never listened is ended at once, not kept until the
	// workspace closes.
	waitFixture(t, func() bool { return f.marker("stopped") })
	// A failed instance is not retried at once: the next workspace of it goes
	// straight to exec without starting another bridge.
	before := f.vsock.Load()
	second := f.connect()
	requests(t, second, 1)
	if f.vsock.Load() != before {
		t.Fatalf("vsock was dialed %d more times for an instance that just failed", f.vsock.Load()-before)
	}
	if f.runtime.directAllowed(f.instance) {
		t.Fatal("a failed instance may be tried again at once")
	}
}

func TestIncusDirectPathSurvivesAStrayDialFailure(t *testing.T) {
	f := newDirectFixture(t, true)
	workspace := f.connect()
	requests(t, workspace, 1)
	// A burst of connections can overrun the listener once: that dial is served
	// over exec and the path stays.
	f.vsockFail.Store(true)
	requests(t, workspace, 1)
	f.vsockFail.Store(false)
	before := f.vsock.Load()
	requests(t, workspace, 3)
	if f.execs.Load() != 1 || f.vsock.Load() != before+3 {
		t.Fatalf("exec dials %d, vsock dials %d -> %d: want one fallback and vsock again afterwards", f.execs.Load(), before, f.vsock.Load())
	}
	if !f.runtime.directAllowed(f.instance) {
		t.Fatal("one failed dial put the instance on exec")
	}
}

func TestIncusDirectPathLatchesOffAfterSeveralDialFailures(t *testing.T) {
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	f := newDirectFixture(t, true)
	workspace := f.connect()
	requests(t, workspace, 1)
	if f.execs.Load() != 0 {
		t.Fatalf("exec dials %d before any failure", f.execs.Load())
	}
	// The guest side goes away: every failed dial falls back inside the same
	// dial, so the requests still succeed, and after a few in a row the path is
	// dropped without further vsock attempts.
	f.vsockFail.Store(true)
	requests(t, workspace, directDialFailures)
	dials := f.vsock.Load()
	requests(t, workspace, 3)
	if f.execs.Load() != directDialFailures+3 || f.vsock.Load() != dials {
		t.Fatalf("exec dials %d, vsock dials %d -> %d: want exec from now on and no further vsock attempts", f.execs.Load(), dials, f.vsock.Load())
	}
	if f.runtime.directAllowed(f.instance) {
		t.Fatal("an instance whose path died may be tried again at once")
	}
	if !strings.Contains(output.String(), "the vsock path ended") {
		t.Fatalf("dropping the path was not logged: %q", output.String())
	}
}

func TestIncusDirectPathWaitsForAWorkbenchThatIsNotUpYet(t *testing.T) {
	f := newDirectFixture(t, true)
	// During boot the guest side listens before the workbench does: the bridge
	// accepts and drops. That is not held against the path.
	f.notReady.Store(true)
	first := f.connect()
	requests(t, first, 2)
	if f.execs.Load() == 0 {
		t.Fatal("a workbench that does not answer over vsock must be reached over exec meanwhile")
	}
	if !f.runtime.directAllowed(f.instance) {
		t.Fatal("a workbench that was not up yet put the instance on exec for good")
	}
	first.close()
	// The workbench is up: the next workspace of the instance uses vsock.
	f.notReady.Store(false)
	execs := f.execs.Load()
	second := f.connect()
	requests(t, second, 3)
	if f.execs.Load() != execs {
		t.Fatalf("the workspace used exec %d more times although vsock works", f.execs.Load()-execs)
	}
}

func TestIncusDirectPathBlamesVsockOnlyWhenExecAnswersWhereItDoesNot(t *testing.T) {
	f := newDirectFixture(t, true)
	// The relay answers HTTP but is not the workbench: the probe passes, the
	// handshake fails over vsock and works over exec, which proves the path wrong.
	f.brokenRelay.Store(true)
	workspace := f.connect()
	requests(t, workspace, 2)
	if f.execs.Load() == 0 || f.runtime.directAllowed(f.instance) {
		t.Fatalf("exec dials %d, direct allowed %v: want the workspace on exec and the instance skipped", f.execs.Load(), f.runtime.directAllowed(f.instance))
	}
}

func TestNewIncusRefusesVsockForContainers(t *testing.T) {
	cfg := IncusConfig{Executable: "/usr/bin/incus", Project: "cage-tenant", Profile: "default", ControlPath: "/home/cage/control.json", InstanceType: InstanceContainer, Vsock: true}
	if _, err := NewIncus(cfg); err == nil {
		t.Fatal("containers must not be offered the vsock path")
	}
	cfg.InstanceType = InstanceVM
	if _, err := NewIncus(cfg); err != nil {
		t.Fatalf("VM with vsock: %v", err)
	}
	// Even a runtime built by hand never goes direct for a container.
	runtime := &IncusRuntime{cfg: IncusConfig{InstanceType: InstanceContainer, Vsock: true}}
	if runtime.directAllowed("local:cage-x") {
		t.Fatal("container instances must always use exec")
	}
}
