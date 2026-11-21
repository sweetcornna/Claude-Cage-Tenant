//go:build !windows

package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
