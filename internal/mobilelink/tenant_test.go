package mobilelink

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakePortalHost   = "example.com" // The httptest certificate covers example.com and *.example.com.
	fakePortal       = "https://" + fakePortalHost
	fakeUser         = "phone-user"
	fakePassword     = "phone-password-123"
	fakeUserID       = "0123456789abcdef0123456789abcdef"
	fakeVMID         = "fedcba9876543210fedcba9876543210"
	fakeTicket       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fakePortalCookie = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	fakeVMCookie     = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

// fakeGateway imitates the portal and one VM origin of cmd/cage-tenant closely
// enough to check what the phone sends: origins, cookies and retries.
type fakeGateway struct {
	t           *testing.T
	mu          sync.Mutex
	requests    []string
	starting    int  // Number of opens answered with 202 before the VM is ready.
	expireVM    int  // Number of VM reads answered with 401.
	expireLogin bool // Answer the next portal read with 401.
	loggedIn    bool
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, r.Method+" "+r.Host+r.URL.RequestURI())
	fail := func(message string) {
		g.t.Errorf("%s %s%s: %s", r.Method, r.Host, r.URL.Path, message)
		http.Error(w, message, http.StatusBadRequest)
	}
	if r.Header.Get("Origin") != "https://"+r.Host {
		fail("missing exact origin")
		return
	}
	reply := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	cookies := r.Cookies()
	only := func(name, value string) bool {
		return len(cookies) == 1 && cookies[0].Name == name && cookies[0].Value == value
	}
	vmHost := "vm-" + fakeVMID + "." + fakePortalHost
	switch r.Host {
	case fakePortalHost:
		if r.URL.Path == "/api/tenant/login" {
			var input struct{ Username, Password string }
			if len(cookies) != 0 || json.NewDecoder(r.Body).Decode(&input) != nil {
				fail("login reused a session")
				return
			}
			if input.Username != fakeUser || input.Password != fakePassword {
				reply(401, map[string]any{"error": map[string]string{"code": "invalid_credentials"}})
				return
			}
			g.loggedIn = true
			http.SetCookie(w, &http.Cookie{Name: tenantPortalCookie, Value: fakePortalCookie, Path: "/", Secure: true, HttpOnly: true})
			reply(200, map[string]any{"user": map[string]string{"id": fakeUserID, "username": fakeUser}})
			return
		}
		if !only(tenantPortalCookie, fakePortalCookie) || r.Header.Get("X-Cage-Expected-User") != fakeUserID {
			fail("expected only the portal cookie and the user ID")
			return
		}
		if g.expireLogin {
			g.expireLogin = false
			reply(401, map[string]any{"error": map[string]string{"code": "unauthenticated"}})
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/tenant/vms":
			reply(200, map[string]any{"vms": []map[string]string{{"id": fakeVMID, "name": "开发", "state": "ready"}, {"id": "bad", "name": "x"}}, "limit": 5})
		case "POST /api/tenant/vms":
			reply(201, map[string]any{"vm": map[string]string{"id": fakeVMID, "name": "开发", "state": "stopped"}})
		case "POST /api/tenant/vms/" + fakeVMID + "/open":
			if g.starting > 0 {
				g.starting--
				reply(202, map[string]any{"vm": map[string]string{"id": fakeVMID, "state": "starting"}})
				return
			}
			reply(200, map[string]any{"vm": map[string]string{"id": fakeVMID, "state": "ready"}, "workspace_url": "https://" + vmHost + "/_tenant/enter#" + fakeTicket})
		case "POST /api/tenant/logout":
			g.loggedIn = false
			w.WriteHeader(204)
		default:
			fail("unexpected portal route")
		}
	case vmHost:
		if r.URL.Path == "/_tenant/enter" {
			var input struct{ Ticket string }
			if len(cookies) != 0 || json.NewDecoder(r.Body).Decode(&input) != nil || input.Ticket != fakeTicket {
				fail("expected the ticket without other credentials")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: tenantVMCookie, Value: fakeVMCookie, Path: "/", Secure: true, HttpOnly: true})
			w.WriteHeader(204)
			return
		}
		if !only(tenantVMCookie, fakeVMCookie) {
			fail("expected only the VM cookie")
			return
		}
		if r.Method == http.MethodGet && g.expireVM > 0 {
			g.expireVM--
			reply(401, map[string]any{"error": map[string]string{"code": "unauthenticated"}})
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
			fail("expected JSON")
			return
		}
		body, _ := io.ReadAll(r.Body)
		reply(200, map[string]string{"path": r.URL.RequestURI(), "body": string(body)})
	default:
		fail("unexpected host")
	}
}

func (g *fakeGateway) take() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	requests := g.requests
	g.requests = nil
	return requests
}

func startFakeGateway(t *testing.T) (*fakeGateway, *http.Transport) {
	t.Helper()
	gateway := &fakeGateway{t: t}
	server := httptest.NewTLSServer(gateway)
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	dialer := net.Dialer{Timeout: time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != fakePortalHost+":443" && address != "vm-"+fakeVMID+"."+fakePortalHost+":443" {
			return nil, fmt.Errorf("unexpected address %q", address)
		}
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	return gateway, transport
}

func TestTenantPortalAddresses(t *testing.T) {
	for raw, want := range map[string]string{
		"https://cage.example.org":       "https://cage.example.org",
		"cage.example.org:9443":          "https://cage.example.org:9443",
		" HTTPS://Cage.Example.org:443/": "https://cage.example.org",
	} {
		if got, err := CanonicalTenantPortal(raw); err != nil || got != want {
			t.Errorf("CanonicalTenantPortal(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"http://cage.example.org", "https://localhost", "https://127.0.0.1", "https://cage.example.org/path",
		"https://user@cage.example.org", "https://cage.example.org?x=1", "https://cage.localhost", "https://cage.example.org:0"} {
		if _, err := CanonicalTenantPortal(raw); err == nil {
			t.Errorf("CanonicalTenantPortal(%q) accepted", raw)
		}
	}
}

func TestTenantAccountOpensWorkspaceAndForwardsOnlyPhoneOperations(t *testing.T) {
	gateway, transport := startFakeGateway(t)
	ctx := t.Context()
	if _, err := loginTenant(ctx, fakePortal, fakeUser, []byte("wrong-password-1"), transport); tenantCode(err) != "tenant_credentials" {
		t.Fatalf("wrong password: %v", err)
	}
	account, err := loginTenant(ctx, fakePortal, " Phone-User ", []byte(fakePassword), transport)
	if err != nil {
		t.Fatal(err)
	}
	vms, err := account.VMs(ctx)
	if err != nil || len(vms) != 1 || vms[0].ID != fakeVMID || vms[0].Name != "开发" {
		t.Fatalf("VMs = %+v, %v", vms, err)
	}
	if _, err := account.CreateVM(ctx, "  "); tenantCode(err) != "tenant_invalid_input" {
		t.Fatalf("empty VM name: %v", err)
	}
	if vm, err := account.CreateVM(ctx, "开发"); err != nil || vm.ID != fakeVMID {
		t.Fatalf("CreateVM = %+v, %v", vm, err)
	}
	gateway.mu.Lock()
	gateway.starting, gateway.expireLogin = 2, true
	gateway.mu.Unlock()
	gateway.take()
	workspace, err := account.OpenWorkspace(ctx, fakeVMID, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	open := "POST " + fakePortalHost + "/api/tenant/vms/" + fakeVMID + "/open"
	want := []string{open, "POST " + fakePortalHost + "/api/tenant/login", open, open, open,
		"POST vm-" + fakeVMID + "." + fakePortalHost + "/_tenant/enter"}
	if got := gateway.take(); !slices.Equal(got, want) {
		t.Fatalf("open requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	reply, err := workspace.Do(ctx, http.MethodGet, "/api/mobile/cli/projects", nil)
	if err != nil || reply.Status != 200 || !strings.Contains(string(reply.Body), "/api/mobile/cli/projects") {
		t.Fatalf("read = %+v, %v", reply, err)
	}
	reply, err = workspace.Do(ctx, http.MethodPost, "/api/mobile/cli/sessions", []byte(`{"workspace":"/w"}`))
	if err != nil || reply.Status != 200 || !strings.Contains(string(reply.Body), `workspace`) {
		t.Fatalf("write = %+v, %v", reply, err)
	}
	if _, err := workspace.Do(ctx, http.MethodGet, "/api/terminals", nil); err == nil {
		t.Fatal("a non-phone route was forwarded")
	}
	gateway.take()

	// An expired VM grant on a read enters again once; a write is not resent.
	gateway.mu.Lock()
	gateway.expireVM = 1
	gateway.mu.Unlock()
	if reply, err := workspace.Do(ctx, http.MethodGet, "/api/mobile/cli/sessions", nil); err != nil || reply.Status != 200 {
		t.Fatalf("read after expiry = %+v, %v", reply, err)
	}
	vmHost := "vm-" + fakeVMID + "." + fakePortalHost
	want = []string{"GET " + vmHost + "/api/mobile/cli/sessions", open, "POST " + vmHost + "/_tenant/enter", "GET " + vmHost + "/api/mobile/cli/sessions"}
	if got := gateway.take(); !slices.Equal(got, want) {
		t.Fatalf("recovery requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// While the VM restarts after idle cooling, a read reports it instead of waiting.
	gateway.mu.Lock()
	gateway.expireVM, gateway.starting = 1, 1
	gateway.mu.Unlock()
	if reply, err := workspace.Do(ctx, http.MethodGet, "/api/mobile/cli/projects", nil); err != nil || reply.Status != 503 || !strings.Contains(string(reply.Body), "启动") {
		t.Fatalf("read while starting = %+v, %v", reply, err)
	}
	account.Close()
	gateway.mu.Lock()
	loggedIn := gateway.loggedIn
	gateway.mu.Unlock()
	if loggedIn {
		t.Error("Close did not sign out of the portal")
	}
	if _, err := account.VMs(ctx); tenantCode(err) != "tenant_closed" {
		t.Errorf("VMs after Close: %v", err)
	}
}
