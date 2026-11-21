package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// terminalPipeListener serves the workbench over unbuffered in-memory pipes,
// so a viewer that stops reading blocks the server's next write at once.
type terminalPipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *terminalPipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *terminalPipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *terminalPipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "workbench", Net: "unix"} }

func (l *terminalPipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.connections <- server:
		return client, nil
	case <-l.closed:
	case <-ctx.Done():
	}
	client.Close()
	server.Close()
	return nil, net.ErrClosed
}

type terminalStreamFixture struct {
	t        *testing.T
	server   *Server
	session  *terminalSession
	file     *controlledTerminal
	listener *terminalPipeListener
	release  func()
}

func newTerminalStreamFixture(t *testing.T) *terminalStreamFixture {
	t.Helper()
	s := testServer(t)
	s.origin = "http://workbench.test"
	session, file := newControlledSession()
	session.id = "resume"
	s.terminals.sessions = map[string]*terminalSession{session.id: session}
	var once sync.Once
	release := func() { once.Do(func() { close(file.waitRelease) }) }
	listener := &terminalPipeListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	httpServer := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() {
		release()
		s.terminals.close()
		_ = httpServer.Close()
	})
	return &terminalStreamFixture{t: t, server: s, session: session, file: file, listener: listener, release: release}
}

// write feeds the PTY in separate reads of at most size bytes and waits
// until the session has recorded all of it.
func (f *terminalStreamFixture) write(data []byte, size int) {
	f.t.Helper()
	f.session.mu.Lock()
	target := f.session.written + uint64(len(data))
	f.session.mu.Unlock()
	for len(data) > 0 {
		n := min(len(data), size)
		if _, err := f.file.writer.Write(data[:n]); err != nil {
			f.t.Fatal(err)
		}
		data = data[n:]
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.session.mu.Lock()
		written := f.session.written
		f.session.mu.Unlock()
		if written >= target {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("PTY output not recorded: %d of %d", written, target)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *terminalStreamFixture) dial(query string) *websocket.Conn {
	f.t.Helper()
	ws, err := f.dialErr(query)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = ws.Close() })
	return ws
}

func (f *terminalStreamFixture) dialErr(query string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{NetDialContext: f.listener.dial, HandshakeTimeout: 5 * time.Second}
	headers := http.Header{"Origin": {f.server.origin}, "Cookie": {f.server.cookieName() + "=" + f.server.session}}
	ws, response, err := dialer.Dial("ws://workbench.test/api/terminals/"+f.session.id+"/stream"+query, headers)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	return ws, err
}

type terminalMarker struct {
	Type    string  `json:"type"`
	Offset  *uint64 `json:"offset"`
	Reset   bool    `json:"reset"`
	Size    int     `json:"size"`
	Pending bool    `json:"pending"`
}

// terminalView rebuilds what a resuming viewer shows: the bytes since its
// last reset, their absolute start, and the offset it would resume after.
type terminalView struct {
	data    []byte
	start   uint64
	offset  uint64
	replay  int
	markers []terminalMarker
	types   []string
	pings   int
}

func (v *terminalView) frame(t *testing.T, kind int, data []byte) {
	t.Helper()
	if kind == websocket.BinaryMessage {
		v.data = append(v.data, data...)
		v.offset += uint64(len(data))
		v.replay -= min(v.replay, len(data))
		return
	}
	var marker terminalMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatalf("invalid terminal message %q: %v", data, err)
	}
	v.types = append(v.types, marker.Type)
	if marker.Type != "history" {
		return
	}
	if marker.Offset == nil {
		t.Fatalf("resume history marker without offset: %s", data)
	}
	v.markers = append(v.markers, marker)
	if marker.Reset || *marker.Offset != v.offset {
		v.data, v.start = nil, *marker.Offset
	}
	v.offset, v.replay = *marker.Offset, marker.Size
}

// read consumes frames until done reports true.
func (v *terminalView) read(t *testing.T, ws *websocket.Conn, done func() bool) {
	t.Helper()
	ws.SetPingHandler(func(data string) error {
		v.pings++
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	for !done() {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("stream ended: %v (offset=%d types=%v)", err, v.offset, v.types)
		}
		v.frame(t, kind, data)
	}
}

func (v *terminalView) saw(kind string) bool {
	for _, item := range v.types {
		if item == kind {
			return true
		}
	}
	return false
}

// terminalPattern is distinguishable output whose every byte position matters.
func terminalPattern(prefix string, size int) []byte {
	var out bytes.Buffer
	for line := 0; out.Len() < size; line++ {
		fmt.Fprintf(&out, "%s-%06d|", prefix, line)
	}
	return out.Bytes()[:size]
}

func TestTerminalStreamKeepsSlowViewerThroughBurstDuringReplay(t *testing.T) {
	f := newTerminalStreamFixture(t)
	history := terminalPattern("history", 500<<10)
	f.write(history, 16<<10)
	ws := f.dial("?after=0")
	view := &terminalView{}
	// The marker has been sent; the server is now blocked writing the replay
	// to a viewer that is not reading.
	view.read(t, ws, func() bool { return len(view.markers) == 1 })
	marker := view.markers[0]
	if *marker.Offset != 0 || marker.Reset || marker.Size != len(history) {
		t.Fatalf("unexpected replay marker: %+v", marker)
	}
	var burst bytes.Buffer
	for i := 0; i < 2000; i++ {
		chunk := []byte(fmt.Sprintf("live-%04d:%s\n", i, strings.Repeat("x", i%97)))
		burst.Write(chunk)
		// Each write is a separate PTY read, far beyond the old 64-chunk queue.
		f.write(chunk, len(chunk))
	}
	want := append(append([]byte(nil), history...), burst.Bytes()...)
	view.read(t, ws, func() bool { return view.offset >= uint64(len(want)) })
	if view.start != 0 || !bytes.Equal(view.data, want) || len(view.markers) != 1 {
		t.Fatalf("viewer bytes differ: start=%d got=%d want=%d markers=%d", view.start, len(view.data), len(want), len(view.markers))
	}
	if err := ws.WriteJSON(map[string]string{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	view.read(t, ws, func() bool { return view.saw("pong") })
	f.session.mu.Lock()
	viewers := len(f.session.clients)
	f.session.mu.Unlock()
	if viewers != 1 {
		t.Fatalf("slow viewer was detached: %d viewers", viewers)
	}
}

func TestTerminalStreamResumesAfterOffsetWithOnlyMissingBytes(t *testing.T) {
	f := newTerminalStreamFixture(t)
	first := terminalPattern("first", 40<<10)
	f.write(first, 4<<10)
	ws := f.dial("?after=0")
	view := &terminalView{}
	view.read(t, ws, func() bool { return view.offset == uint64(len(first)) && view.replay == 0 && len(view.markers) == 1 })
	resumeAt := view.offset
	_ = ws.Close()
	missed := terminalPattern("missed", 70<<10)
	f.write(missed, 16<<10)

	again := f.dial("?after=" + strconv.FormatUint(resumeAt, 10))
	resumed := &terminalView{offset: resumeAt}
	resumed.read(t, again, func() bool { return len(resumed.markers) == 1 && resumed.replay == 0 })
	marker := resumed.markers[0]
	if *marker.Offset != resumeAt || marker.Reset || marker.Size != len(missed) || !bytes.Equal(resumed.data, missed) {
		t.Fatalf("resume did not send exactly the missing bytes: marker=%+v bytes=%d", marker, len(resumed.data))
	}
	live := []byte("live after resume\r\n")
	f.write(live, len(live))
	resumed.read(t, again, func() bool { return resumed.offset == resumeAt+uint64(len(missed)+len(live)) })
	if !bytes.Equal(resumed.data, append(append([]byte(nil), missed...), live...)) {
		t.Fatalf("live output after resume differs: %q", resumed.data[len(missed):])
	}

	// A viewer that is already current receives an empty replay, not history.
	current := f.dial("?after=" + strconv.FormatUint(resumed.offset, 10))
	idle := &terminalView{offset: resumed.offset}
	idle.read(t, current, func() bool { return len(idle.markers) == 1 })
	if idle.markers[0].Size != 0 || idle.markers[0].Reset {
		t.Fatalf("current viewer was sent history: %+v", idle.markers[0])
	}
}

func TestTerminalStreamResetsWhenOffsetIsNoLongerKept(t *testing.T) {
	f := newTerminalStreamFixture(t)
	output := terminalPattern("trimmed", terminalHistoryLimit+100<<10)
	f.write(output, 16<<10)
	tail := output[len(output)-terminalHistoryLimit:]
	oldest := uint64(len(output) - terminalHistoryLimit)
	for _, after := range []uint64{0, 1, oldest - 1, uint64(len(output)) + 1} {
		ws := f.dial("?after=" + strconv.FormatUint(after, 10))
		view := &terminalView{offset: after}
		view.read(t, ws, func() bool { return len(view.markers) == 1 && view.replay == 0 })
		marker := view.markers[0]
		if !marker.Reset || *marker.Offset != oldest || marker.Size != len(tail) || !bytes.Equal(view.data, tail) {
			t.Fatalf("after=%d: expected reset to the kept tail, got %+v with %d bytes", after, marker, len(view.data))
		}
		_ = ws.Close()
	}
	// The oldest kept byte itself is still a continuation.
	ws := f.dial("?after=" + strconv.FormatUint(oldest, 10))
	view := &terminalView{offset: oldest}
	view.read(t, ws, func() bool { return len(view.markers) == 1 && view.replay == 0 })
	if view.markers[0].Reset || !bytes.Equal(view.data, tail) {
		t.Fatalf("resume at the oldest kept byte was reset: %+v", view.markers[0])
	}
}

func TestTerminalStreamRejectsMalformedOffsets(t *testing.T) {
	f := newTerminalStreamFixture(t)
	for _, query := range []string{"?after=", "?after=abc", "?after=-1", "?after=+1", "?after=01", "?after=1.5", "?after=0x10",
		"?after=18446744073709551616", "?after=99999999999999999999999", "?after=1&after=2", "?after=%201"} {
		w := request(f.server, "GET", "/api/terminals/resume/stream"+query, f.server.origin, "", true)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: HTTP %d", query, w.Code)
		}
	}
	ws := f.dial("?after=18446744073709551615")
	view := &terminalView{}
	view.read(t, ws, func() bool { return len(view.markers) == 1 })
	if !view.markers[0].Reset {
		t.Fatal("an offset past the output was not reset")
	}
}

func TestTerminalStreamResynchronisesOverflowingViewerWithoutClosing(t *testing.T) {
	budget := terminalViewerBudget
	terminalViewerBudget = 64 << 10
	t.Cleanup(func() { terminalViewerBudget = budget })
	f := newTerminalStreamFixture(t)
	f.write([]byte("prompt$ "), 64)
	ws := f.dial("?after=0")
	view := &terminalView{}
	view.read(t, ws, func() bool { return view.offset == 8 })
	// The viewer stops reading while far more than its budget is produced.
	flood := terminalPattern("flood", 1<<20)
	f.write(flood, 16<<10)
	total := uint64(8 + len(flood))
	view.read(t, ws, func() bool { return view.offset == total && view.replay == 0 })
	if len(view.markers) < 2 || !view.markers[len(view.markers)-1].Reset {
		t.Fatalf("overflow did not resynchronise with a reset: %+v", view.markers)
	}
	all := append([]byte("prompt$ "), flood...)
	if view.start < total-terminalHistoryLimit || !bytes.Equal(view.data, all[view.start:]) {
		t.Fatalf("resynchronised view differs from the kept output: start=%d bytes=%d", view.start, len(view.data))
	}
	live := []byte("still attached\r\n")
	f.write(live, len(live))
	view.read(t, ws, func() bool { return view.offset == total+uint64(len(live)) })
	if !bytes.HasSuffix(view.data, live) {
		t.Fatal("stream did not continue after resynchronisation")
	}
}

func TestTerminalStreamLegacyViewerReconnectsAfterOverflow(t *testing.T) {
	budget := terminalViewerBudget
	terminalViewerBudget = 64 << 10
	t.Cleanup(func() { terminalViewerBudget = budget })
	f := newTerminalStreamFixture(t)
	f.write([]byte("legacy"), 64)
	ws := f.dial("")
	var marker terminalMarker
	if err := ws.ReadJSON(&marker); err != nil || marker.Type != "history" {
		t.Fatalf("legacy history marker: %+v %v", marker, err)
	}
	if _, data, err := ws.ReadMessage(); err != nil || string(data) != "legacy" {
		t.Fatalf("legacy history: %q %v", data, err)
	}
	f.write(terminalPattern("flood", 1<<20), 16<<10)
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		// An original viewer is never sent a mid-stream history it cannot apply.
		if kind == websocket.TextMessage && strings.Contains(string(data), "history") {
			t.Fatalf("legacy viewer received a resynchronisation: %s", data)
		}
	}
}

func TestTerminalStreamAnswersPingAndSendsHeartbeats(t *testing.T) {
	interval := terminalHeartbeatInterval
	terminalHeartbeatInterval = 50 * time.Millisecond
	t.Cleanup(func() { terminalHeartbeatInterval = interval })
	f := newTerminalStreamFixture(t)
	for _, query := range []string{"?after=0", ""} {
		ws := f.dial(query)
		if err := ws.WriteJSON(map[string]string{"type": "ping"}); err != nil {
			t.Fatal(err)
		}
		view := &terminalView{}
		view.read(t, ws, func() bool { return view.saw("pong") && view.saw("heartbeat") && view.pings > 0 })
		_ = ws.Close()
	}
}

func TestStatusTerminalsRevisionFollowsTerminalList(t *testing.T) {
	s := testServer(t)
	revision := func() string {
		t.Helper()
		w := request(s, "GET", "/api/status", "", "", true)
		var view struct {
			Revision string `json:"terminals_revision"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Revision == "" {
			t.Fatalf("status without terminals revision: %d %s", w.Code, w.Body.String())
		}
		return view.Revision
	}
	empty := revision()
	first := &terminalSession{id: "a", title: "one", done: make(chan struct{})}
	second := &terminalSession{id: "b", title: "two", done: make(chan struct{})}
	s.terminals.sessions = map[string]*terminalSession{first.id: first, second.id: second}
	listed := revision()
	for range 20 {
		if revision() != listed {
			t.Fatal("revision depends on terminal order")
		}
	}
	if listed == empty {
		t.Fatal("revision ignored a new terminal")
	}
	close(second.done)
	if closed := revision(); closed == listed {
		t.Fatal("revision ignored a terminal that ended")
	}
}

func TestTerminalStreamReportsSessionExit(t *testing.T) {
	f := newTerminalStreamFixture(t)
	f.write([]byte("bye\r\n"), 64)
	ws := f.dial("?after=0")
	view := &terminalView{}
	view.read(t, ws, func() bool { return view.offset == 5 })
	_ = f.file.writer.Close()
	f.release()
	awaitTerminalSignal(t, f.session.done)
	view.read(t, ws, func() bool { return view.saw("exit") })
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := ws.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseNormalClosure {
		t.Fatalf("ended stream did not close normally: %v", err)
	}
	// A viewer that attaches to the ended terminal gets its output and the exit.
	again := f.dial("?after=0")
	ended := &terminalView{}
	ended.read(t, again, func() bool { return ended.saw("exit") })
	if string(ended.data) != "bye\r\n" {
		t.Fatalf("ended terminal history: %q", ended.data)
	}
}
