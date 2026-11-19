package workbench

import (
	"errors"
	"net"

	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

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
