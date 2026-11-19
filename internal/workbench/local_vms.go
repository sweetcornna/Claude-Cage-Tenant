package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/host"
)

// The native desktop owns local virtual machines. Each independent cage
// profile in <default profile>/vms is one VM; the original default profile is
// the non-removable default VM. A VM gets its own account key and trusted
// grant at creation time, so creating or opening one never asks the user to
// register another account and never copies the source profile's credentials.
const (
	defaultLocalVMID   = "default"
	localVMVersion     = 1
	localVMDir         = cage.LocalVMDir
	localVMManifest    = "vm.json"
	localVMControlFile = "control.json"
	localVMOwnerFile   = "local-vms-owner.json"
	localVMOpenTimeout = 2 * time.Minute
)

var (
	errLocalVMNotFound      = errors.New("未找到此本机虚拟机")
	errLocalAccountMissing  = errors.New("请先完成本机 Cage 账户设置，再新建虚拟机")
	errLocalVMOwnerLocked   = errors.New("请先解锁本机 Cage 工作区")
	errLocalVMOwnerRequired = errors.New("请先进入本机 Cage 工作区再管理虚拟机")
)

type localVMManifestRecord struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Owner   string `json:"owner"`
}

type localVMOwnerRecord struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
}

type localVM struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state,omitempty"`
	Dir    string `json:"-"`
	Config string `json:"-"`
}

type localVMStore struct {
	defaultProfile string
	executable     string
	mu             sync.Mutex
	controllers    map[string]*desktopLocal
}

func newLocalVMStore(defaultProfile, executable string) *localVMStore {
	return &localVMStore{defaultProfile: defaultProfile, executable: executable, controllers: make(map[string]*desktopLocal)}
}

func (m *localVMStore) defaultVM() localVM {
	return localVM{ID: defaultLocalVMID, Name: "默认环境", Dir: m.defaultProfile, Config: filepath.Join(m.defaultProfile, "config.json")}
}

func validLocalVMID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func validLocalVMName(name string) error {
	length := utf8.RuneCountInString(name)
	if length < 1 || length > 64 || strings.IndexFunc(name, unicode.IsControl) != -1 {
		return errors.New("虚拟机名称需要 1–64 个字符，不能包含控制字符")
	}
	return nil
}

func (m *localVMStore) vmDir(id string) string {
	return filepath.Join(m.defaultProfile, localVMDir, id)
}

func (m *localVMStore) manifestPath(id string) string {
	return filepath.Join(m.vmDir(id), localVMManifest)
}

func (m *localVMStore) ownerPath() string {
	return filepath.Join(m.defaultProfile, localVMOwnerFile)
}

// ensureOwner lazily publishes the opaque owner identity for this profile.
// It is a random private token, never a username, so copied VM directories do
// not inherit another profile's ownership when a different owner is present.
func (m *localVMStore) ensureOwner() (string, error) {
	owner, err := m.readOwner()
	if err == nil {
		return owner, nil
	}
	if !os.IsNotExist(err) {
		return "", errors.New("无法读取本机虚拟机所有者，请检查配置目录的访问权限")
	}
	record := localVMOwnerRecord{Version: 1, ID: randomToken()}
	if err = writePrivate(m.ownerPath(), record); err != nil {
		return "", errors.New("无法保存本机虚拟机所有者，请检查配置目录的访问权限")
	}
	return record.ID, nil
}

func (m *localVMStore) readOwner() (string, error) {
	info, err := os.Lstat(m.ownerPath())
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return "", errors.New("本机虚拟机所有者记录无效")
	}
	data, err := os.ReadFile(m.ownerPath())
	if err != nil {
		return "", err
	}
	var record localVMOwnerRecord
	if json.Unmarshal(data, &record) != nil || record.Version != 1 || !validTrustedID(record.ID) || record.ID == "" {
		return "", errors.New("本机虚拟机所有者记录无效")
	}
	return record.ID, nil
}

func (m *localVMStore) find(id string) (localVM, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == defaultLocalVMID {
		return m.defaultVM(), nil
	}
	if !validLocalVMID(id) {
		return localVM{}, errLocalVMNotFound
	}
	manifest, err := readLocalVMManifest(m.manifestPath(id))
	if err != nil {
		return localVM{}, errLocalVMNotFound
	}
	owner, err := m.readOwner()
	if err != nil || manifest.Owner != owner {
		return localVM{}, errLocalVMNotFound
	}
	return localVM{ID: id, Name: manifest.Name, Dir: m.vmDir(id), Config: filepath.Join(m.vmDir(id), "config.json")}, nil
}

// list scans only Cage's own VM directory and never starts or probes a guest
// controller. An unrelated folder, a partial create or a VM owned by another
// profile is never offered.
func (m *localVMStore) list() ([]localVM, error) {
	vms := []localVM{m.defaultVM()}
	owner, err := m.readOwner()
	if os.IsNotExist(err) {
		// Listing is read-only. The owner record is published only when the
		// first VM is created.
		return vms, nil
	}
	if err != nil {
		return nil, errors.New("无法读取本机虚拟机所有者，请检查配置目录的访问权限")
	}
	entries, err := os.ReadDir(filepath.Join(m.defaultProfile, localVMDir))
	if os.IsNotExist(err) {
		return vms, nil
	}
	if err != nil {
		return nil, errors.New("无法读取本机虚拟机列表，请检查配置目录的访问权限")
	}
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validLocalVMID(id) {
			continue
		}
		manifest, err := readLocalVMManifest(m.manifestPath(id))
		if err != nil || manifest.Owner != owner {
			continue
		}
		vms = append(vms, localVM{ID: id, Name: manifest.Name, Dir: m.vmDir(id), Config: filepath.Join(m.vmDir(id), "config.json")})
	}
	sort.Slice(vms[1:], func(i, j int) bool {
		if vms[1+i].Name == vms[1+j].Name {
			return vms[1+i].ID < vms[1+j].ID
		}
		return vms[1+i].Name < vms[1+j].Name
	})
	return vms, nil
}

func readLocalVMManifest(path string) (localVMManifestRecord, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return localVMManifestRecord{}, errors.New("虚拟机记录无效")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return localVMManifestRecord{}, err
	}
	var record localVMManifestRecord
	if json.Unmarshal(data, &record) != nil || record.Version != localVMVersion || validLocalVMName(record.Name) != nil || !validTrustedID(record.Owner) || record.Owner == "" {
		return localVMManifestRecord{}, errors.New("虚拟机记录无效")
	}
	return record, nil
}

// create makes one independent profile. No disk, guest or controller is
// created here: the existing environment startup builds the VM from the
// verified base template on first use, with its own writable storage.
func (m *localVMStore) create(name string) (localVM, error) {
	name = strings.TrimSpace(name)
	if err := validLocalVMName(name); err != nil {
		return localVM{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	owner, err := m.ensureOwner()
	if err != nil {
		return localVM{}, err
	}
	vms, err := m.list()
	if err != nil {
		return localVM{}, err
	}
	for _, vm := range vms {
		if strings.EqualFold(strings.TrimSpace(vm.Name), name) {
			return localVM{}, errors.New("已存在同名虚拟机")
		}
	}
	id := randomToken()[:16]
	dir := m.vmDir(id)
	if err = host.EnsurePrivateDir(filepath.Join(m.defaultProfile, localVMDir)); err != nil {
		return localVM{}, errors.New("无法创建虚拟机目录，请检查配置目录的访问权限")
	}
	if err = host.EnsurePrivateDir(dir); err != nil {
		return localVM{}, errors.New("无法创建虚拟机目录，请检查配置目录的访问权限")
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(dir)
		}
	}()
	if err = writePrivate(m.manifestPath(id), localVMManifestRecord{Version: localVMVersion, Name: name, Owner: owner}); err != nil {
		return localVM{}, errors.New("虚拟机记录未能保存，请检查磁盘空间与目录权限")
	}
	// A new VM starts with its own empty config. Never inherit the default
	// profile's environment storage, proxy revision or any path-bearing state.
	if err = writePrivate(filepath.Join(dir, "config.json"), cage.DefaultConfig()); err != nil {
		return localVM{}, errors.New("虚拟机配置未能保存，请检查磁盘空间与目录权限")
	}
	if err = initLocalVMProxy(dir, vms); err != nil {
		return localVM{}, errors.New("无法保存虚拟机的独立代理端口，请检查磁盘空间与端口可用性")
	}
	if err = createLocalVMAccount(m.defaultProfile, dir, owner); err != nil {
		return localVM{}, err
	}
	published = true
	return localVM{ID: id, Name: name, State: "stopped", Dir: dir, Config: filepath.Join(dir, "config.json")}, nil
}

func (m *localVMStore) controller(vm localVM, fallback DesktopLocalController) DesktopLocalController {
	if vm.ID == defaultLocalVMID {
		return fallback
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if controller := m.controllers[vm.ID]; controller != nil {
		return controller
	}
	controller := &desktopLocal{config: vm.Config, profile: vm.Dir, executable: m.executable}
	m.controllers[vm.ID] = controller
	return controller
}

func normalizedLocalVMID(id string) string {
	if id == "" {
		return defaultLocalVMID
	}
	return id
}

func targetLocalVMID(target *desktopTarget) string {
	if target == nil || target.mode != "local" {
		return ""
	}
	return normalizedLocalVMID(target.vmID)
}

// localController resolves a VM to the existing profile runtime. The default
// VM keeps the controller passed to the gateway, including test fixtures.
func (d *desktopGateway) localController(id string) (DesktopLocalController, error) {
	id = normalizedLocalVMID(id)
	if d.localFor != nil {
		return d.localFor(id)
	}
	if d.localVMs == nil {
		if id != defaultLocalVMID {
			return nil, errLocalVMNotFound
		}
		return d.local, nil
	}
	vm, err := d.localVMs.find(id)
	if err != nil {
		return nil, err
	}
	return d.localVMs.controller(vm, d.local), nil
}

func (d *desktopGateway) localVMName(id string) string {
	id = normalizedLocalVMID(id)
	if d.localVMs != nil {
		if vm, err := d.localVMs.find(id); err == nil {
			return vm.Name
		}
	}
	return "本机环境"
}

func (d *desktopGateway) currentLocalVMID() string {
	d.mu.RLock()
	target := d.active
	d.mu.RUnlock()
	if target == nil {
		return defaultLocalVMID
	}
	return targetLocalVMID(target)
}

// listVirtualMachines reports only inventory and cheap persistent hints. It
// deliberately never calls Status on an inactive VM: a multi-VM list must not
// spawn helpers or probe guest disks just to paint the sidebar.
func (d *desktopGateway) listVirtualMachines() ([]localVM, error) {
	vms, err := d.localVMs.list()
	if err != nil {
		return nil, err
	}
	d.mu.RLock()
	active, pending := d.active, d.pending
	d.mu.RUnlock()
	for index := range vms {
		switch {
		case active != nil && active.mode == "local" && targetLocalVMID(active) == vms[index].ID:
			vms[index].State = "running"
		case pending != nil && pending.mode == "local" && targetLocalVMID(pending) == vms[index].ID:
			vms[index].State = "starting"
		case localVMControlPresent(vms[index].Dir):
			vms[index].State = "running"
		default:
			vms[index].State = "stopped"
		}
	}
	return vms, nil
}

// localVMControlPresent is a record-presence hint only. A stale record is
// harmless for display; the controller's own release path remains the source
// of truth for a real switch or stop.
func localVMControlPresent(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, localVMControlFile))
	return err == nil && info.Mode().IsRegular()
}

// verifyLocalVMOwner requires the exact generation of the currently selected
// local workbench and an unlocked local account. The desktop shell supplies
// X-Cage-Connection; a stale renderer, a remote selection and a locked account
// are all refused before any profile is read or written.
func (d *desktopGateway) verifyLocalVMOwner(r *http.Request) error {
	generation := requestConnection(r)
	if generation == "" {
		return errViewChanged
	}
	d.mu.RLock()
	active, current := d.active, d.generation
	d.mu.RUnlock()
	if generation != current || active == nil || active.mode != "local" {
		return errLocalVMOwnerRequired
	}
	if active.state().State != "connected" {
		return errLocalVMOwnerLocked
	}
	session, err := active.acquire()
	if err != nil {
		return errLocalVMOwnerRequired
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, session.Origin+"/api/account", nil)
	if err != nil {
		return errLocalVMOwnerRequired
	}
	req.Header.Set("Origin", session.Origin)
	response, err := session.Client.Do(req)
	if err != nil {
		return errLocalVMOwnerRequired
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusLocked:
		return errLocalVMOwnerLocked
	default:
		return errLocalVMOwnerRequired
	}
	var account struct {
		Unlocked bool `json:"unlocked"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&account) != nil || !account.Unlocked {
		return errLocalVMOwnerLocked
	}
	// A secondary VM must present the account record this gateway provisioned,
	// not a copied or replaced profile that merely shares the directory name.
	if id := targetLocalVMID(active); id != defaultLocalVMID && d.localVMs != nil {
		if local, ok := active.local.(*desktopLocal); ok {
			record, err := readLocalAccountRecord(local.profile)
			if err != nil || record.Owner == "" {
				return errLocalVMOwnerRequired
			}
			owner, err := d.localVMs.readOwner()
			if err != nil || record.Owner != owner {
				return errLocalVMOwnerRequired
			}
		}
	}
	return nil
}

func localVMOwnerError(w http.ResponseWriter, err error) {
	code := http.StatusConflict
	if errors.Is(err, errLocalVMOwnerLocked) {
		code = http.StatusLocked
	}
	http.Error(w, err.Error(), code)
}

func (d *desktopGateway) virtualMachines(w http.ResponseWriter, r *http.Request) {
	if d.localVMs == nil {
		http.NotFound(w, r)
		return
	}
	if err := d.verifyLocalVMOwner(r); err != nil {
		localVMOwnerError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		vms, err := d.listVirtualMachines()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		jsonReply(w, struct {
			VMs       []localVM `json:"vms"`
			CurrentID string    `json:"current_id"`
		}{VMs: vms, CurrentID: d.currentLocalVMID()})
	case http.MethodPost:
		var input struct {
			Name string `json:"name"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		// Recheck after reading the body and hold the switch lock through the
		// write: a renderer may have started while another window switched to
		// a remote or locked view.
		d.change.Lock()
		defer d.change.Unlock()
		if d.closed {
			http.Error(w, errViewChanged.Error(), http.StatusConflict)
			return
		}
		if err := d.verifyLocalVMOwner(r); err != nil {
			localVMOwnerError(w, err)
			return
		}
		vm, err := d.localVMs.create(input.Name)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errLocalAccountMissing) || err.Error() == "已存在同名虚拟机" {
				code = http.StatusConflict
			}
			http.Error(w, err.Error(), code)
			return
		}
		jsonReply(w, struct {
			VM localVM `json:"vm"`
		}{VM: vm})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *desktopGateway) openVirtualMachine(w http.ResponseWriter, r *http.Request, id string) {
	if d.localVMs == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := d.verifyLocalVMOwner(r); err != nil {
		localVMOwnerError(w, err)
		return
	}
	vm, err := d.localVMs.find(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	d.change.Lock()
	defer d.change.Unlock()
	if d.closed {
		http.Error(w, errViewChanged.Error(), http.StatusConflict)
		return
	}
	// The owner check above may have raced with a switch. Every mutation keeps
	// the exact generation it observed and rechecks it under the change lock.
	if err := d.verifyLocalVMOwner(r); err != nil {
		localVMOwnerError(w, err)
		return
	}
	d.mu.RLock()
	active := d.active
	generation := d.generation
	d.mu.RUnlock()
	if active != nil && active.mode == "local" && targetLocalVMID(active) == vm.ID && active.state().State == "connected" {
		jsonReply(w, map[string]string{"current_id": vm.ID})
		return
	}
	candidate, err := d.prepareLocked(switchRequest{ConnectionID: generation, Mode: "local", VMID: vm.ID})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	d.mu.RLock()
	target := d.pending
	d.mu.RUnlock()
	if target == nil || target.id != candidate.ID {
		http.Error(w, errCandidateChanged.Error(), http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), localVMOpenTimeout)
	defer cancel()
	if err = waitDesktopTarget(ctx, target); err != nil {
		d.dropCandidate(candidate.ID)
		code := http.StatusServiceUnavailable
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	if err = d.verifyLocalVMOwner(r); err != nil {
		d.dropCandidate(candidate.ID)
		localVMOwnerError(w, err)
		return
	}
	if err = d.commitLocked(r.Context(), switchRequest{ConnectionID: generation, CandidateID: candidate.ID}); err != nil {
		d.dropCandidate(candidate.ID)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	jsonReply(w, map[string]string{"current_id": vm.ID})
}

func waitDesktopTarget(ctx context.Context, target *desktopTarget) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := target.state()
		switch state.State {
		case "connected":
			return nil
		case "failed":
			if state.Message == "" {
				return errors.New("本机虚拟机未能连接")
			}
			return errors.New(state.Message)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// A failed open must not leave a prepared candidate that blocks every later
// selection. This is called with change locked.
func (d *desktopGateway) dropCandidate(id string) {
	d.mu.Lock()
	pending := d.pending
	if pending != nil && pending.id == id {
		d.pending = nil
	}
	d.mu.Unlock()
	if pending != nil && pending.id == id {
		d.discard(pending)
	}
}
