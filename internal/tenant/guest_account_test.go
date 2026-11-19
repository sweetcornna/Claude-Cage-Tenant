package tenant

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestAccountFileValidation(t *testing.T) {
	dir := t.TempDir()
	secret := strings.Repeat("a", 64)
	valid := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(valid, []byte(`{"username":"cage","secret":"`+secret+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	account, err := LoadGuestAccount(valid)
	if err != nil || !validGuestAccountName(account.username) {
		t.Fatalf("valid guest account was rejected: %v", err)
	}
	if password := account.password(strings.Repeat("1", 32)); !secretPattern.MatchString(string(password)) || string(password) == string(account.password(strings.Repeat("2", 32))) {
		t.Fatal("guest password is not a unique 32-byte hex value")
	}
	public := filepath.Join(dir, "public.json")
	if err := os.WriteFile(public, []byte(`{"username":"cage","secret":"`+secret+`"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGuestAccount(public); err == nil {
		t.Fatal("world-readable guest account was accepted")
	}
	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"username":"cage","secret":"`+secret+`","extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGuestAccount(unknown); err == nil {
		t.Fatal("unknown guest account field was accepted")
	}
	short := filepath.Join(dir, "short.json")
	if err := os.WriteFile(short, []byte(`{"username":"cage","secret":"abc"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGuestAccount(short); err == nil {
		t.Fatal("short guest account secret was accepted")
	}
	if _, err := LoadGuestAccount(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing guest account file was accepted")
	}
}

func TestGuestAccountStateSecretIsPrivateAndStable(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateGuestAccount(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, guestAccountStateFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state guest account secret is not private: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"username":"cage"`) {
		t.Fatalf("unexpected state guest account file: %s", data)
	}
	second, err := loadOrCreateGuestAccount(dir)
	if err != nil {
		t.Fatal(err)
	}
	vmID := strings.Repeat("3", 32)
	if string(first.password(vmID)) != string(second.password(vmID)) {
		t.Fatal("guest account secret changed across reloads")
	}
	if !secretPattern.MatchString(hex.EncodeToString(first.secret)) {
		t.Fatal("guest account secret has the wrong size")
	}
}
