//go:build !windows

package workbench

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// observedGuest keeps the real host bootstrap and SSH path and replaces only
// the guest helper with one that prints the observation file.
func observedGuest(t *testing.T, s *Server, home string) func([]terminalConversation) {
	t.Helper()
	observation := filepath.Join(home, "observation.json")
	helper := filepath.Join(home, ".local/share/cage/cli-channel-code", fmt.Sprintf("%x.py", sha256.Sum256([]byte(cliChannelHelper+cliResumePrompt+claudeAccountResumePrompt))))
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("import json,sys\njson.load(sys.stdin)\nprint(open(%q).read())\n", observation)
	if err := os.WriteFile(helper, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	return func(found []terminalConversation) {
		t.Helper()
		if err := writePrivate(observation, found); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscoveryRewritesOnlyChangedRecords(t *testing.T) {
	s, id, project := metadataFixture(t)
	home, _ := filepath.EvalSymlinks(t.TempDir())
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/local/bin"})
	record, err := s.mobileCLI.record(s.profile, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.mobileCLI.summaries(s.profile, map[string]bool{project: true}, false); err != nil {
		t.Fatal(err)
	}
	observe := observedGuest(t, s, home)
	s.ready = true
	item := terminalConversation{CLIID: record.CLIID, Workspace: guestPath(project), Config: filepath.Join(home, ".claude"), Title: "first", UpdatedAt: 1700000000}
	observe([]terminalConversation{item})
	path := mobileRecordPath(s.profile, id)
	discover := func() os.FileInfo {
		t.Helper()
		if err := s.discoverTerminalConversations(context.Background(), []string{project}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	first := discover()
	if second := discover(); !os.SameFile(first, second) {
		t.Fatal("an unchanged observation rewrote the session record")
	}
	item.Title = "second"
	observe([]terminalConversation{item})
	if third := discover(); os.SameFile(first, third) {
		t.Fatal("a changed observation was not written")
	}
	if saved, err := s.mobileCLI.record(s.profile, id); err != nil || saved.Title != "second" {
		t.Fatalf("record = %+v, %v", saved, err)
	}
}
