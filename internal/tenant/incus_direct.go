package tenant

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The direct path.
//
// By default every connection to a workbench starts "incus exec ... socat" and
// relays its bytes through incusd and the guest agent. With IncusConfig.Vsock
// the gateway starts one long-lived socat per workspace through that same
// exec. It listens on a vsock port of the VM and forwards each connection to
// the workbench's loopback port, and the gateway dials the VM's vsock address
// directly: a new connection costs a socket connect instead of a process
// start, and the bytes bypass incusd.
//
// The path is an optimisation with three safeguards:
//
//   - the workspace's handshake runs over it, so it only stays in use after it
//     carried a real workbench exchange;
//   - a failed direct dial falls back to exec inside the same dial, so no
//     request or stream sees the failure;
//   - a path that failed once is never used again by that workspace, and the
//     instance is not tried again for directRetry, so a guest that cannot do
//     vsock costs one failed attempt rather than one per connection.
//
// Containers share the host's kernel and its vsock namespace, so a vsock
// listener in a container would be reachable from every other container:
// they always use exec.

// directRetry is how long an instance whose direct path failed stays on exec.
const directRetry = 5 * time.Minute

// directWait bounds how long a fresh bridge may take to start listening.
var directWait = 5 * time.Second

// vsockStream opens a connection to a port of a VM's vsock address.
type vsockStream func(ctx context.Context, cid, port uint32) (net.Conn, error)

func (r *IncusRuntime) dialVsock(ctx context.Context, cid, port uint32) (net.Conn, error) {
	if r.vsockDial != nil {
		return r.vsockDial(ctx, cid, port)
	}
	return dialVsockHost(ctx, cid, port)
}

func (r *IncusRuntime) dialExec(ctx context.Context, instance string, port int) (net.Conn, error) {
	if r.execDial != nil {
		return r.execDial(ctx, instance, port)
	}
	return r.dial(ctx, instance, port)
}

// directAllowed reports whether new workspaces of this instance should try
// the direct path.
func (r *IncusRuntime) directAllowed(instance string) bool {
	if !r.cfg.Vsock || r.instanceType() != InstanceVM {
		return false
	}
	until, skipped := r.directSkip.Load(instance)
	return !skipped || !time.Now().Before(until.(time.Time))
}

func (r *IncusRuntime) skipDirect(instance string) {
	r.directSkip.Store(instance, time.Now().Add(directRetry))
}

// guestDialer opens the connections of one workspace: over the direct path
// while it works, with exec as the fallback.
type guestDialer struct {
	runtime  *IncusRuntime
	instance string
	port     int
	direct   atomic.Pointer[directPath]
}

func (g *guestDialer) dial(ctx context.Context) (net.Conn, error) {
	if path := g.direct.Load(); path != nil && !path.dead.Load() {
		conn, err := path.dial(ctx)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		g.runtime.skipDirect(g.instance)
		path.fail(fmt.Errorf("dial: %w", err))
	}
	return g.runtime.dialExec(ctx, g.instance, g.port)
}

// directPath is the vsock endpoint of one workspace's workbench.
type directPath struct {
	runtime   *IncusRuntime
	instance  string
	cid, port uint32
	dead      atomic.Bool
	reported  sync.Once
}

func (p *directPath) dial(ctx context.Context) (net.Conn, error) {
	return p.runtime.dialVsock(ctx, p.cid, p.port)
}

// fail retires the path and says so once.
func (p *directPath) fail(reason error) {
	p.dead.Store(true)
	p.reported.Do(func() {
		log.Printf("cage-tenant: workspace %s: the vsock path ended (%v); new connections use incus exec", p.instance, reason)
	})
}

// openDirect prepares the vsock path of a workspace: it finds the VM's vsock
// address, reuses a listener that is already there or starts the bridge, and
// proves with a plain HTTP exchange that the listener reaches the workbench.
// The bridge ends with the workspace.
func (r *IncusRuntime) openDirect(ctx context.Context, workspace *Workspace, instance string, port int) (path *directPath, err error) {
	id := strings.TrimPrefix(instance, "local:cage-")
	cid, err := r.vsockID(ctx, id)
	if err != nil {
		return nil, err
	}
	path = &directPath{runtime: r, instance: instance, cid: cid, port: uint32(port)}
	if path.probe(ctx, port) == nil {
		// A bridge from an earlier gateway process is still listening.
		return path, nil
	}
	// A bridge that did not become usable ends at once, not with the workspace.
	bridge, stopBridge := context.WithCancel(workspace.ctx)
	defer func() {
		if err != nil {
			stopBridge()
		}
	}()
	exited, err := r.startBridge(bridge, instance, port)
	if err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, directWait)
	defer cancel()
	for {
		if path.probe(wait, port) == nil {
			go func() {
				<-exited
				path.fail(errors.New("the bridge process ended"))
			}()
			return path, nil
		}
		select {
		case <-exited:
			return nil, errors.New("the vsock bridge in the guest ended at once; its socat may lack vsock support")
		case <-wait.Done():
			return nil, errors.New("the vsock bridge in the guest did not start listening")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// probe sends one request through the path and accepts any HTTP answer: that
// proves the bytes reach an HTTP server on the workbench's port.
func (p *directPath) probe(ctx context.Context, port int) error {
	conn, err := p.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	request, err := http.NewRequest(http.MethodGet, "http://localhost:"+strconv.Itoa(port)+"/api/client/about", nil)
	if err != nil {
		return err
	}
	request.Close = true
	if err := request.Write(conn); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return err
	}
	return response.Body.Close()
}

// startBridge runs the guest's vsock-to-loopback relay through exec. The
// returned channel closes when the process ends; cancelling ctx stops it.
func (r *IncusRuntime) startBridge(ctx context.Context, instance string, port int) (<-chan struct{}, error) {
	command := r.command(ctx, "exec", instance, "--mode=non-interactive", "--", "/usr/bin/socat",
		fmt.Sprintf("VSOCK-LISTEN:%d,fork,reuseaddr", port), fmt.Sprintf("TCP4:127.0.0.1:%d,connect-timeout=5", port))
	// The incus client forwards SIGTERM to the process in the guest, which ends
	// the listener; the client is killed if it does not leave in time.
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	if err := command.Start(); err != nil {
		return nil, errors.New("the vsock bridge could not be started")
	}
	exited := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(exited)
	}()
	return exited, nil
}

// vsockID returns the VM's vsock context ID, which Incus keeps in the
// instance's volatile configuration.
func (r *IncusRuntime) vsockID(ctx context.Context, id string) (uint32, error) {
	body, err := captureIncus(r.localCommand(ctx, "query", "local:/1.0/instances/cage-"+id+"?project="+r.cfg.Project), 1<<20)
	if err != nil {
		return 0, err
	}
	var record instance
	if err := json.Unmarshal(body, &record); err != nil || record.Name != "cage-"+id || record.Type != InstanceVM || record.Config[instanceLabel] != id {
		return 0, errors.New("Incus instance ownership does not match the workspace")
	}
	// 0 to 2 are the hypervisor, local and host addresses.
	cid, err := strconv.ParseUint(record.Config["volatile.vsock_id"], 10, 32)
	if err != nil || cid < 3 {
		return 0, errors.New("the VM has no vsock address")
	}
	return uint32(cid), nil
}
