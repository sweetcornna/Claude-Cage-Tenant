package workbench

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"github.com/sylvinus/agent-vm/internal/paths"
)

// Every VM of a desktop family keeps its own proxy store: subscriptions,
// nodes, route, exit bindings, credentials and loopback endpoint. The cores
// share the host loopback, so one port belongs to exactly one VM, also while
// that VM is stopped.

// initLocalVMProxy assigns a stable loopback endpoint before the profile can
// start. A stopped sibling's port still belongs to that sibling. The proxy
// itself checks the bind again at startup in case another program took it.
func initLocalVMProxy(profile string, vms []localVM) error {
	store, err := managedproxy.Load(profile)
	if err != nil {
		return err
	}
	reserved := map[int]bool{store.Port: true}
	for _, vm := range vms {
		if existing, err := managedproxy.Load(vm.Dir); err == nil {
			reserved[existing.Port] = true
		}
	}
	// Also reserve VMs this owner does not list, such as a copied directory.
	family, err := cage.LocalVMProfiles(profile)
	if err != nil {
		return err
	}
	for _, other := range family {
		if existing, err := managedproxy.Load(other); err == nil {
			reserved[existing.Port] = true
		}
	}
	for range 32 {
		listener, err := listenLoopback(0)
		if err != nil {
			return err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if port < 1024 || reserved[port] {
			listener.Close()
			continue
		}
		store.Port = port
		err = store.Save(profile)
		listener.Close()
		return err
	}
	return errors.New("找不到可用的本机代理端口")
}

// proxyPortOwner names the other VM of this desktop family whose proxy
// endpoint already uses port. The default VM without a saved proxy store will
// start on the default port, so that port stays reserved for it. An added VM
// always saves its own port at creation; one without a store owns no port.
func proxyPortOwner(profile string, port int) (string, bool, error) {
	family := filepath.Clean(cage.LocalVMFamily(profile))
	profiles, err := cage.LocalVMProfiles(profile)
	if err != nil {
		return "", false, err
	}
	for _, other := range profiles {
		if paths.Equal(filepath.Clean(other), filepath.Clean(profile)) {
			continue
		}
		if !paths.Equal(filepath.Clean(other), family) {
			if _, err := os.Lstat(filepath.Join(managedproxy.Dir(other), "sources.json")); err != nil {
				continue
			}
		}
		store, err := managedproxy.Load(other)
		if err != nil || store.Port != port {
			continue
		}
		return localVMLabel(other), true, nil
	}
	return "", false, nil
}

// checkProxyPortOwner runs when a VM claims an endpoint: a new port or its
// first application. A node switch on the endpoint the VM already applied is
// not re-checked, so an older duplicate never blocks changing nodes; the core
// still refuses a port that is actually in use when it starts. Two VMs that
// claim one port at the same instant are likewise caught by that bind check.
func checkProxyPortOwner(profile string, port int) error {
	owner, taken, err := proxyPortOwner(profile, port)
	if err != nil {
		return errors.New("无法读取本机虚拟机列表，暂不能确认代理端口归属，请检查配置目录的访问权限")
	}
	if taken {
		return fmt.Errorf("代理端口 %d 已分配给虚拟机「%s」。每台虚拟机使用各自的代理端口，请换一个端口后再应用", port, owner)
	}
	return nil
}

func localVMLabel(profile string) string {
	if paths.Equal(filepath.Clean(cage.LocalVMFamily(profile)), filepath.Clean(profile)) {
		return "默认环境"
	}
	if manifest, err := readLocalVMManifest(filepath.Join(profile, localVMManifest)); err == nil {
		return manifest.Name
	}
	return "另一台虚拟机"
}
