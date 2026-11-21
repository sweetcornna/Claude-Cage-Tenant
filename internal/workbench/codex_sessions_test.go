//go:build !windows

package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/google/uuid"
)

func TestCodexSessionsHelper(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cmd := exec.Command(python, "-I", "codex_sessions_test.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("codex session helper: %v\n%s", err, output)
	}
}

func TestCodexSessionsCommandFitsGuestArgument(t *testing.T) {
	// SSH passes the command to the guest shell as one argument; Linux limits
	// a single argument to 128 KiB.
	command := codexSessionsCommand("discover")
	t.Logf("embedded command: %d bytes", len(command))
	if len(command) > 96<<10 {
		t.Fatalf("Codex session command uses %d bytes", len(command))
	}
}

type codexGuestCall struct {
	Mode    string         `json:"mode"`
	Request map[string]any `json:"request"`
}

// codexFakeGuest serves the Codex helper's operations from canned replies
// through the real host SSH path. Only the guest operation boundary is fake.
type codexFakeGuest struct {
	t    *testing.T
	home string
}

func newCodexFixture(t *testing.T) (*Server, string, *codexFakeGuest) {
	t.Helper()
	s := testServer(t)
	project, _ := filepath.EvalSymlinks(t.TempDir())
	s.preferences.Workspace = project
	home, _ := filepath.EvalSymlinks(t.TempDir())
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin"})
	helper := filepath.Join(home, ".local/share/cage/codex-session-code", fmt.Sprintf("%x.py", sha256.Sum256([]byte(codexSessionsHelper))))
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	code := "import json,os,sys\nmode=sys.argv[1]\nrequest=json.load(sys.stdin)\nhome=os.environ['HOME']\n" +
		"open(os.path.join(home,'calls.jsonl'),'a').write(json.dumps({'mode':mode,'request':request})+'\\n')\n" +
		"print(json.dumps(json.load(open(os.path.join(home,'replies.json'))).get(mode,{'error':'unexpected'})))\n"
	if err := os.WriteFile(helper, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	s.ready = true
	return s, project, &codexFakeGuest{t: t, home: home}
}

func (g *codexFakeGuest) reply(mode string, value any) {
	g.t.Helper()
	replies := map[string]any{}
	if data, err := os.ReadFile(filepath.Join(g.home, "replies.json")); err == nil {
		_ = json.Unmarshal(data, &replies)
	}
	replies[mode] = value
	data, _ := json.Marshal(replies)
	if err := os.WriteFile(filepath.Join(g.home, "replies.json"), data, 0600); err != nil {
		g.t.Fatal(err)
	}
}

func (g *codexFakeGuest) calls(mode string) []map[string]any {
	g.t.Helper()
	data, _ := os.ReadFile(filepath.Join(g.home, "calls.jsonl"))
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var call codexGuestCall
		if json.Unmarshal([]byte(line), &call) == nil && call.Mode == mode {
			found = append(found, call.Request)
		}
	}
	return found
}

func codexSessions(t *testing.T, s *Server) ([]mobileSummary, string) {
	t.Helper()
	w := request(s, "GET", "/api/mobile/cli/sessions", "", "", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var listing struct {
		Sessions []mobileSummary `json:"sessions"`
		Notice   string          `json:"notice"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	return listing.Sessions, listing.Notice
}

func TestCodexSessionsDiscoveryViewAndMessages(t *testing.T) {
	s, project, guest := newCodexFixture(t)
	root := guestPath(project)
	thread := "0199a213-81c0-7800-8aa1-bbab2a035a53"
	guest.reply("discover", []map[string]any{
		{"thread_id": thread, "account": "default", "workspace": root + "/service", "title": "Codex 标题", "updated_at": 1763600000.5, "running": true},
		{"thread_id": uuid.NewString(), "account": "default", "workspace": "/elsewhere", "title": "outside"},
	})
	sessions, notice := codexSessions(t, s)
	if notice != "" || len(sessions) != 1 || sessions[0].Provider != codexProvider || sessions[0].Title != "Codex 标题" || !sessions[0].Running || sessions[0].Workspace != project {
		t.Fatalf("Codex discovery: %#v %q", sessions, notice)
	}
	if again, _ := codexSessions(t, s); len(again) != 1 || again[0].ID != sessions[0].ID {
		t.Fatal("rediscovery duplicated the session", again)
	}
	// The desktop sidebar cannot open Codex sessions, so it does not list them.
	if w := request(s, "GET", "/api/cli/sessions", "", "", true); w.Code != 200 || strings.Contains(w.Body.String(), sessions[0].ID) {
		t.Fatalf("desktop listing: %d %s", w.Code, w.Body.String())
	}
	if calls := guest.calls("discover"); len(calls) != 2 || fmt.Sprint(calls[0]["accounts"]) != "[default]" || fmt.Sprint(calls[0]["workspaces"]) != "["+root+"]" {
		t.Fatal("discovery request", calls)
	}
	id := sessions[0].ID
	record, err := s.mobileCLI.record(s.profile, id)
	if err != nil || record.CLIID != thread || !record.Resume || !record.Terminal || record.CodexAccount != "" || record.GuestWorkspace != root+"/service" || record.Connection != "" {
		t.Fatalf("discovered record: %#v %v", record, err)
	}

	turn := map[string]any{"id": uuid.NewString(), "prompt": "看看这个项目", "text": "好的", "status": "completed",
		"tools": []map[string]string{{"id": "call-1", "name": "exec_command", "detail": "ls", "status": "completed"}}}
	view := map[string]any{"thread_id": thread, "account": "default", "workspace": root + "/service", "title": "Codex 标题", "updated_at": 1763600001.0,
		"turns": []any{turn}, "running": false, "active": false, "can_send": true}
	guest.reply("view", view)
	path := "/api/mobile/cli/sessions/" + id
	w := request(s, "GET", path, "", "", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var session mobileSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Provider != codexProvider || len(session.Turns) != 1 || session.Turns[0].Text != "好的" || len(session.Turns[0].Tools) != 1 || session.Turns[0].Tools[0].Detail != "ls" ||
		session.CanSend == nil || !*session.CanSend || session.TerminalAvailable {
		t.Fatalf("Codex view: %s", w.Body.String())
	}
	if w := request(s, "GET", fmt.Sprintf("%s?after=%d&epoch=%s", path, session.Revision, session.Generation), "", "", true); w.Code != http.StatusNoContent {
		t.Fatal("unchanged view was resent", w.Code)
	}
	if calls := guest.calls("view"); calls[0]["thread"] != thread || calls[0]["account"] != "default" || calls[0]["workspace"] != root {
		t.Fatal("view request", calls[0])
	}

	guest.reply("send", map[string]any{"ok": true})
	requestID := uuid.NewString()
	body, _ := json.Marshal(map[string]string{"request_id": requestID, "text": "继续"})
	w = request(s, "POST", path+"/messages", s.origin, string(body), true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt mobileSession
	_ = json.Unmarshal(w.Body.Bytes(), &receipt)
	if receipt.AcknowledgedRequestID != requestID || receipt.Turns[len(receipt.Turns)-1].Status != "queued" || receipt.Turns[len(receipt.Turns)-1].Prompt != "继续" {
		t.Fatalf("send receipt: %s", w.Body.String())
	}
	record, _ = s.mobileCLI.record(s.profile, id)
	sent := guest.calls("send")
	if len(sent) != 1 || !mobileID(record.Connection) || sent[0]["connection"] != record.Connection || sent[0]["thread"] != thread || sent[0]["text"] != "继续" ||
		sent[0]["cwd"] != root+"/service" || sent[0]["account"] != "default" || sent[0]["subject"] != "" || fmt.Sprint(sent[0]["additional_directories"]) != "[]" {
		t.Fatalf("send request: %#v", sent)
	}
	busy := "这段会话正在电脑上的 Codex 终端中打开。请先退出该终端中的 Codex，再从手机继续。"
	guest.reply("send", map[string]any{"error": busy})
	body, _ = json.Marshal(map[string]string{"request_id": uuid.NewString(), "text": "再来"})
	if w := request(s, "POST", path+"/messages", s.origin, string(body), true); w.Code != 409 || !strings.Contains(w.Body.String(), busy) {
		t.Fatal("interactive terminal did not refuse the message", w.Code, w.Body.String())
	}

	view["turns"] = []any{turn, map[string]any{"id": requestID, "prompt": "继续", "text": "", "status": "running"}}
	view["running"], view["active"], view["can_send"] = true, true, false
	guest.reply("view", view)
	guest.reply("stop", map[string]any{"ok": true})
	body, _ = json.Marshal(map[string]string{"request_id": requestID})
	if w := request(s, "POST", path+"/stop", s.origin, string(body), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if stops := guest.calls("stop"); len(stops) != 1 || stops[0]["request_id"] != requestID || stops[0]["connection"] != record.Connection {
		t.Fatal("stop request", stops)
	}
	for route, body := range map[string]string{"/approval": `{"request_id":"` + requestID + `","approval_id":"` + uuid.NewString() + `","allow":true}`, "/open": `{}`, "/account": `{"action":"switch","confirm":true}`} {
		if w := request(s, "POST", path+route, s.origin, body, true); w.Code != 409 {
			t.Fatal("Codex session accepted", route, w.Code, w.Body.String())
		}
	}
	w = request(s, "POST", path+"/account", s.origin, `{"action":"inspect"}`, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"supported":false`) {
		t.Fatal("account inspect", w.Code, w.Body.String())
	}

	view["workspace"] = "/elsewhere"
	guest.reply("view", view)
	if w := request(s, "GET", path, "", "", true); w.Code != 503 {
		t.Fatal("view outside the project accepted", w.Code)
	}
	guest.reply("purge", map[string]any{"ok": true})
	if w := request(s, "POST", path+"/archive", s.origin, `{}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(s, "POST", path+"/purge", s.origin, `{"confirm":true}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if purges := guest.calls("purge"); len(purges) != 1 || purges[0]["thread"] != thread || purges[0]["workspace"] != root || purges[0]["connection"] != record.Connection {
		t.Fatal("purge request", purges)
	}
	if sessions, _ := codexSessions(t, s); len(sessions) != 0 {
		t.Fatal("purged Codex session rediscovered", sessions)
	}
}

func TestCodexNewSessionUsesAllocatedAccountAndBindsItsThread(t *testing.T) {
	s, project, guest := newCodexFixture(t)
	account := codexAccount{ID: strings.Repeat("ab", 12), Label: "work", Enabled: true, Status: "ready", Email: "codex@example.com"}
	if err := s.saveCodexAccounts(codexAccountList{Accounts: []codexAccount{account}, Scheduling: (claudeScheduling{}).defaults()}); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	create := func(provider string) *httpResponse {
		body, _ := json.Marshal(map[string]string{"id": id, "workspace": project, "provider": provider})
		w := request(s, "POST", "/api/mobile/cli/sessions", s.origin, string(body), true)
		return &httpResponse{w.Code, w.Body.String()}
	}
	created := create(codexProvider)
	var session mobileSession
	if created.code != 200 || json.Unmarshal([]byte(created.body), &session) != nil || session.ID != id || session.Provider != codexProvider ||
		len(session.Turns) != 0 || session.CanSend == nil || !*session.CanSend || session.TerminalAvailable {
		t.Fatal("new Codex session", created)
	}
	record, err := s.mobileCLI.record(s.profile, id)
	if err != nil || record.CodexAccount != account.ID || record.Resume || !mobileID(record.Connection) || record.GuestWorkspace != guestPath(project) {
		t.Fatalf("new Codex record: %#v %v", record, err)
	}
	if retried := create(codexProvider); retried.code != 200 {
		t.Fatal("retried creation", retried)
	}
	if claude := create(""); claude.code != 409 {
		t.Fatal("Claude creation reused a Codex session", claude)
	}
	if other := create("other"); other.code != 400 {
		t.Fatal("unknown provider", other)
	}
	if len(s.terminals.snapshot()) != 0 {
		t.Fatal("a new Codex session started a terminal")
	}

	guest.reply("view", map[string]any{"thread_id": "", "turns": []any{}, "can_send": true})
	if w := request(s, "GET", "/api/mobile/cli/sessions/"+id, "", "", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	guest.reply("send", map[string]any{"ok": true})
	body, _ := json.Marshal(map[string]string{"request_id": uuid.NewString(), "text": "开始"})
	if w := request(s, "POST", "/api/mobile/cli/sessions/"+id+"/messages", s.origin, string(body), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	sent := guest.calls("send")
	if len(sent) != 1 || sent[0]["thread"] != "" || sent[0]["account"] != account.ID || sent[0]["subject"] != account.subject() || sent[0]["connection"] != record.Connection {
		t.Fatalf("first message: %#v", sent)
	}

	thread := "0199a213-81c0-7800-8aa1-bbab2a035a54"
	guest.reply("discover", []map[string]any{{"thread_id": thread, "account": account.ID, "connection": record.Connection, "workspace": guestPath(project), "title": "开始", "running": true, "active": true}})
	sessions, _ := codexSessions(t, s)
	if len(sessions) != 1 || sessions[0].ID != id || !sessions[0].Running || !sessions[0].Active || sessions[0].Title != "开始" {
		t.Fatal("first turn did not bind the new session", sessions)
	}
	if calls := guest.calls("discover"); fmt.Sprint(calls[0]["accounts"]) != "[default "+account.ID+"]" {
		t.Fatal("account homes not searched", calls)
	}
	record, _ = s.mobileCLI.record(s.profile, id)
	if record.CLIID != thread || !record.Resume {
		t.Fatalf("thread not bound: %#v", record)
	}
	ctx := context.WithValue(context.Background(), accountEpochKey{}, mustEpoch(t, s))
	if err := s.discoverCodexConversations(ctx, []string{project}); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.mobileCLI.summaries(s.profile, map[string]bool{project: true}, false); len(list) != 1 {
		t.Fatal("rediscovery duplicated the bound session", list)
	}
}

func TestCodexSessionBlocksProjectRemovalWhileRunning(t *testing.T) {
	s, project, guest := newCodexFixture(t)
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"tabs":[]}`))}, nil
	}))
	session := map[string]any{"thread_id": uuid.NewString(), "account": "default", "workspace": guestPath(project), "title": "busy", "running": true, "active": true}
	guest.reply("discover", []any{session})
	if sessions, _ := codexSessions(t, s); len(sessions) != 1 || !sessions[0].Running {
		t.Fatal("running Codex session not listed", sessions)
	}
	removeProjectRequest(t, s, project, 409)
	session["running"], session["active"] = false, false
	guest.reply("discover", []any{session})
	removeProjectRequest(t, s, project, 200)
}

func TestCodexSessionsBootstrapRunsShippedHelper(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	s := testServer(t)
	home, _ := filepath.EvalSymlinks(t.TempDir())
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin"})
	var found []codexConversation
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.runCodexSessions(ctx, "discover", map[string]any{"accounts": []string{"default"}, "workspaces": []string{"/project"}}, &found); err != nil || len(found) != 0 {
		t.Fatal("shipped helper discovery", found, err)
	}
	installed, err := os.ReadFile(filepath.Join(home, ".local/share/cage/codex-session-code", fmt.Sprintf("%x.py", sha256.Sum256([]byte(codexSessionsHelper)))))
	if err != nil || string(installed) != codexSessionsHelper {
		t.Fatal("helper was not installed verbatim", err)
	}
	var failure codexConversation
	if err := s.runCodexSessions(ctx, "view", map[string]any{"thread": "not-a-thread", "account": "default"}, &failure); err != nil || failure.Error == "" {
		t.Fatal("invalid request did not return a message", failure, err)
	}
}

type httpResponse struct {
	code int
	body string
}

func mustEpoch(t *testing.T, s *Server) context.Context {
	t.Helper()
	epoch, ok := s.account.access()
	if !ok {
		t.Fatal("missing unlock epoch")
	}
	return epoch
}

// The shipped helper runs each phone message as a detached exec turn. A
// stand-in CLI writes the rollout records of a real turn; nothing else is fake.
func TestCodexSessionThroughShippedHelper(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	s := testServer(t)
	project, _ := filepath.EvalSymlinks(t.TempDir())
	s.preferences.Workspace = project
	home, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	codex := `#!/usr/bin/python3
import json, os, pathlib, sys, time, uuid
args, home = sys.argv[1:], pathlib.Path.home() / ".codex"
text = sys.stdin.read()
if "resume" in args:
    thread = args[args.index("resume") + 1]
    path = next(home.glob("sessions/*/*/*/rollout-*-" + thread + ".jsonl"))
else:
    thread = str(uuid.uuid4())
    path = home / "sessions/2026/11/20" / ("rollout-2026-11-20T10-00-00-" + thread + ".jsonl")
    path.parent.mkdir(parents=True)
    path.write_text(json.dumps({"type": "session_meta", "payload": {"id": thread, "cwd": os.getcwd(), "source": "exec"}}) + "\n")
print(json.dumps({"type": "thread.started", "thread_id": thread}), flush=True)
def row(kind, payload):
    with path.open("a") as stream:
        stream.write(json.dumps({"type": kind, "payload": payload}) + "\n")
row("event_msg", {"type": "task_started"})
row("response_item", {"type": "message", "role": "user", "content": [{"type": "input_text", "text": text}]})
if (pathlib.Path.home() / "fake-sleep").exists():
    time.sleep(30)
row("response_item", {"type": "function_call", "name": "exec_command", "arguments": json.dumps({"cmd": "ls"}), "call_id": "call-" + str(len(text))})
row("response_item", {"type": "function_call_output", "call_id": "call-" + str(len(text)), "output": "ok"})
row("response_item", {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "reply: " + text}]})
row("event_msg", {"type": "task_complete"})
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0700); err != nil {
		t.Fatal(err)
	}
	// The guest login shell must find only the stand-in CLI, never one
	// installed on the developer's machine.
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("export PATH="+shellescape.Quote(bin)+":/usr/bin:/bin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"})
	t.Cleanup(func() {
		states, _ := filepath.Glob(filepath.Join(home, ".local/share/cage/codex-sessions/*/state.json"))
		for _, path := range states {
			var state struct {
				Run struct {
					PID int `json:"pid"`
				} `json:"run"`
			}
			if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &state) == nil && state.Run.PID > 0 {
				_ = syscall.Kill(-state.Run.PID, syscall.SIGKILL)
			}
		}
	})
	s.ready = true
	id := uuid.NewString()
	if w := request(s, "POST", "/api/mobile/cli/sessions", s.origin, `{"id":"`+id+`","workspace":"`+project+`","provider":"codex"}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	path := "/api/mobile/cli/sessions/" + id
	send := func(text string) string {
		requestID := uuid.NewString()
		body, _ := json.Marshal(map[string]string{"request_id": requestID, "text": text})
		if w := request(s, "POST", path+"/messages", s.origin, string(body), true); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return requestID
	}
	wait := func(done func(mobileSession) bool) mobileSession {
		deadline := time.Now().Add(20 * time.Second)
		for {
			w := request(s, "GET", path, "", "", true)
			var view mobileSession
			if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &view) == nil && done(view) {
				return view
			}
			if time.Now().After(deadline) {
				logs, _ := exec.Command("sh", "-c", "cat "+home+"/.local/share/cage/codex-sessions/*/exec.log").CombinedOutput()
				t.Fatalf("session did not settle: %d %s\n%s", w.Code, w.Body.String(), logs)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	last := func(view mobileSession) mobileTurn { return view.Turns[len(view.Turns)-1] }
	first := send("hello")
	view := wait(func(view mobileSession) bool {
		return len(view.Turns) == 1 && last(view).Status == "completed" && view.CanSend != nil && *view.CanSend
	})
	turn := last(view)
	if turn.ID != first || turn.Prompt != "hello" || turn.Text != "reply: hello" || len(turn.Tools) != 1 || turn.Tools[0].Name != "exec_command" || turn.Tools[0].Detail != "ls" || turn.Tools[0].Status != "completed" {
		t.Fatalf("first turn: %#v", turn)
	}
	record, _ := s.mobileCLI.record(s.profile, id)
	if !record.Resume || !validCLIUUID(record.CLIID) || record.Title != "hello" {
		t.Fatalf("thread not bound: %#v", record)
	}
	thread := record.CLIID
	if sessions, notice := codexSessions(t, s); notice != "" || len(sessions) != 1 || sessions[0].ID != id || sessions[0].Provider != codexProvider || sessions[0].Running {
		t.Fatal("listing", sessions, notice)
	}
	second := send("again")
	view = wait(func(view mobileSession) bool { return len(view.Turns) == 2 && last(view).Status == "completed" })
	if last(view).ID != second || last(view).Text != "reply: again" || view.Turns[0].ID != first {
		t.Fatalf("resumed turn: %#v", view.Turns)
	}
	if record, _ = s.mobileCLI.record(s.profile, id); record.CLIID != thread {
		t.Fatal("resume changed the thread")
	}
	if err := os.WriteFile(filepath.Join(home, "fake-sleep"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	third := send("long task")
	view = wait(func(view mobileSession) bool {
		return len(view.Turns) == 3 && last(view).ID == third && last(view).Status == "running"
	})
	if view.CanSend == nil || *view.CanSend || !view.Active {
		t.Fatalf("running turn: %#v", view)
	}
	body, _ := json.Marshal(map[string]string{"request_id": uuid.NewString(), "text": "too soon"})
	if w := request(s, "POST", path+"/messages", s.origin, string(body), true); w.Code != 409 {
		t.Fatal("second message during a running turn", w.Code, w.Body.String())
	}
	if w := request(s, "POST", path+"/stop", s.origin, `{"request_id":"`+third+`"}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	wait(func(view mobileSession) bool {
		return last(view).Status == "stopped" && view.CanSend != nil && *view.CanSend
	})
}
