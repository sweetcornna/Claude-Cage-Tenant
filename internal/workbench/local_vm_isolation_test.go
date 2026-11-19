package workbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func writeLocalVMFixture(t *testing.T, family, id, name string) string {
	t.Helper()
	dir := filepath.Join(family, localVMDir, id)
	for _, path := range []string{filepath.Join(family, localVMDir), dir} {
		if err := host.EnsurePrivateDir(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := writePrivate(filepath.Join(dir, localVMManifest), localVMManifestRecord{Version: localVMVersion, Name: name, Owner: randomToken()}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func saveProxyPort(t *testing.T, profile string, port int) {
	t.Helper()
	store, err := managedproxy.Load(profile)
	if err != nil {
		t.Fatal(err)
	}
	store.Port = port
	if err = store.Save(profile); err != nil {
		t.Fatal(err)
	}
}

// Every VM keeps its own proxy store. Applying a node or a port in one VM
// never takes a port another VM of the same desktop owns.
func TestLocalVMProxyPortsStayWithOneVM(t *testing.T) {
	family := t.TempDir()
	saveProxyPort(t, family, 23001)
	first := writeLocalVMFixture(t, family, "0123456789abcdef", "开发")
	second := writeLocalVMFixture(t, family, "fedcba9876543210", "测试")
	saveProxyPort(t, second, 23002)
	s := testServer(t)
	for _, name := range []string{"config.json", "proxy"} {
		if err := os.Rename(filepath.Join(s.profile, name), filepath.Join(first, name)); err != nil {
			t.Fatal(err)
		}
	}
	s.profile, s.configPath = first, filepath.Join(first, "config.json")
	validate, start := validateProxyCore, startProxyCore
	t.Cleanup(func() { validateProxyCore, startProxyCore = validate, start })
	validateProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
	started := 0
	startProxyCore = func(context.Context, string, managedproxy.Store) error {
		started++
		return nil
	}
	original, _ := managedproxy.Load(first)
	for port, owner := range map[int]string{23001: "默认环境", 23002: "测试"} {
		err := s.changeProxy("restart", proxyInput{Port: port}, nil)
		if err == nil || !strings.Contains(err.Error(), owner) {
			t.Fatalf("port %d owned by %s: %v", port, owner, err)
		}
	}
	if current, _ := managedproxy.Load(first); started != 0 || current.Port != original.Port || current.Revision != original.Revision {
		t.Fatalf("refused port changed the VM proxy: started=%d store=%+v", started, current)
	}
	if err := s.changeProxy("restart", proxyInput{Port: 23003}, nil); err != nil {
		t.Fatal(err)
	}
	applied, _ := managedproxy.Load(first)
	cfg, err := loadConfig(s.configPath)
	if err != nil || started != 1 || applied.Port != 23003 || cfg.ProxyURL != applied.Endpoint() || cfg.ProxyRevision != applied.Revision {
		t.Fatalf("own port not applied: started=%d port=%d cfg=%+v err=%v", started, applied.Port, cfg, err)
	}
	for profile, port := range map[string]int{family: 23001, second: 23002} {
		if other, _ := managedproxy.Load(profile); other.Port != port {
			t.Fatalf("sibling %s proxy changed to %d", profile, other.Port)
		}
	}
	// A duplicate left by an older version never blocks switching nodes on
	// the endpoint this VM already applied; claiming another taken port is
	// still refused.
	saveProxyPort(t, second, 23003)
	if err := s.changeProxy("select", proxyInput{NodeID: applied.Selected}, nil); err != nil {
		t.Fatalf("node switch on the applied endpoint: %v", err)
	}
	if err := s.changeProxy("restart", proxyInput{Port: 23001}, nil); err == nil {
		t.Fatal("claimed the default VM's port")
	}
}

func TestUnconfiguredDefaultVMKeepsTheDefaultProxyPort(t *testing.T) {
	family := t.TempDir()
	vm := writeLocalVMFixture(t, family, "0123456789abcdef", "开发")
	saveProxyPort(t, vm, 23010)
	defaultPort, _ := managedproxy.Load(family)
	if owner, taken, err := proxyPortOwner(vm, defaultPort.Port); err != nil || !taken || owner != "默认环境" {
		t.Fatalf("default port owner = %q, %v, %v", owner, taken, err)
	}
	if owner, taken, err := proxyPortOwner(family, 23010); err != nil || !taken || owner != "开发" {
		t.Fatalf("VM port owner = %q, %v, %v", owner, taken, err)
	}
	if _, taken, err := proxyPortOwner(vm, 23010); err != nil || taken {
		t.Fatalf("a VM conflicted with its own port: %v", err)
	}
	// An added VM whose store is missing owns no port; it never blocks the
	// default VM from its default endpoint.
	writeLocalVMFixture(t, family, "fedcba9876543210", "测试")
	if owner, taken, err := proxyPortOwner(family, defaultPort.Port); err != nil || taken {
		t.Fatalf("storeless VM %q blocked the default port: %v", owner, err)
	}
}

// One VM cannot share another VM's private environment storage as a project,
// in either direction between the default VM and an added VM.
func TestLocalVMSharesExcludeSiblingStorage(t *testing.T) {
	family := t.TempDir()
	first := writeLocalVMFixture(t, family, "0123456789abcdef", "开发")
	second := writeLocalVMFixture(t, family, "fedcba9876543210", "测试")
	disk := t.TempDir()
	// The cage package's storage record for a custom storage directory.
	if err := writePrivate(filepath.Join(second, "environment-storage.json"), map[string]string{"directory": disk, "id": strings.Repeat("a", 32)}); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(disk, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ root, profile string }{{disk, first}, {nested, first}, {disk, family}, {second, first}} {
		if err := validateHostProjectData(request.root, request.profile); err == nil {
			t.Fatalf("profile %s shared %s", request.profile, request.root)
		}
	}
	project := t.TempDir()
	if err := validateHostProjectData(project, first); err != nil {
		t.Fatalf("unrelated project refused: %v", err)
	}
}
