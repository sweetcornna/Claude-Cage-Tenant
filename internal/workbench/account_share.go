package workbench

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sylvinus/agent-vm/internal/host"
)

// trustedLocalDir is the per-system-user credential store used by the native
// desktop's trusted login. It is outside every profile, so each VM's own
// wrapping credential can resume against its own account record without a
// prompt.
func trustedLocalDir() (string, error) {
	base, _ := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		base = os.Getenv("LOCALAPPDATA")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("无法确定本机系统用户配置目录")
	}
	return filepath.Join(base, "Cage", "trusted-local"), nil
}

// createLocalVMAccount gives a new VM its own data key and its own OS-user
// trust slot, using the same record and crypto as the first-run trusted local
// workspace. The visible account name and avatar are inherited, while no
// source key, password wrapping, recovery or device grant is copied. If the
// user later turns off automatic entry, the existing account settings can set
// a password for this VM without affecting any other VM.
func createLocalVMAccount(defaultProfile, destination, owner string) error {
	data, err := os.ReadFile(filepath.Join(defaultProfile, "account.json"))
	if os.IsNotExist(err) {
		return errLocalAccountMissing
	}
	if err != nil {
		return errors.New("无法读取本机 Super Cage 账户，请检查配置目录的访问权限")
	}
	var source accountRecord
	if len(data) > 256<<10 || json.Unmarshal(data, &source) != nil || source.Version != 1 || validAccountName(source.Username) != nil {
		return errors.New("本机 Super Cage 账户记录损坏，请从备份恢复")
	}
	if !filepath.IsAbs(destination) {
		return errors.New("虚拟机目录无效")
	}
	info, err := os.Lstat(destination)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("虚拟机目录无效")
	}
	accountPath := filepath.Join(destination, "account.json")
	if _, err := os.Lstat(accountPath); err == nil {
		return errors.New("虚拟机账户已存在")
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return err
	}
	defer clear(key)
	secret := randomToken()
	record := &accountRecord{
		Version:       1,
		Username:      source.Username,
		Avatar:        source.Avatar,
		Owner:         owner,
		TrustedDevice: randomToken(),
	}
	if err = sealAccountKey(record, secret, key); err != nil {
		return err
	}
	dir, err := trustedLocalDir()
	if err != nil {
		return err
	}
	credential := filepath.Join(dir, record.TrustedDevice+".json")
	if err = writePrivate(credential, secret); err != nil {
		return errors.New("无法保存当前系统用户的信任凭据")
	}
	if err = writePrivate(accountPath, record); err != nil {
		_ = os.Remove(credential)
		return errors.New("虚拟机账户未能保存，请检查磁盘空间与目录权限")
	}
	return nil
}

// readLocalAccountRecord reads the private account record of a local workload
// without unlocking it. It is used only to check that the selected VM belongs
// to this gateway's owner identity.
func readLocalAccountRecord(profile string) (*accountRecord, error) {
	path := filepath.Join(profile, "account.json")
	if err := host.CheckPrivateFile(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 256<<10 {
		return nil, errors.New("本地账户文件无法读取")
	}
	var record accountRecord
	if json.Unmarshal(data, &record) != nil || record.Version != 1 || !validTrustedID(record.Owner) {
		return nil, errors.New("本地账户文件无效")
	}
	return &record, nil
}
