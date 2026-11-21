package workbench

import (
	"context"
	"sync"
	"time"
)

// cliDiscovery keeps the session lists cheap. A list answer is always built
// from the host catalog, which create, archive, restore, rename and purge
// change in place; only the guest's observation of transcripts and processes
// costs a guest round trip, and concurrent requests share it.
type cliDiscovery struct {
	mu      sync.Mutex
	flights map[string]*discoveryFlight
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
