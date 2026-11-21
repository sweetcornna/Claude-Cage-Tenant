package workbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestMobileEnvironmentStatusOmitsSecrets(t *testing.T) {
	s := testServer(t)
	s.setJobForTest(Job{ID: "job1", Kind: "start", Running: true, Message: "正在启动", Log: "private task log /Users/me"})
	s.safety = safetyState{Gate: &cage.GateStatus{Mode: cage.GateOpen, Until: time.Now().Add(time.Hour), Reason: "已检查"}, ExitIP: "203.0.113.9"}
	w := request(s, "GET", "/api/mobile/environment", "", "", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"secret", "socks5", "private task log", "proxy_url"} {
		if strings.Contains(body, secret) {
			t.Fatalf("environment view exposes %q: %s", secret, body)
		}
	}
	var view struct {
		Ready, VMRunning, WorkloadReady bool
		GuestSystem                     string `json:"guest_system"`
		Job                             *mobileJobView
		Network                         struct {
			Mode, Reason string
			ExitIP       string `json:"exit_ip"`
		}
		Proxy struct {
			State        string
			Selected     string
			SelectedName string `json:"selected_name"`
			Route        []string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Job == nil || view.Job.ID != "job1" || !view.Job.Running || view.GuestSystem != "debian-13" {
		t.Fatalf("job or system: %s", body)
	}
	// A stopped guest has no open grant, whatever the last published gate said.
	if view.Network.Mode != "closed" || view.Network.Reason != "已检查" || view.Network.ExitIP != "203.0.113.9" {
		t.Fatalf("network: %s", body)
	}
	if view.Proxy.SelectedName != "qa" || view.Proxy.Selected == "" || view.Proxy.State == "" {
		t.Fatalf("proxy: %s", body)
	}
	s.setJobForTest(Job{})
	if w := request(s, "GET", "/api/mobile/environment", "", "", true); !strings.Contains(w.Body.String(), `"job":null`) {
		t.Fatalf("no job: %s", w.Body.String())
	}
}

func TestMobileNetworkMode(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		running bool
		gate    *cage.GateStatus
		want    string
	}{
		{true, &cage.GateStatus{Mode: cage.GateOpen, Until: now.Add(time.Minute)}, "open"},
		{true, &cage.GateStatus{Mode: cage.GateChecking, Until: now.Add(time.Minute)}, "checking"},
		{true, &cage.GateStatus{Mode: cage.GateOpen, Until: now.Add(-time.Minute)}, "closed"},
		{false, &cage.GateStatus{Mode: cage.GateOpen, Until: now.Add(time.Minute)}, "closed"},
		{true, nil, "closed"},
	} {
		if got := mobileNetwork(tc.running, safetyState{Gate: tc.gate}, now)["mode"]; got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
}

func TestMobileEnvironmentOperationsShareAdmission(t *testing.T) {
	s := testServer(t)
	s.setJobForTest(Job{ID: "busy", Kind: "settings", Running: true})
	for _, path := range []string{"start", "restart"} {
		w := request(s, "POST", "/api/mobile/environment/"+path, s.origin, "{}", true)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "另一个操作仍在进行") {
			t.Fatalf("%s while busy: %d %s", path, w.Code, w.Body.String())
		}
		if w := request(s, "POST", "/api/mobile/environment/"+path, s.origin, `{"guest_system":"macos"}`, true); w.Code != 400 {
			t.Fatalf("%s with install input: %d", path, w.Code)
		}
	}
	s.setJobForTest(Job{})
	s.account.lock()
	if w := request(s, "POST", "/api/mobile/environment/stop", s.origin, "{}", true); w.Code != 423 {
		t.Fatalf("locked stop: %d", w.Code)
	}
}

// A restart is one admitted operation that requests networking like start.
func TestRestartIsOneJob(t *testing.T) {
	s := testServer(t)
	s.mu.Lock()
	s.networkPaused = true
	s.mu.Unlock()
	inside := make(chan error, 1)
	release := make(chan struct{})
	job, err := s.submitJob("restart", func() error {
		_, err := s.submitJob("start", func() error { return nil })
		inside <- err
		<-release
		return networkCheckError{errors.New("出口检查未通过")}
	})
	if err != nil || job.Kind != "restart" {
		t.Fatal(job, err)
	}
	if err := <-inside; !errors.Is(err, errJobBusy) || !strings.Contains(err.Error(), "重启环境") {
		t.Fatalf("start admitted inside restart: %v", err)
	}
	s.mu.Lock()
	paused := s.networkPaused
	s.mu.Unlock()
	if paused {
		t.Fatal("restart did not request networking")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for s.jobs.snapshot().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if done := s.jobs.snapshot(); done.Running || done.Message != "环境已启动，联网检查未通过" {
		t.Fatalf("restart result: %+v", done)
	}
}

func TestMobileProxyListsOnlyNodeNames(t *testing.T) {
	s := testServer(t)
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	p.Sources[0].URL = "https://sub.example/private-token"
	p.Sources[0].Error = "Get \"https://sub.example/private-token\": timeout"
	many := managedproxy.Source{ID: "many", Name: "大量节点"}
	for i := range mobileProxyNodes + 5 {
		many.Nodes = append(many.Nodes, managedproxy.Node{ID: fmt.Sprintf("n%d", i), Name: fmt.Sprintf("节点 %d", i), Type: "socks5", Config: map[string]any{"server": "198.51.100.7", "password": "node-secret"}})
	}
	p.Sources = append(p.Sources, many)
	if err := p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/mobile/environment/proxy", "", "", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"sub.example", "private-token", "198.51.100.7", "node-secret", "secret", "config"} {
		if strings.Contains(body, secret) {
			t.Fatalf("proxy view exposes %q", secret)
		}
	}
	var view struct {
		Selected  string
		Truncated bool
		Sources   []struct {
			ID, Name, Error string
			Nodes           []map[string]string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, src := range view.Sources {
		total += len(src.Nodes)
	}
	if !view.Truncated || total != mobileProxyNodes || view.Sources[0].Error == "" || view.Selected == "" {
		t.Fatalf("nodes=%d truncated=%v error=%q", total, view.Truncated, view.Sources[0].Error)
	}
	for body, status := range map[string]int{`{"node_id":"missing"}`: 404, `{"node_id":""}`: 400, `{"node_id":"n1","port":1}`: 400} {
		if w := request(s, "POST", "/api/mobile/environment/proxy/select", s.origin, body, true); w.Code != status {
			t.Fatalf("select %s: %d", body, w.Code)
		}
	}
	if w := request(s, "POST", "/api/mobile/environment/proxy/delay", s.origin, `{"node_id":"missing"}`, true); w.Code != 404 {
		t.Fatalf("delay of missing node: %d", w.Code)
	}
	if w := request(s, "POST", "/api/mobile/environment/proxy/delay", s.origin, `{"node_id":"n1"}`, true); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"delay":0}` {
		t.Fatalf("delay without core: %d %s", w.Code, w.Body.String())
	}
}
