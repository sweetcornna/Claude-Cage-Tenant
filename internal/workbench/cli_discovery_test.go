//go:build !windows

package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/google/uuid"
)

// observedGuest keeps the real host bootstrap and SSH path and replaces only
// the guest helper with one that prints the observation file.
func observedGuest(t *testing.T, s *Server, home string) func([]terminalConversation) {
	t.Helper()
	return observedGuestWith(t, home, "")
}

// guestHelper replaces the versioned channel helper in the guest home.
func guestHelper(t *testing.T, home, code string) {
	t.Helper()
	helper := filepath.Join(home, ".local/share/cage/cli-channel-code", fmt.Sprintf("%x.py", sha256.Sum256([]byte(cliChannelHelper+cliResumePrompt+claudeAccountResumePrompt))))
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
}

// observedGuestWith runs prelude (Python) in the guest helper before it
// prints the observation.
func observedGuestWith(t *testing.T, home, prelude string) func([]terminalConversation) {
	t.Helper()
	observation := filepath.Join(home, "observation.json")
	guestHelper(t, home, fmt.Sprintf("import json,sys,time\njson.load(sys.stdin)\n%s\nprint(open(%q).read())\n", prelude, observation))
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

func sessionList(t *testing.T, s *Server, query string) (int, sessionListAnswer) {
	t.Helper()
	w := request(s, "GET", "/api/cli/sessions"+query, s.origin, "", true)
	var answer sessionListAnswer
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, answer
}

func TestSessionListRevisionAnswersNoContentWhenUnchanged(t *testing.T) {
	s, id, _ := metadataFixture(t)
	code, first := sessionList(t, s, "")
	if code != http.StatusOK || len(first.Sessions) != 1 || len(first.Revision) != 32 {
		t.Fatalf("list = %d %+v", code, first)
	}
	if code, _ := sessionList(t, s, "?after="+first.Revision); code != http.StatusNoContent {
		t.Fatalf("unchanged list answered %d", code)
	}
	if code, _ := sessionList(t, s, "?archived=true&after="+first.Revision); code != http.StatusOK {
		t.Fatalf("the archived list shares a revision with the active list: %d", code)
	}
	if w := request(s, "POST", "/api/cli/sessions/"+id+"/metadata", s.origin, `{"title":"改名"}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	code, renamed := sessionList(t, s, "?after="+first.Revision)
	if code != http.StatusOK || renamed.Revision == first.Revision || renamed.Sessions[0].Title != "改名" {
		t.Fatalf("a rename did not reach the conditional answer: %d %+v", code, renamed)
	}
	for _, query := range []string{"?after=xyz", "?after=" + strings.ToUpper(first.Revision), "?wait=5", "?after=" + first.Revision + "&wait=-1"} {
		if code, _ := sessionList(t, s, query); code != http.StatusBadRequest {
			t.Fatalf("%s answered %d", query, code)
		}
	}
}

func TestConcurrentSessionListsShareDiscovery(t *testing.T) {
	s, id, project := metadataFixture(t)
	home, _ := filepath.EvalSymlinks(t.TempDir())
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/local/bin"})
	record, err := s.mobileCLI.record(s.profile, id)
	if err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(home, "runs")
	observe := observedGuestWith(t, home, fmt.Sprintf("open(%q,'a').write('run\\n')\ntime.sleep(0.4)", runs))
	observe([]terminalConversation{{CLIID: record.CLIID, Workspace: guestPath(project), Config: filepath.Join(home, ".claude"), Title: "guest", UpdatedAt: 1700000000}})
	s.ready = true
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = request(s, "GET", "/api/cli/sessions", s.origin, "", true).Code
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(runs)
	if count := strings.Count(string(data), "run"); count < 1 || count > 2 {
		t.Fatalf("six concurrent lists ran %d discoveries", count)
	}
	for _, code := range codes {
		if code != http.StatusOK {
			t.Fatal(codes)
		}
	}
}

func TestSessionListNeverSharesAnEarlierDiscovery(t *testing.T) {
	s, id, project := metadataFixture(t)
	home, _ := filepath.EvalSymlinks(t.TempDir())
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/local/bin"})
	record, err := s.mobileCLI.record(s.profile, id)
	if err != nil {
		t.Fatal(err)
	}
	// The guest reads its observation, marks that it did, and answers later.
	marker := filepath.Join(home, "observed")
	observation := filepath.Join(home, "observation.json")
	guestHelper(t, home, fmt.Sprintf("import json,sys,time\njson.load(sys.stdin)\ndata=open(%q).read()\nopen(%q,'a').write('x')\ntime.sleep(0.6)\nprint(data)\n", observation, marker))
	item := terminalConversation{CLIID: record.CLIID, Workspace: guestPath(project), Config: filepath.Join(home, ".claude"), Title: "before", UpdatedAt: 1700000000}
	if err := writePrivate(observation, []terminalConversation{item}); err != nil {
		t.Fatal(err)
	}
	s.ready = true
	earlier := make(chan sessionListAnswer, 1)
	go func() {
		_, answer := sessionList(t, s, "")
		earlier <- answer
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first discovery did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	item.Title = "after"
	if err := writePrivate(observation, []terminalConversation{item}); err != nil {
		t.Fatal(err)
	}
	// This request arrives while a run that read the old title is in flight;
	// it must answer with an observation made after its own arrival.
	_, latest := sessionList(t, s, "")
	if first := <-earlier; len(first.Sessions) != 1 || first.Sessions[0].Title != "before" {
		t.Fatalf("first list = %+v", first.Sessions)
	}
	if len(latest.Sessions) != 1 || latest.Sessions[0].Title != "after" {
		t.Fatalf("later list = %+v", latest.Sessions)
	}
}

// residentGuest prepares a guest home with a login profile and a claude CLI
// that count their runs, and the real channel helper.
func residentGuest(t *testing.T, s *Server) (home string, logins, claudeRuns func() int) {
	t.Helper()
	home, _ = filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	cli := "#!/bin/sh\n[ \"$1\" = agents ] || exit 64\necho run >> \"$HOME/claude-runs\"\nprintf '[]\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(cli), 0700); err != nil {
		t.Fatal(err)
	}
	profile := "echo login >> \"$HOME/logins\"\nexport PATH=" + shellescape.Quote(bin) + ":/usr/bin:/bin\n"
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin", "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude")})
	t.Cleanup(s.cliDiscovery.resident.close)
	count := func(name string) func() int {
		return func() int {
			data, _ := os.ReadFile(filepath.Join(home, name))
			return strings.Count(string(data), "\n")
		}
	}
	return home, count("logins"), count("claude-runs")
}

func writeTranscript(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	stream, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for _, row := range rows {
		data, _ := json.Marshal(row)
		if _, err := stream.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionListsUseOneResidentGuestHelper(t *testing.T) {
	s, id, project := metadataFixture(t)
	home, logins, claudeRuns := residentGuest(t, s)
	record, err := s.mobileCLI.record(s.profile, id)
	if err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(home, ".claude", "projects", "fixture", record.CLIID+".jsonl")
	writeTranscript(t, history, map[string]any{"cwd": guestPath(project), "type": "user", "uuid": uuid.NewString(), "message": map[string]string{"content": "guest title"}})
	s.ready = true
	title := func() string {
		t.Helper()
		code, answer := sessionList(t, s, "")
		if code != http.StatusOK || len(answer.Sessions) != 1 || answer.Notice != "" {
			t.Fatalf("list = %d %+v", code, answer)
		}
		return answer.Sessions[0].Title
	}
	// The first request installs the channel helper with an ordinary login;
	// the second starts the resident helper with one more.
	for want := 1; want <= 2; want++ {
		if got := title(); got != "guest title" || logins() != want || claudeRuns() != want {
			t.Fatalf("request %d: title %q, %d logins, %d CLI listings", want, got, logins(), claudeRuns())
		}
	}
	for range 3 {
		if got := title(); got != "guest title" || logins() != 2 || claudeRuns() != 2 {
			t.Fatalf("an unchanged list cost a login or a CLI listing: title %q, %d logins, %d listings", got, logins(), claudeRuns())
		}
	}
	writeTranscript(t, history, map[string]any{"type": "custom-title", "customTitle": "renamed in the guest"})
	if got := title(); got != "renamed in the guest" || logins() != 2 {
		t.Fatalf("a changed transcript: title %q after %d logins", got, logins())
	}
	// Reading the session itself uses the same helper.
	for range 2 {
		if w := request(s, "GET", "/api/mobile/cli/sessions/"+id, s.origin, "", true); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if logins() != 2 {
		t.Fatalf("session reads opened %d logins", logins()-2)
	}
	if s.cliDiscovery.viewKeys[id].key == "" {
		t.Fatal("the resident view left no watch key")
	}
	// A resident helper that ended is started again with one login.
	if err := exec.Command("pkill", "-f", cliChannelVersion+" "+fmt.Sprintf("%x", sha256.Sum256([]byte(cliChannelInstalled)))).Run(); err != nil {
		t.Fatal("no resident helper to stop:", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := title(); got != "renamed in the guest" || logins() != 3 {
		t.Fatalf("after the helper ended: title %q, %d logins", got, logins())
	}
}
