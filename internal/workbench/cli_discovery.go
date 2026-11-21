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

// residentLifetime bounds one resident helper. After it, the helper answers
// residentRetired without reading the request, and a new one, started through
// a fresh login shell, takes over; changes to the login environment apply.
var residentLifetime = 10 * time.Minute

const residentRetired = 4

type residentOutcome int

const (
	residentUnavailable residentOutcome = iota // not sent: no helper or backing off
	residentAnswered
	residentLost   // the helper or its connection ended; the next request starts another
	residentFailed // the helper failed the request; backing off
)

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
	titles    map[string]string // the title discovery last gave each session
	// Held list requests share one guest watch, which runs only while at
	// least one of them waits.
	watchers  int
	stopWatch context.CancelFunc
	watching  bool          // the guest watch is answering
	changed   chan struct{} // closed when the watch saw the guest change
	changedAt time.Time
	lastInput map[string]any // the latest resident discovery's request
	lastHash  string         // and the guest's digest of its answer
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
	return shellescape.QuoteCommand([]string{"python3", "-I", "-u", "-c", bootstrap, cliChannelVersion, fmt.Sprintf("%x", sha256.Sum256([]byte(cliChannelInstalled))), fmt.Sprint(int(residentLifetime.Seconds()))})
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

// residentRequest sends one request to the resident helper. Anything but
// residentAnswered means the caller answers with the channel helper instead;
// the request may have reached the guest, so only idempotent reads use it.
func (s *Server) residentRequest(ctx context.Context, mode string, input any) (json.RawMessage, residentOutcome) {
	if s.windowsGuest != nil || s.macosGuest != nil || !s.cliDiscovery.usable() {
		return nil, residentUnavailable
	}
	payload, err := json.Marshal(map[string]any{"mode": mode, "input": input})
	if err != nil {
		return nil, residentUnavailable
	}
	started := time.Now()
	for attempt := 0; attempt < 2; attempt++ {
		reply, sent, err := s.cliDiscovery.resident.run(ctx, s.lifetimeContext(), s.cliDiscoveryProcess, string(payload))
		switch {
		case !sent:
			return nil, residentUnavailable
		case err == nil && reply.code == residentRetired:
			// It read nothing; the next attempt starts its successor.
			s.cliDiscovery.resident.close()
			continue
		case err == nil && reply.code == 0 && json.Valid(reply.stdout):
			return reply.stdout, residentAnswered
		case ctx.Err() != nil && errors.Is(err, context.Canceled):
			return nil, residentLost // the caller left
		case errors.Is(err, errGuestSessionEnded):
			return nil, residentLost
		}
		// A stalled or failing helper must not delay the next requests.
		s.cliDiscovery.failed()
		_ = diagnostics.New(s.profile).Record("warn", "cli-discovery", fmt.Sprintf("operation=%s exit=%d elapsed_ms=%d error=%v output_bytes=%d", mode, reply.code, time.Since(started).Milliseconds(), err, len(reply.stdout)))
		return nil, residentFailed
	}
	return nil, residentLost
}

// residentCLIChannel answers discover and view from the resident helper. The
// answer has the channel helper's own shape; false means it was not used.
func (s *Server) residentCLIChannel(ctx context.Context, mode string, input any, output any) bool {
	switch output := output.(type) {
	case *[]terminalConversation:
		if mode != "discover" {
			return false
		}
		reply, outcome := s.residentRequest(ctx, mode, input)
		var answer struct {
			Hash     string                 `json:"hash"`
			Sessions []terminalConversation `json:"sessions"`
		}
		if outcome != residentAnswered || json.Unmarshal(reply, &answer) != nil || answer.Hash == "" {
			return false
		}
		*output = answer.Sessions
		s.cliDiscovery.observed(input, answer.Hash)
		return true
	case *terminalConversation:
		if mode != "view" {
			return false
		}
		reply, outcome := s.residentRequest(ctx, mode, input)
		var current terminalConversation
		if outcome != residentAnswered || json.Unmarshal(reply, &current) != nil || current.WatchKey == "" {
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

const (
	// guestWatchTimeout bounds one watch request; the guest answers at once
	// when its discovery differs from the last one the host saw.
	guestWatchTimeout = 20 * time.Second
	guestWatchRetry   = 5 * time.Second
)

func (d *cliDiscovery) observed(input any, hash string) {
	request, ok := input.(map[string]any)
	if !ok {
		return
	}
	d.mu.Lock()
	d.lastInput, d.lastHash = request, hash
	d.mu.Unlock()
}

// subscribe registers a held list request. changes returns a channel that is
// closed when the guest's sessions change, with the time of that change;
// watching reports whether the guest watch is answering, so the caller knows
// whether it must look at the guest itself.
func (s *Server) subscribeGuestChanges() (changes func() (<-chan struct{}, time.Time, bool), stop func()) {
	d := &s.cliDiscovery
	d.mu.Lock()
	if d.changed == nil {
		d.changed = make(chan struct{})
	}
	d.watchers++
	if d.watchers == 1 {
		ctx, cancel := context.WithCancel(s.lifetimeContext())
		d.stopWatch = cancel
		go s.watchGuestSessions(ctx)
	}
	d.mu.Unlock()
	changes = func() (<-chan struct{}, time.Time, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.changed, d.changedAt, d.watching
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.watchers--
			if d.watchers == 0 && d.stopWatch != nil {
				d.stopWatch()
				d.stopWatch, d.watching = nil, false
			}
		})
	}
	return changes, stop
}

// watchGuestSessions asks the resident helper to answer once its discovery
// differs from the last one the host saw, and wakes the held requests.
func (s *Server) watchGuestSessions(ctx context.Context) {
	d := &s.cliDiscovery
	for ctx.Err() == nil {
		d.mu.Lock()
		input, known := d.lastInput, d.lastHash
		d.mu.Unlock()
		epoch, unlocked := s.account.access()
		var reply json.RawMessage
		outcome := residentUnavailable
		if input != nil && unlocked {
			request := make(map[string]any, len(input)+2)
			for key, value := range input {
				request[key] = value
			}
			request["known"], request["timeout"] = known, guestWatchTimeout.Seconds()
			d.setWatching(true)
			watchCtx, cancel := context.WithTimeout(ctx, guestWatchTimeout+5*time.Second)
			stop := context.AfterFunc(epoch, cancel)
			reply, outcome = s.residentRequest(watchCtx, "watch", request)
			stop()
			cancel()
		}
		var answer struct {
			Hash    string `json:"hash"`
			Changed bool   `json:"changed"`
		}
		if outcome != residentAnswered || json.Unmarshal(reply, &answer) != nil || answer.Hash == "" {
			pause := guestWatchRetry
			if outcome == residentLost {
				pause = 250 * time.Millisecond // a successor starts on the next request
			} else {
				d.setWatching(false)
			}
			select {
			case <-ctx.Done():
			case <-time.After(pause):
			}
			continue
		}
		if answer.Changed {
			d.mu.Lock()
			if d.lastHash == known {
				d.lastHash = answer.Hash
			}
			close(d.changed)
			d.changed, d.changedAt = make(chan struct{}), time.Now()
			d.mu.Unlock()
		}
	}
}

func (d *cliDiscovery) setWatching(value bool) {
	d.mu.Lock()
	if d.stopWatch != nil {
		d.watching = value
	}
	d.mu.Unlock()
}

// awaitSessionView waits until a held view may have changed: the resident
// helper answers when the files and processes the last view was read from
// change; other sessions are looked at again at the client's own pace.
func (s *Server) awaitSessionView(ctx context.Context, view mobileSession, deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	pause := time.Second
	if !view.Active {
		pause = 4 * time.Second
	}
	if !view.TerminalAvailable && view.Provider != codexProvider {
		pause = 250 * time.Millisecond // host-run sessions change in this process
	} else if view.TerminalAvailable {
		s.cliDiscovery.mu.Lock()
		last, ok := s.cliDiscovery.viewKeys[view.ID]
		s.cliDiscovery.mu.Unlock()
		if epoch, unlocked := s.account.access(); ok && unlocked {
			request := make(map[string]any, len(last.input)+2)
			for key, value := range last.input {
				request[key] = value
			}
			request["key"], request["timeout"] = last.key, remaining.Seconds()
			waitCtx, cancel := context.WithTimeout(ctx, remaining+5*time.Second)
			stop := context.AfterFunc(epoch, cancel)
			_, outcome := s.residentRequest(waitCtx, "view-wait", request)
			stop()
			cancel()
			if outcome == residentAnswered || ctx.Err() != nil {
				return ctx.Err() == nil
			}
		}
	}
	timer := time.NewTimer(min(pause, remaining))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// named records the title discovery gave a session. The session read derives
// its own title from the same transcript differently (slash commands, long
// histories); keeping discovery's avoids the two rewriting each other and
// waking every held list and read in turn.
func (d *cliDiscovery) named(id, title string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.titles == nil {
		d.titles = map[string]string{}
	}
	d.titles[id] = title
}

func (d *cliDiscovery) discoveredTitle(id, title string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	named, ok := d.titles[id]
	return ok && named == title
}
