package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/hostimage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"github.com/sylvinus/agent-vm/internal/remotehost"
)

type localVMFixture struct {
	endpoint *httptest.Server
	manifest remotehost.PublicManifest
	unlocked atomic.Bool
	connects atomic.Int32
	releases atomic.Int32
}

func newLocalVMFixture(t *testing.T, label string) *localVMFixture {
	t.Helper()
	f := &localVMFixture{manifest: remotehost.PublicManifest{APIProtocol: remoteAPIProtocol, InstanceID: strings.Repeat(label, 64), Platform: runtime.GOOS}}
	f.unlocked.Store(true)
	f.endpoint = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/account":
			_, _ = w.Write([]byte(`{"unlocked":` + map[bool]string{true: "true", false: "false"}[f.unlocked.Load()] + `}`))
		case "/api/account/suspend":
			f.unlocked.Store(false)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(f.endpoint.Close)
	return f
}

func (f *localVMFixture) Connect(ctx context.Context) (*remotehost.Session, error) {
	f.connects.Add(1)
	return &remotehost.Session{Origin: f.endpoint.URL, Client: f.endpoint.Client(), Transport: f.endpoint.Client().Transport, Context: ctx, Manifest: f.manifest}, nil
}

func (f *localVMFixture) Status(context.Context) (DesktopLocalStatus, error) {
	return DesktopLocalStatus{Available: true, Known: true, ServiceRunning: true}, nil
}

func (f *localVMFixture) Release(context.Context, DesktopLocalStatus, bool) error {
	f.releases.Add(1)
	return nil
}

func (f *localVMFixture) ReleaseOwnedIdle(context.Context, DesktopLocalStatus) (bool, error) {
	return false, nil
}

func attachLocalVMFixture(t *testing.T, d *desktopGateway, vmID string, local DesktopLocalController) *desktopTarget {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	session, err := local.Connect(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); cancel() })
	d.mu.Lock()
	target := &desktopTarget{id: d.generation, mode: "local", vmID: vmID, local: local, ctx: ctx, cancel: cancel, session: session}
	d.active = target
	d.mu.Unlock()
	return target
}

func localVMGateway(t *testing.T) (*Server, *desktopGateway, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	s := testServer(t)
	account, err := loadLocalAccount(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = account.create("Alice", "source-password", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(account.lock)
	local := newLocalVMFixture(t, "a")
	s.remote = newDesktopGateway(s.profile, local)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.remote.desktop.localVMs = newLocalVMStore(s.profile, exe)
	t.Cleanup(s.remote.close)
	attachLocalVMFixture(t, s.remote.desktop, defaultLocalVMID, local)
	return s, s.remote.desktop, home
}

func localVMRequest(t *testing.T, s *Server, method, path, generation, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, s.origin+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.origin)
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	if generation != "" {
		r.Header.Set(connectionHeader, generation)
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}

func TestLocalVMCreateProvisionsIndependentTrustedAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	root := filepath.Join(home, "profile")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := loadLocalAccount(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = source.create("Alice", "source-password", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.lock)
	if err = source.trustedLogin("enable", "source-password", mustTrustedLocalDir(t)); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := newLocalVMStore(root, exe)
	vm, err := store.create("开发")
	if err != nil {
		t.Fatal(err)
	}
	if vm.ID == "" || vm.Name != "开发" || vm.State != "stopped" {
		t.Fatalf("unexpected VM: %+v", vm)
	}
	if !strings.HasPrefix(vm.Dir, filepath.Join(root, "vms")+string(os.PathSeparator)) {
		t.Fatalf("VM outside the private profile root: %s", vm.Dir)
	}
	// Each VM keeps its own default workspace and storage record; no writable
	// disk, path or environment record is inherited from the default profile.
	defaultWorkspace, vmWorkspace := cage.EnvironmentDir(root), cage.EnvironmentDir(vm.Dir)
	if vmWorkspace == defaultWorkspace || !strings.Contains(vmWorkspace, vm.ID) {
		t.Fatalf("VM reused the default workspace: %q vs %q", vmWorkspace, defaultWorkspace)
	}
	if _, err := os.Lstat(filepath.Join(vm.Dir, "environment-storage.json")); !os.IsNotExist(err) {
		t.Fatal("VM inherited the default environment storage record")
	}
	owner, err := store.readOwner()
	if err != nil || owner == "" {
		t.Fatalf("owner record: %q %v", owner, err)
	}
	manifest, err := readLocalVMManifest(store.manifestPath(vm.ID))
	if err != nil || manifest.Owner != owner {
		t.Fatalf("manifest owner: %+v %v", manifest, err)
	}
	record, err := readLocalAccountRecord(vm.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if record.Username != "Alice" || record.Owner != owner || record.TrustedDevice == "" {
		t.Fatalf("provisioned account: %+v", record)
	}
	if record.TrustedDevice == source.record.TrustedUnlock.ID || record.TrustedUnlock != nil || record.Devices != nil {
		t.Fatal("new VM reused source trust or device grants")
	}
	// A real record round trip: the fresh trusted slot opens the VM account,
	// while the source password is not copied into the independent key.
	provisioned, err := loadLocalAccount(vm.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provisioned.lock)
	if err = provisioned.trustedLogin("resume", "", mustTrustedLocalDir(t)); err != nil || !provisioned.viewFor(true).Unlocked {
		t.Fatalf("trusted resume: %+v %v", provisioned.view(), err)
	}
	if provisioned.view().Username != "Alice" || !provisioned.view().TrustedLocal || !provisioned.view().LocalVM || source.view().LocalVM {
		t.Fatalf("account semantics changed: %+v", provisioned.view())
	}
	provisioned.lock()
	if err = provisioned.unlock("source-password"); err == nil {
		t.Fatal("source password unlocked the independent VM key")
	}
	// The source profile keeps its own account and trust record.
	if source.view().Username != "Alice" || source.record.TrustedUnlock == nil {
		t.Fatalf("source account changed: %+v", source.view())
	}
	// Revoking or changing the source account never revokes or accepts the
	// independent VM account: no source trust or password was copied.
	if err = source.trustedLogin("disable", "", mustTrustedLocalDir(t)); err != nil {
		t.Fatal(err)
	}
	if err = source.changePassword("source-password", "changed-password"); err != nil {
		t.Fatal(err)
	}
	if err = provisioned.trustedLogin("resume", "", mustTrustedLocalDir(t)); err != nil || !provisioned.viewFor(true).Unlocked {
		t.Fatalf("independent VM trust was revoked with the source: %v", err)
	}
	provisioned.lock()
	if err = provisioned.unlock("changed-password"); err == nil {
		t.Fatal("source password change reached the independent VM")
	}
	other, err := store.create("测试")
	if err != nil {
		t.Fatal(err)
	}
	seenPorts, seenAuth := make(map[int]bool), make(map[string]bool)
	for _, dir := range []string{root, vm.Dir, other.Dir} {
		proxy, err := managedproxy.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if seenPorts[proxy.Port] || seenAuth[proxy.Auth] || len(proxy.Auth) != 48 {
			t.Fatal("local VM reused a proxy endpoint or credentials")
		}
		seenPorts[proxy.Port], seenAuth[proxy.Auth] = true, true
	}
	// Duplicate names are refused before another directory is created.
	if _, err := store.create("开发"); err == nil {
		t.Fatal("duplicate VM name accepted")
	}
	if _, err := store.create(strings.Repeat("名", 65)); err == nil {
		t.Fatal("overlong VM name accepted")
	}
	if _, err := store.create("bad\x00name"); err == nil {
		t.Fatal("control character accepted")
	}
}

func mustTrustedLocalDir(t *testing.T) string {
	t.Helper()
	dir, err := trustedLocalDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLocalVMRoutesRequireExactLocalGenerationAndUnlockedOwner(t *testing.T) {
	s, d, _ := localVMGateway(t)
	generation := d.context().ConnectionID
	if !d.context().VirtualMachines {
		t.Fatal("local desktop did not advertise VM support")
	}
	w := localVMRequest(t, s, "GET", "/api/client/virtual-machines", generation, "")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		VMs       []localVM `json:"vms"`
		CurrentID string    `json:"current_id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.VMs) != 1 || list.VMs[0].ID != defaultLocalVMID || list.CurrentID != defaultLocalVMID {
		t.Fatalf("initial list: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), s.profile) || strings.Contains(w.Body.String(), "account") {
		t.Fatal("inventory leaked private paths or credentials")
	}
	if fixture := d.local.(*localVMFixture); fixture.connects.Load() != 1 {
		t.Fatalf("inventory probed a local controller: %d", fixture.connects.Load())
	}
	for _, request := range []struct{ generation, method, path, body string }{
		{"", "GET", "/api/client/virtual-machines", ""},
		{"stale", "GET", "/api/client/virtual-machines", ""},
		{"", "POST", "/api/client/virtual-machines", `{"name":"dev"}`},
		{"stale", "POST", "/api/client/virtual-machines", `{"name":"dev"}`},
	} {
		if got := localVMRequest(t, s, request.method, request.path, request.generation, request.body); got.Code != 409 {
			t.Fatalf("%s %s generation %q: %d %s", request.method, request.path, request.generation, got.Code, got.Body.String())
		}
	}
	w = localVMRequest(t, s, "POST", "/api/client/virtual-machines", generation, `{"name":"开发"}`)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		VM localVM `json:"vm"`
	}
	if json.Unmarshal(w.Body.Bytes(), &created) != nil || created.VM.ID == "" || created.VM.Name != "开发" || created.VM.State != "stopped" {
		t.Fatalf("create reply: %s", w.Body.String())
	}
	w = localVMRequest(t, s, "GET", "/api/client/virtual-machines", generation, "")
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.VMs) != 2 || list.CurrentID != defaultLocalVMID {
		t.Fatalf("list after create: %s", w.Body.String())
	}
	// A locked account and a remote selection never reach inventory or create.
	fixture := d.local.(*localVMFixture)
	fixture.unlocked.Store(false)
	if got := localVMRequest(t, s, "GET", "/api/client/virtual-machines", generation, ""); got.Code != http.StatusLocked {
		t.Fatalf("locked list: %d", got.Code)
	}
	fixture.unlocked.Store(true)
	d.mu.Lock()
	d.active.mode, d.active.vmID = "remote", ""
	d.generation = "remote-generation"
	d.mu.Unlock()
	if d.context().VirtualMachines {
		t.Fatal("remote context advertised local VM management")
	}
	if got := localVMRequest(t, s, "GET", "/api/client/virtual-machines", "remote-generation", ""); got.Code != 409 {
		t.Fatalf("remote list: %d", got.Code)
	}
}

func TestLocalVMOpenSwitchesGenerationWithoutStoppingOldVM(t *testing.T) {
	s, d, _ := localVMGateway(t)
	generation := d.context().ConnectionID
	w := localVMRequest(t, s, "POST", "/api/client/virtual-machines", generation, `{"name":"开发"}`)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		VM localVM `json:"vm"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	second := newLocalVMFixture(t, "b")
	defaultLocal := d.local.(*localVMFixture)
	d.localFor = func(id string) (DesktopLocalController, error) {
		if id == defaultLocalVMID {
			return defaultLocal, nil
		}
		return second, nil
	}
	if got := localVMRequest(t, s, "POST", "/api/client/virtual-machines/"+created.VM.ID+"/open", "stale", `{}`); got.Code != 409 {
		t.Fatalf("open with stale generation: %d %s", got.Code, got.Body.String())
	}
	w = localVMRequest(t, s, "POST", "/api/client/virtual-machines/"+created.VM.ID+"/open", generation, `{}`)
	if w.Code != 200 {
		t.Fatalf("open: %d %s", w.Code, w.Body.String())
	}
	var opened struct {
		CurrentID string `json:"current_id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &opened) != nil || opened.CurrentID != created.VM.ID {
		t.Fatalf("open reply: %s", w.Body.String())
	}
	if second.connects.Load() != 1 || defaultLocal.releases.Load() != 0 {
		t.Fatalf("switch connections=%d releases=%d", second.connects.Load(), defaultLocal.releases.Load())
	}
	contextNow := d.context()
	if contextNow.Mode != "local" || contextNow.Name != "开发" || contextNow.ConnectionID == generation {
		t.Fatalf("context after open: %+v", contextNow)
	}
	if got := localVMRequest(t, s, "GET", "/api/client/virtual-machines", contextNow.ConnectionID, ""); got.Code != 200 {
		t.Fatalf("list on new generation: %d", got.Code)
	}
	// Opening the default VM again must not release the background VM.
	w = localVMRequest(t, s, "POST", "/api/client/virtual-machines/"+defaultLocalVMID+"/open", contextNow.ConnectionID, `{}`)
	if w.Code != 200 || d.context().Name != "默认环境" || second.releases.Load() != 0 {
		t.Fatalf("reopen default: %d %s releases=%d", w.Code, w.Body.String(), second.releases.Load())
	}
}

func TestLocalVMRoutesAreLocalDesktopOnly(t *testing.T) {
	s := testServer(t)
	s.remote = newRemoteGateway(s.profile)
	defer s.remote.close()
	if got := request(s, "GET", "/api/client/virtual-machines", "", "", true); got.Code != http.StatusNotFound {
		t.Fatalf("non-desktop gateway exposed VM inventory: %d", got.Code)
	}
	_, err := AgentRequest(context.Background(), filepath.Join(t.TempDir(), "config.json"), AgentTarget{Scope: "desktop"}, "GET", "/api/client/virtual-machines", nil, "", "")
	var agentErr *AgentError
	if !errors.As(err, &agentErr) || agentErr.Code != "native_transport_required" {
		t.Fatalf("agent transport exported profile grants: %v", err)
	}
}

func TestLocalVMReusesSelectedGuestMedia(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude-cage")
	profile := filepath.Join(root, cage.LocalVMDir, "0123456789abcdef")
	if err := writePrivate(filepath.Join(profile, localVMManifest), localVMManifestRecord{Version: 1}); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(profile, "state", "guest-media", "debian-13")
	sourceCache := filepath.Join(root, "state", "guest-media", "debian-13")
	media := []byte("verified installation media")
	digest := sha256.Sum256(media)
	plan := hostimage.Plan{
		Host:    hostimage.Identity{System: "debian-13", Version: "13", Arch: "arm64"},
		Version: "13", Format: "qcow2", URL: "https://cloud.debian.org/image.qcow2",
		Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: int64(len(media)),
	}
	if err := os.MkdirAll(sourceCache, 0700); err != nil {
		t.Fatal(err)
	}
	image := plan.CachedPath(sourceCache)
	if err := os.WriteFile(image, media, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(image)
	if err != nil {
		t.Fatal(err)
	}
	source := plan
	source.Path, source.Modified = image, info.ModTime().UnixNano()
	if err = hostimage.Save(sourceCache, source); err != nil {
		t.Fatal(err)
	}
	other := plan
	other.Host.Arch = "amd64"
	reuseLocalVMInstallMedia(t.Context(), profile, cache, other)
	if _, err := os.Lstat(other.CachedPath(cache)); !os.IsNotExist(err) {
		t.Fatal("image for a different architecture was staged")
	}
	reuseLocalVMInstallMedia(t.Context(), profile, cache, plan)
	copied := plan.CachedPath(cache)
	cloned, err := os.Stat(copied)
	if os.IsNotExist(err) {
		t.Skip("copy-on-write file cloning is unavailable on this file system")
	}
	if err != nil || os.SameFile(info, cloned) {
		t.Fatalf("archive was not cloned into a separate file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "host-system-source.json")); !os.IsNotExist(err) {
		t.Fatal("profile-specific media metadata was copied")
	}
	prepared, err := hostimage.Prepare(t.Context(), cache, plan, nil, nil)
	if err != nil || prepared.Path != copied {
		t.Fatalf("installer did not validate the staged archive: %v", err)
	}
	if err = os.WriteFile(copied, []byte("modified VM copy"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(image); err != nil || string(data) != string(media) {
		t.Fatal("writing to the VM's copy changed the family archive")
	}
}
