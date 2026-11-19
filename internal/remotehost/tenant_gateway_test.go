package remotehost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sylvinus/agent-vm/internal/tenant"
)

const (
	tenantFixtureHost         = "example.com" // Covered by the httptest certificate, along with *.example.com.
	tenantFixtureOrigin       = "https://" + tenantFixtureHost
	tenantFixtureUser         = "fixture-user"
	tenantFixturePassword     = "fixture-password"
	tenantFixtureVMName       = "Fixture VM"
	tenantFixtureSystem       = "debian-12"
	tenantFixtureImage        = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tenantFixtureTicket       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tenantFixtureWorkspace    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tenantFixturePortalCookie = "__Host-cage-tenant"
	tenantFixtureVMCookie     = "__Host-cage-workspace"
)

// The real portal dispatches the start to this runtime. tenant.Workspace has no
// exported constructor, so the fixture supplies readiness and VM-side HTTP
// responses rather than starting Incus or constructing a workspace by hand.
type tenantFixtureRuntime chan tenant.LaunchSpec

func (runtime tenantFixtureRuntime) Prepare(ctx context.Context, spec tenant.LaunchSpec) (*tenant.Workspace, error) {
	select {
	case runtime <- spec:
	case <-ctx.Done():
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type tenantGatewayFixture struct {
	t        *testing.T
	portal   *tenant.Server
	mu       sync.Mutex
	vmID     string
	userID   string
	opened   bool
	polled   bool
	requests []string
}

func (f *tenantGatewayFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.Host+r.URL.Path)
	reject := func(message string) {
		f.t.Errorf("%s %s: %s", r.Method, r.URL.Path, message)
		http.Error(w, "unexpected fixture request", http.StatusBadRequest)
	}
	if r.TLS == nil || r.Header.Get("Origin") != "https://"+r.Host || r.URL.RawQuery != "" {
		reject("expected HTTPS and a same-origin request without a query")
		return
	}
	cookies := r.Cookies()
	hasOnlyCookie := func(name string) bool {
		return len(cookies) == 1 && cookies[0].Name == name
	}
	replyJSON := func(body any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
	switch r.Host {
	case tenantFixtureHost:
		switch r.URL.Path {
		case "/api/tenant/register", "/api/tenant/login":
			if len(cookies) != 0 || r.Header.Get("X-Cage-Expected-User") != "" {
				reject("registration or login reused an existing session")
				return
			}
		default:
			if !hasOnlyCookie(tenantFixturePortalCookie) || f.userID == "" || r.Header.Get("X-Cage-Expected-User") != f.userID {
				reject("expected only the portal cookie and the signed-in user ID")
				return
			}
		}

		if r.Method == http.MethodPost && r.URL.Path == "/api/tenant/vms/"+f.vmID+"/open" {
			if f.polled {
				replyJSON(map[string]string{"workspace_url": "https://vm-" + f.vmID + "." + tenantFixtureHost + "/_tenant/enter#" + tenantFixtureTicket})
				return
			}
			f.opened = true // The first open runs through the portal's actual start handler.
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/tenant/vms" && f.opened {
			if f.polled {
				replyJSON(map[string][]TenantVM{"vms": {{ID: f.vmID, State: "ready"}}})
				return
			}
			f.polled = true // The first poll sees the real portal's "starting" state.
		}
		f.portal.ServeHTTP(w, r)

	case "vm-" + f.vmID + "." + tenantFixtureHost:
		switch r.Method + " " + r.URL.Path {
		case "POST /_tenant/enter":
			var input struct {
				Ticket string `json:"ticket"`
			}
			if len(cookies) != 0 || r.Header.Get("X-Cage-Expected-User") != "" ||
				json.NewDecoder(r.Body).Decode(&input) != nil || input.Ticket != tenantFixtureTicket {
				reject("expected the workspace ticket without portal credentials")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: tenantFixtureVMCookie, Value: tenantFixtureWorkspace, Path: "/", Secure: true, HttpOnly: true})
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/client/about":
			if !hasOnlyCookie(tenantFixtureVMCookie) || cookies[0].Value != tenantFixtureWorkspace {
				reject("expected only the workspace cookie")
				return
			}
			_, _ = io.WriteString(w, "fixture workbench")
		default:
			reject("unexpected workspace route")
		}
	default:
		reject("unexpected host")
	}
}

func TestTenantGatewayHTTPSLoginCreateStartAndSession(t *testing.T) {
	runtime := make(tenantFixtureRuntime, 1)
	portal, err := tenant.NewServer(tenant.Config{
		Origin: tenantFixtureOrigin, EnablePreviewRegistration: true,
		StateDir: filepath.Join(t.TempDir(), "state"), Runtime: runtime,
		Systems: []tenant.System{tenant.DefaultSystem(tenantFixtureImage)},
		UI:      fstest.MapFS{}, AccountVMLimit: 1, TotalVMLimit: 1, ParallelStarts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(portal.Close)
	fixture := &tenantGatewayFixture{t: t, portal: portal}
	server := httptest.NewTLSServer(fixture)
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	// Keep the portal and workspace hostnames intact for TLS, cookies and
	// origin checks, but only allow connections to this loopback listener.
	dialer := net.Dialer{Timeout: time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		fixture.mu.Lock()
		workspace := "vm-" + fixture.vmID + "." + tenantFixtureHost + ":443"
		fixture.mu.Unlock()
		if network != "tcp" || (address != tenantFixtureHost+":443" && address != workspace) {
			return nil, fmt.Errorf("unexpected fixture address %q", address)
		}
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	origin, err := parseTenantPortal(tenantFixtureOrigin)
	if err != nil {
		t.Fatal(err)
	}
	registration := tenantLogin{portal: origin, client: &http.Client{Transport: transport, CheckRedirect: tenantRedirect}}
	if _, err := registration.request(ctx, tenantFixtureOrigin, http.MethodPost, "/api/tenant/register",
		map[string]string{"username": tenantFixtureUser, "password": tenantFixturePassword}, nil, http.StatusCreated); err != nil {
		t.Fatalf("register fixture account: %v", err)
	}

	login, err := loginTenant(ctx, origin, tenantFixtureUser, []byte(tenantFixturePassword), transport)
	if err != nil {
		t.Fatalf("log in to fixture portal: %v", err)
	}
	fixture.mu.Lock()
	fixture.userID = login.userID
	fixture.mu.Unlock()
	var created struct {
		VM TenantVM `json:"vm"`
	}
	_, err = login.request(ctx, tenantFixtureOrigin, http.MethodPost, "/api/tenant/vms",
		map[string]string{"name": tenantFixtureVMName, "system_id": tenantFixtureSystem}, &created, http.StatusCreated)
	login.logout()
	vm := created.VM
	if err != nil || !tenantVMID.MatchString(vm.ID) || vm.Name != tenantFixtureVMName || vm.SystemLabel != "Debian 12" || vm.State != "stopped" {
		t.Fatalf("create fixture VM: got %+v, err=%v", vm, err)
	}
	fixture.mu.Lock()
	fixture.vmID = vm.ID
	fixture.mu.Unlock()

	profile := HostProfile{Kind: TenantKind, Portal: tenantFixtureOrigin, VMID: vm.ID, User: tenantFixtureUser}
	secrets := ConnectSecrets{Passphrase: []byte(tenantFixturePassword)}
	var phases []string
	session, err := connectTenantWithTransport(ctx, profile, secrets, func(phase string) {
		phases = append(phases, phase)
	}, transport, time.Millisecond)
	if err != nil {
		t.Fatalf("connect to fixture workspace: %v", err)
	}
	defer session.Close()
	if want := []string{"tenant_login", "tenant_start", "tenant_enter"}; !slices.Equal(phases, want) {
		t.Errorf("connection phases = %v, want %v", phases, want)
	}
	select {
	case spec := <-runtime:
		if want := (tenant.LaunchSpec{VMID: vm.ID, SystemID: tenantFixtureSystem, Image: tenantFixtureImage}); spec != want {
			t.Errorf("runtime launch = %+v, want %+v", spec, want)
		}
	case <-ctx.Done():
		t.Fatal("portal did not dispatch the VM start to the runtime")
	}
	for _, b := range secrets.Passphrase {
		if b != 0 {
			t.Error("tenant connector did not clear the passphrase")
			break
		}
	}

	workspaceHost := "vm-" + vm.ID + "." + tenantFixtureHost
	if want := "https://" + workspaceHost; session.Origin != want {
		t.Fatalf("session origin = %q, want %q", session.Origin, want)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, session.Origin+"/api/client/about", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := session.Client.Do(req)
	if err != nil {
		t.Fatalf("access fixture workspace through Session: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "fixture workbench" {
		t.Fatalf("workspace response = %d %q, err=%v", resp.StatusCode, body, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if session.Context.Err() != context.Canceled {
		t.Error("closing the session did not cancel its context")
	}

	want := []string{
		"POST " + tenantFixtureHost + "/api/tenant/register",
		"POST " + tenantFixtureHost + "/api/tenant/login",
		"POST " + tenantFixtureHost + "/api/tenant/vms",
		"POST " + tenantFixtureHost + "/api/tenant/logout",
		"POST " + tenantFixtureHost + "/api/tenant/login",
		"POST " + tenantFixtureHost + "/api/tenant/vms/" + vm.ID + "/open",
		"GET " + tenantFixtureHost + "/api/tenant/vms",
		"GET " + tenantFixtureHost + "/api/tenant/vms",
		"POST " + tenantFixtureHost + "/api/tenant/vms/" + vm.ID + "/open",
		"POST " + workspaceHost + "/_tenant/enter",
		"GET " + workspaceHost + "/api/client/about",
		"POST " + tenantFixtureHost + "/api/tenant/logout",
	}
	fixture.mu.Lock()
	got := slices.Clone(fixture.requests)
	fixture.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Errorf("gateway requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
