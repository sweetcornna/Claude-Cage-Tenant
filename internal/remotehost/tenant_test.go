package remotehost

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTenantPortalCanonicalOrigin(t *testing.T) {
	for input, want := range map[string]string{
		" https://Cage.Example.org:443/ ": "https://cage.example.org",
		"https://cage.example.org:8443":   "https://cage.example.org:8443",
	} {
		origin, err := parseTenantPortal(input)
		if err != nil || origin.String() != want {
			t.Fatalf("parse %q = %v, %v; want %q", input, origin, err, want)
		}
	}
	for _, input := range []string{"http://cage.example.org", "https://localhost", "https://cage.localhost", "https://127.0.0.1", "https://cage.example.org:0443", "https://cage.example.org:", "https://user@cage.example.org", "https://cage.example.org/login", "https://cage.example.org/?", "https://cage.example.org/#token", "https://cage.example.org/%2f", "https://cage..example.org"} {
		if _, err := parseTenantPortal(input); err == nil {
			t.Errorf("accepted non-portal URL: %q", input)
		}
	}
}

func TestTenantWorkspaceTicketBoundToExpectedVM(t *testing.T) {
	portal, err := parseTenantPortal("https://cage.example.org:8443")
	if err != nil {
		t.Fatal(err)
	}
	id, token := strings.Repeat("a", 32), strings.Repeat("b", 64)
	origin := "https://vm-" + id + ".cage.example.org:8443"
	entry := "/_tenant/enter#"
	if got, ticket, err := tenantWorkspaceURL(portal, id, origin+entry+token); err != nil || got != origin || ticket != token {
		t.Fatal("valid workspace entry refused", err)
	}
	for _, other := range []string{strings.Replace(origin, id, strings.Repeat("c", 32), 1), strings.TrimSuffix(origin, ":8443"), origin + ".test", "http" + strings.TrimPrefix(origin, "https")} {
		if _, _, err := tenantWorkspaceURL(portal, id, other+entry+token); err == nil {
			t.Errorf("accepted mismatched VM origin %s", other)
		}
	}
	for _, suffix := range []string{entry, entry + token[:63], entry + token + "?a", "/_tenant/enter?x=1#" + token, "/#" + token} {
		if _, _, err := tenantWorkspaceURL(portal, id, origin+suffix); err == nil {
			t.Errorf("accepted invalid workspace entry %q", suffix)
		}
	}
}

func TestTenantProfilesDoNotRewriteSSHStore(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	ssh, err := s.Upsert(HostProfile{Name: "SSH", Host: "ssh.example.org", User: "cage", Auth: AuthConfig{Kind: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, sshHosts)
	before, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	profile := HostProfile{Kind: TenantKind, Portal: "https://Cage.Example.org:443/", VMID: strings.Repeat("a", 32), Name: "测试 VM", User: " Alice ", Auth: AuthConfig{Kind: "key_file", IdentityFile: "ignored-value"}, ConfigPath: "also-ignored"}
	p, err := s.Upsert(profile)
	if err != nil || p.User != "alice" || p.Port != 443 || p.Portal != "https://cage.example.org" || p.Auth.Kind != "portal_password" || p.Auth.IdentityFile != "" || p.ConfigPath != "" {
		t.Fatalf("tenant profile not canonical: %+v, %v", p, err)
	}
	if _, err := s.Upsert(profile); err == nil {
		t.Fatal("saved two rows for the same VM")
	}
	if listed, err := NewStore(dir).List(); err != nil || len(listed) != 2 || listed[0].ID != ssh.ID || listed[1].ID != p.ID {
		t.Fatalf("merged hosts: %+v, %v", listed, err)
	}
	tenantFile, err := os.ReadFile(filepath.Join(dir, tenantHosts))
	if err != nil || bytes.Contains(tenantFile, []byte("ignored")) {
		t.Fatal("tenant profile persisted unrelated input", err)
	}
	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("tenant store mutated SSH configuration", err)
	}
	if listed, err := s.List(); err != nil || len(listed) != 1 || listed[0].ID != ssh.ID {
		t.Fatalf("hosts after tenant delete: %+v, %v", listed, err)
	}
}

func TestInvalidHostStoreLeavesOtherHostKindUsable(t *testing.T) {
	for _, kind := range []string{sshHosts, tenantHosts} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			s := NewStore(dir)
			ssh, err := s.Upsert(HostProfile{Name: "SSH", Host: "ssh.example.org", User: "cage", Auth: AuthConfig{Kind: "agent"}})
			if err != nil {
				t.Fatal(err)
			}
			tenant, err := s.Upsert(HostProfile{Kind: TenantKind, Portal: "https://cage.example.org", VMID: strings.Repeat("a", 32), Name: "VM", User: "alice"})
			if err != nil {
				t.Fatal(err)
			}
			good := ssh
			if kind == sshHosts {
				good = tenant
			}
			broken := filepath.Join(dir, kind)
			if err := os.WriteFile(broken, []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			if listed, err := s.List(); err == nil || len(listed) != 1 || listed[0].ID != good.ID {
				t.Fatalf("partial list = %+v, %v", listed, err)
			}
			if got, err := s.Get(good.ID); err != nil || got.ID != good.ID {
				t.Fatalf("healthy profile = %+v, %v", got, err)
			}
			if _, err := s.ListSSH(); (err != nil) != (kind == sshHosts) {
				t.Fatalf("SSH credential inventory error = %v", err)
			}
			if err := s.Delete(good.ID); err != nil {
				t.Fatal(err)
			}
			if contents, err := os.ReadFile(broken); err != nil || string(contents) != "invalid" {
				t.Fatalf("damaged profile changed: %q, %v", contents, err)
			}
		})
	}
}
