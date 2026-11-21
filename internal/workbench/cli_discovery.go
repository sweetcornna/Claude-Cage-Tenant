package workbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/klauspost/compress/gzip"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
)

//go:embed cli_discovery.py
var cliDiscoveryHelper string

var cliDiscoveryEncoded = func() string {
	var compressed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	_, _ = writer.Write([]byte(cliDiscoveryHelper))
	_ = writer.Close()
	return cliTransport85(compressed.Bytes())
}()

// residentRetry is how long the channel helper answers alone after the
// resident helper failed a request.
const residentRetry = time.Minute

// cliDiscovery keeps the session lists cheap. A list answer is always built
// from the host catalog, which create, archive, restore, rename and purge
// change in place; only the guest's observation of transcripts and processes
// costs a guest round trip, and concurrent requests share it.
type cliDiscovery struct {
	mu      sync.Mutex
	flights map[string]*discoveryFlight
	// resident is one long-lived helper process on Linux guests, started
	// through the login shell once instead of per request; see
	// cli_discovery.py. Only read-only requests use it.
	resident  guestSession
	installed bool      // the channel helper it loads is in the guest
	retryAt   time.Time // after a failure, no resident requests until then
	viewKeys  map[string]residentView
}

// residentView is what the last resident view of a session was read from.
type residentView struct {
	input map[string]any
	key   string
}

type discoveryFlight struct {
	started time.Time
	done    chan struct{}
	err     error
}

const discoveryTimeout = 20 * time.Second

// share runs discover, or waits for a run with the same key that started at
// or after arrival. A run that started earlier may have missed a change the
// caller has to see, so the caller waits for it to end and then shares or
// starts the next one. Concurrent callers therefore cost at most two runs,
// and every answer reflects an observation made after its request arrived.
func (d *cliDiscovery) share(ctx context.Context, key string, arrival time.Time, run func() (context.Context, context.CancelFunc), discover func(context.Context) error) error {
	for {
		d.mu.Lock()
		if d.flights == nil {
			d.flights = map[string]*discoveryFlight{}
		}
		flight := d.flights[key]
		if flight == nil {
			flight = &discoveryFlight{started: time.Now(), done: make(chan struct{})}
			d.flights[key] = flight
			go func() {
				runCtx, cancel := run()
				flight.err = discover(runCtx)
				cancel()
				d.mu.Lock()
				if d.flights[key] == flight {
					delete(d.flights, key)
				}
				d.mu.Unlock()
				close(flight.done)
			}()
		}
		d.mu.Unlock()
		select {
		case <-flight.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if !flight.started.Before(arrival) {
			return flight.err
		}
	}
}

// discoveryContext keeps the request's values but not its cancellation: the
// run is shared, and the first caller leaving must not fail the others.
func (s *Server) discoveryContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), discoveryTimeout)
	stop := context.AfterFunc(s.lifetimeContext(), cancel)
	return ctx, func() { stop(); cancel() }
}

// cliDiscoveryCommand starts the resident helper. It loads the channel helper
// that the ordinary bootstrap installed, and only if that file is intact.
func cliDiscoveryCommand() string {
	bootstrap := fmt.Sprintf(`import base64,gzip;exec(compile(gzip.decompress(base64.b85decode("%s")),"cli_discovery.py","exec"))`, cliDiscoveryEncoded)
	return shellescape.QuoteCommand([]string{"python3", "-I", "-u", "-c", bootstrap, cliChannelVersion, fmt.Sprintf("%x", sha256.Sum256([]byte(cliChannelInstalled)))})
}

func (s *Server) cliDiscoveryProcess(ctx context.Context) *exec.Cmd {
	return s.sshCommand(ctx, "-T", s.sshTarget(), "--", guestLoginCommand(cliDiscoveryCommand()))
}

// helperInstalled records that an ordinary channel request succeeded, so the
// versioned helper the resident process loads exists in the guest.
func (d *cliDiscovery) helperInstalled(s *Server) {
	if s.windowsGuest != nil || s.macosGuest != nil {
		return
	}
	d.mu.Lock()
	d.installed = true
	d.mu.Unlock()
}

func (d *cliDiscovery) usable() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.installed && !time.Now().Before(d.retryAt)
}

func (d *cliDiscovery) failed() {
	d.mu.Lock()
	d.retryAt = time.Now().Add(residentRetry)
	d.mu.Unlock()
	d.resident.close()
}

// residentRequest sends one request to the resident helper. It reports false
// when the caller should answer with the channel helper instead: the request
// may have been sent, so only idempotent reads may use it.
func (s *Server) residentRequest(ctx context.Context, mode string, input any) (json.RawMessage, bool) {
	if s.windowsGuest != nil || s.macosGuest != nil || !s.cliDiscovery.usable() {
		return nil, false
	}
	payload, err := json.Marshal(map[string]any{"mode": mode, "input": input})
	if err != nil {
		return nil, false
	}
	started := time.Now()
	reply, sent, err := s.cliDiscovery.resident.run(ctx, s.lifetimeContext(), s.cliDiscoveryProcess, string(payload))
	if !sent {
		return nil, false
	}
	if err == nil && reply.code == 0 && json.Valid(reply.stdout) {
		return reply.stdout, true
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil, false
	}
	// A stalled or failing helper must not delay the next requests.
	s.cliDiscovery.failed()
	_ = diagnostics.New(s.profile).Record("warn", "cli-discovery", fmt.Sprintf("operation=%s exit=%d elapsed_ms=%d error=%v output_bytes=%d", mode, reply.code, time.Since(started).Milliseconds(), err, len(reply.stdout)))
	return nil, false
}

// residentCLIChannel answers discover and view from the resident helper. The
// answer has the channel helper's own shape; false means it was not used.
func (s *Server) residentCLIChannel(ctx context.Context, mode string, input any, output any) bool {
	switch output := output.(type) {
	case *[]terminalConversation:
		if mode != "discover" {
			return false
		}
		reply, ok := s.residentRequest(ctx, mode, input)
		var answer struct {
			Hash     string                 `json:"hash"`
			Sessions []terminalConversation `json:"sessions"`
		}
		if !ok || json.Unmarshal(reply, &answer) != nil || answer.Hash == "" {
			return false
		}
		*output = answer.Sessions
		return true
	case *terminalConversation:
		if mode != "view" {
			return false
		}
		reply, ok := s.residentRequest(ctx, mode, input)
		var current terminalConversation
		if !ok || json.Unmarshal(reply, &current) != nil || current.WatchKey == "" {
			return false
		}
		*output = current
		return true
	}
	return false
}

// noteView keeps what the latest view of a session was read from; an empty
// key (an answer from the channel helper) forgets it.
func (d *cliDiscovery) noteView(id string, input map[string]any, key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if key == "" {
		delete(d.viewKeys, id)
		return
	}
	if d.viewKeys == nil {
		d.viewKeys = map[string]residentView{}
	}
	d.viewKeys[id] = residentView{input: input, key: key}
}
