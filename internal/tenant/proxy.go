package tenant

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const enterHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Cage Workspace</title><script defer src="/_tenant/enter.js"></script></head>
<body><p id="status" role="status">Opening workspace…</p></body></html>`

const enterJS = `"use strict";
const ticket = location.hash.slice(1);
history.replaceState(null, "", location.pathname);
(async () => {
  if (/^[a-f0-9]{64}$/.test(ticket)) {
    const response = await fetch("/_tenant/enter", {
      method: "POST", credentials: "same-origin", cache: "no-store",
      redirect: "error", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ticket })
    });
    if (response.status === 204) { location.replace("/"); return; }
  }
  throw new Error();
})().catch(() => { document.getElementById("status").textContent = "Workspace link expired or unavailable. Open it again from the portal."; });`

func validPath(target *url.URL) bool {
	value := target.Path
	escaped := strings.ToLower(target.EscapedPath())
	return strings.HasPrefix(value, "/") && (path.Clean(value) == value || (value != "//" && path.Clean(value)+"/" == value)) &&
		!strings.Contains(value, `\`) && !strings.Contains(escaped, "%2f") && strings.IndexFunc(value, unicode.IsControl) < 0
}

func internalPath(value string) bool {
	for _, prefix := range []string{"/_tenant", "/api/tenant", "/api/launch", "/api/session", "/api/handoff", "/api/desktop-local", "/api/desktop-owner", "/api/remote/manifest", "/api/agent/native/window", "/api/account/device-enroll", "/api/account/device-unlock", "/api/account/device-revoke"} {
		if value == prefix || strings.HasPrefix(value, prefix+"/") {
			return true
		}
	}
	return false
}

func (s *Server) workbench(w http.ResponseWriter, r *http.Request, id string) {
	origin := s.site.vmOrigin(id)
	wsScheme := "wss"
	if s.cfg.Development {
		wsScheme = "ws"
	}
	ancestors := "; frame-ancestors " + s.site.origin
	if r.URL.Path == "/_tenant/enter" || r.URL.Path == "/_tenant/enter.js" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'"+ancestors)
		s.entry(w, r, id, origin)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' "+wsScheme+"://"+r.Host+"; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; worker-src 'none'; object-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'self'"+ancestors)
	upgrade := len(r.Header.Values("Upgrade")) != 0
	if !ownSource(r, origin, !safeMethod(r.Method) || upgrade) {
		refuseOrigin(w)
		return
	}
	if (upgrade && (r.Method != http.MethodGet || len(r.Header.Values("Upgrade")) != 1 || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket"))) || r.Method == http.MethodTrace || internalPath(r.URL.Path) {
		notFound(w)
		return
	}
	s.mu.Lock()
	grant := s.grants[cookieValue(r, s.site.vmCookie)]
	if !s.validGrant(grant, id, s.now()) {
		s.mu.Unlock()
		unauthenticated(w)
		return
	}
	workspace := s.workspaces[id]
	available := workspace != nil && !workspace.checkedAt.IsZero()
	s.mu.Unlock()
	if !available {
		unavailable(w)
		return
	}
	select {
	case workspace.slots <- struct{}{}:
		defer func() { <-workspace.slots }()
	default:
		rateLimited(w)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stopLogin := context.AfterFunc(grant.login.ctx, cancel)
	defer stopLogin()
	stopWorkspace := context.AfterFunc(workspace.ctx, cancel)
	defer stopWorkspace()
	if grant.login.ctx.Err() != nil || workspace.ctx.Err() != nil || ctx.Err() != nil {
		unauthenticated(w)
		return
	}
	writer := &revocableWriter{ResponseWriter: w, ctx: ctx}
	finished := make(chan struct{})
	stopWrite := context.AfterFunc(ctx, func() {
		defer close(finished)
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stopWrite() {
			<-finished
		}
		if writer.stopHijack != nil {
			writer.stopHijack()
		}
	}()
	workspace.proxy(origin, func() { s.invalidateWorkspace(id, workspace) }, func() {
		s.confirmWorkspaceFault(id, workspace)
	}).ServeHTTP(writer, r.WithContext(ctx))
}

func (s *Server) validGrant(grant vmGrant, id string, now time.Time) bool {
	return grant.vmID == id && s.live(grant.login, now) && s.store.VMs[id].Owner == grant.login.user.ID
}

func (s *Server) entry(w http.ResponseWriter, r *http.Request, id, origin string) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		invalidInput(w)
		return
	}
	if safeMethod(r.Method) {
		body, contentType := enterHTML, "text/html; charset=utf-8"
		if r.URL.Path == "/_tenant/enter.js" {
			body, contentType = enterJS, "text/javascript; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, body)
		}
		return
	}
	if r.URL.Path != "/_tenant/enter" || r.Method != http.MethodPost {
		notFound(w)
		return
	}
	if !ownSource(r, origin, true) {
		refuseOrigin(w)
		return
	}
	var input struct {
		Ticket string `json:"ticket"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.allowRate("enter", s.clientIP(r), 120, now) {
		rateLimited(w)
		return
	}
	ticket, exists := s.tickets[input.Ticket]
	if !exists || !now.Before(ticket.expires) || !s.validGrant(ticket.grant, id, now) {
		unauthenticated(w)
		return
	}
	if workspace := s.workspaces[id]; workspace == nil || workspace.checkedAt.IsZero() {
		unavailable(w)
		return
	}
	s.deleteTicket(input.Ticket, ticket)
	session := ticket.grant.login
	token := session.grants[id]
	if token == "" {
		token = secret(32)
		session.grants[id] = token
		s.grants[token] = ticket.grant
	}
	s.setCookie(w, s.site.vmCookie, token, session.expires)
	w.WriteHeader(http.StatusNoContent)
}

type revocableWriter struct {
	http.ResponseWriter
	ctx        context.Context
	stopHijack func() bool
}

func (w *revocableWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *revocableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffer, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.stopHijack = context.AfterFunc(w.ctx, func() { _ = conn.Close() })
	}
	return conn, buffer, err
}

var requestHeaders = []string{
	"Accept", "Accept-Language", "Content-Type", "Content-Encoding", "Range", "If-Match", "If-None-Match", "If-Range",
	"If-Modified-Since", "If-Unmodified-Since", "Last-Event-ID", "Connection", "Upgrade",
	"Sec-WebSocket-Extensions", "Sec-WebSocket-Key", "Sec-WebSocket-Protocol", "Sec-WebSocket-Version",
}

var responseHeaders = []string{
	"Content-Type", "Content-Encoding", "Content-Length", "Content-Disposition", "Accept-Ranges", "Content-Range",
	"Location", "Allow", "Retry-After", "X-Cage-Recovery", "X-Cage-Clipboard-Status", "X-Cage-Metadata-Warning",
}

func selectHeaders(src http.Header, names ...string) http.Header {
	result := make(http.Header)
	for _, name := range names {
		for _, value := range src.Values(name) {
			result.Add(name, value)
		}
	}
	return result
}

func (workspace *Workspace) proxy(origin string, fault, suspect func()) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:     proxyTransport{RoundTripper: workspace.transport, fault: fault, suspect: suspect},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler:  func(w http.ResponseWriter, _ *http.Request, _ error) { unavailable(w) },
		Rewrite: func(req *httputil.ProxyRequest) {
			req.Out.URL.Scheme, req.Out.URL.Host = workspace.origin.Scheme, workspace.origin.Host
			req.Out.Host = workspace.origin.Host
			req.Out.Header = selectHeaders(req.Out.Header, requestHeaders...)
			req.Out.Trailer = nil
			req.Out.Header.Set("Origin", workspace.origin.String())
			req.Out.Header.Set("User-Agent", "")
			req.Out.AddCookie(workspace.cookie)
		},
		ModifyResponse: func(response *http.Response) error {
			if response.StatusCode == http.StatusUnauthorized && strings.HasPrefix(response.Request.URL.Path, "/api/") {
				workspace.confirmSessionFailure(response.Request, fault)
			}
			headers := selectHeaders(response.Header, responseHeaders...)
			if response.StatusCode == http.StatusSwitchingProtocols {
				if !strings.EqualFold(response.Request.Header.Get("Upgrade"), "websocket") || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
					return errors.New("workspace protocol upgrade refused")
				}
				headers = selectHeaders(response.Header, "Connection", "Upgrade", "Sec-WebSocket-Accept", "Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions")
			}
			if values := headers.Values("Location"); len(values) > 0 {
				location, err := url.Parse(values[0])
				if len(values) != 1 || err != nil {
					return errors.New("invalid workspace redirect")
				}
				location = response.Request.URL.ResolveReference(location)
				if location.Scheme != workspace.origin.Scheme || location.Host != workspace.origin.Host || location.User != nil || !validPath(location) || internalPath(location.Path) {
					return errors.New("workspace redirect refused")
				}
				browserOrigin, _ := url.Parse(origin)
				location.Scheme, location.Host = browserOrigin.Scheme, browserOrigin.Host
				headers.Set("Location", location.String())
			}
			response.Header = headers
			return nil
		},
	}
}

func (workspace *Workspace) confirmSessionFailure(request *http.Request, fault func()) {
	if request.URL.Path == "/api/client/about" {
		if request.Context().Err() == nil {
			fault()
		}
		return
	}
	if !workspace.checkingSession.CompareAndSwap(false, true) {
		return
	}
	defer workspace.checkingSession.Store(false)
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
	defer cancel()
	err := workspace.check(ctx)
	if ctx.Err() == nil && (errors.Is(err, errControllerSession) || connectionFailure(err)) {
		fault()
	}
}

type proxyTransport struct {
	http.RoundTripper
	fault   func()
	suspect func()
}

type withoutTrace struct{ context.Context }

func (withoutTrace) Value(any) any { return nil }

func (transport proxyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	outbound := request.WithContext(withoutTrace{request.Context()})
	if outbound.Body != nil && outbound.Body != http.NoBody {
		outbound.Body = clientBody{outbound.Body}
	}
	response, err := transport.RoundTripper.RoundTrip(outbound)
	if err != nil {
		if request.Context().Err() == nil {
			var clientErr clientReadError
			switch {
			case connectionFailure(err):
				transport.fault()
			case !errors.As(err, &clientErr) && isTimeout(err):
				transport.suspect()
			}
		}
		return nil, err
	}
	forward := *response
	forward.Trailer = nil
	if response.StatusCode == http.StatusSwitchingProtocols {
		if stream, ok := response.Body.(io.ReadWriteCloser); ok {
			forward.Body = guestSocket{ReadWriteCloser: stream, ctx: request.Context(), suspect: transport.suspect}
		}
	} else {
		forward.Body = guestBody{ReadCloser: response.Body, ctx: request.Context(), fault: transport.fault}
	}
	return &forward, nil
}

type clientBody struct{ io.ReadCloser }
type clientReadError struct{ error }

func (body clientBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if err != nil && err != io.EOF {
		return count, clientReadError{err}
	}
	return count, err
}

type guestBody struct {
	io.ReadCloser
	ctx   context.Context
	fault func()
}

func (body guestBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if err != io.EOF && body.ctx.Err() == nil && connectionFailure(err) {
		body.fault()
	}
	return count, err
}

type guestSocket struct {
	io.ReadWriteCloser
	ctx     context.Context
	suspect func()
}

func (socket guestSocket) Read(buffer []byte) (int, error) {
	count, err := socket.ReadWriteCloser.Read(buffer)
	if err != nil && socket.ctx.Err() == nil {
		// EOF also occurs during an ordinary WebSocket close. Confirm through HTTP.
		socket.suspect()
	}
	return count, err
}

func (socket guestSocket) Write(buffer []byte) (int, error) {
	count, err := socket.ReadWriteCloser.Write(buffer)
	if err != nil && socket.ctx.Err() == nil {
		socket.suspect()
	}
	return count, err
}

func (socket guestSocket) CloseWrite() error {
	if stream, ok := socket.ReadWriteCloser.(interface{ CloseWrite() error }); ok {
		return stream.CloseWrite()
	}
	return socket.Close()
}

func isTimeout(err error) bool {
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func connectionFailure(err error) bool {
	var clientErr clientReadError
	return !errors.As(err, &clientErr) && (errors.Is(err, errController) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE))
}
