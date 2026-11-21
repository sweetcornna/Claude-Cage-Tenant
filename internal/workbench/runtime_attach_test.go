//go:build !windows

package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
)

// cannedGuestSession answers every script on the shared control connection
// with the same output, as a guest would for the connection snapshot.
const cannedGuestSession = `import sys
out, inp = sys.stdout.buffer, sys.stdin.buffer
payload = sys.argv[1].encode()
out.write(b"CAGE-GUEST-SESSION 1\n")
out.flush()
while True:
    fields = inp.readline().split()
    if len(fields) != 3 or fields[0] != b"R":
        break
    inp.read(int(fields[2]))
    out.write(b"D %d 0 %d 0\n" % (int(fields[1]), len(payload)) + payload)
    out.flush()
`

func TestAttachReadsSnapshotOverSharedSessionAndSkipsSeparateHealth(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is unavailable")
	}
	s := transferMountServer(t, true, "current")
	manifest, err := json.Marshal(cage.RequiredGuestManifest())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := `{"token":"attach-token","display_scale":2}` + "\nvnc-password\n" + string(manifest) + "\n"
	ctx := t.Context()
	t.Cleanup(s.controlSession.close)
	// Open the shared connection as an earlier status poll would have.
	if _, sent, err := s.controlSession.run(ctx, ctx, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", "-I", "-u", "-c", cannedGuestSession, snapshot)
	}, "true"); err != nil || !sent {
		t.Fatalf("opening control connection: sent=%v err=%v", sent, err)
	}
	var mu sync.Mutex
	var requests []string
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer attach-token" {
			t.Errorf("guest request %s used %q", r.URL.Path, got)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
	}))
	if err := s.attach(); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !environmentReady(s) || s.credentials.Token != "attach-token" {
		t.Fatal("attach did not publish the guest connection")
	}
	mu.Lock()
	defer mu.Unlock()
	homepage := 0
	for _, request := range requests {
		switch request {
		case "GET /health":
			t.Fatalf("attach sent a separate health request: %v", requests)
		case "POST /homepage":
			homepage++
		}
	}
	if homepage != 1 {
		t.Fatalf("attach requests = %v, want one homepage update", requests)
	}
}

func TestRetryAttachStartsFastAndKeepsTheBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		var calls []time.Duration
		err := retryAttach(context.Background(), func() error {
			calls = append(calls, time.Since(started))
			if len(calls) == 3 {
				return nil
			}
			return errors.New("browser agent starting")
		})
		if err != nil || len(calls) != 3 {
			t.Fatalf("err=%v calls=%v", err, calls)
		}
		if calls[1] != 100*time.Millisecond || calls[2] != 300*time.Millisecond {
			t.Fatalf("retries at %v, want 100ms and 300ms", calls)
		}

		started, calls = time.Now(), nil
		failure := errors.New("never ready")
		if err := retryAttach(context.Background(), func() error {
			calls = append(calls, time.Since(started))
			return failure
		}); !errors.Is(err, failure) {
			t.Fatalf("exhausted retries returned %v", err)
		}
		last := calls[len(calls)-1]
		if last < attachRetryBudget || last > attachRetryBudget+time.Second {
			t.Fatalf("last attempt at %s, want the %s budget", last, attachRetryBudget)
		}

		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(150 * time.Millisecond); cancel() }()
		started = time.Now()
		if err := retryAttach(ctx, func() error { return failure }); !errors.Is(err, context.Canceled) || time.Since(started) > 200*time.Millisecond {
			t.Fatalf("cancelled retry returned %v after %s", err, time.Since(started))
		}
	})
}
