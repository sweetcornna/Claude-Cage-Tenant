package mobilelink

import (
	"net/url"
	"strings"
	"testing"
)

func TestSnapshotPollingQuery(t *testing.T) {
	const id = "12345678-1234-4321-aaaa-123456789abc"
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"", true}, {"after=0", true}, {"after=18446744073709551615", true},
		{"after=2&epoch=" + id, true}, {"epoch=" + id + "&after=2", true},
		{"epoch=" + id, false}, {"after=2&epoch=", false}, {"after=2&epoch=" + strings.ToUpper(id), false},
		{"after=2&epoch=12345678-1234-5321-aaaa-123456789abc", false},
		{"after=2&epoch=12345678-1234-4321-0aaa-123456789abc", false},
		{"after=-1&epoch=" + id, false}, {"after=0&after=1", false}, {"after=1&other=x", false},
		{"after=1&epoch=" + id + "&epoch=" + id, false}, {"after=%31&epoch=" + id, false},
		{"after=18446744073709551616", false},
	} {
		u := &url.URL{Path: "/api/mobile/cli/sessions/" + id, RawQuery: tc.query}
		if got := allowed("GET", u); got != tc.want {
			t.Errorf("poll query %q allowed=%v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestBrowserFrameQuery(t *testing.T) {
	const tag = "b542cd94286a2489bc2df1241b043c76"
	const id = "12345678-1234-4321-aaaa-123456789abc"
	for _, tc := range []struct {
		method, surface, operation, query string
		want                              bool
	}{
		{"GET", "browser", "frame", "after=" + tag, true},
		{"GET", "browser", "frame", "after=" + tag + "&after=" + tag, false},
		{"GET", "browser", "frame", "after=" + strings.ToUpper(tag), false},
		{"POST", "browser", "action", "after=" + tag, false},
		{"GET", "desktop", "frame", "after=" + tag, false},
	} {
		u := &url.URL{Path: "/api/mobile/" + tc.surface + "/" + id + "/" + tc.operation, RawQuery: tc.query}
		if allowed(tc.method, u) != tc.want {
			t.Errorf("unexpected result for %s %s", tc.method, u.RequestURI())
		}
	}
}

func TestSessionListMutations(t *testing.T) {
	for _, operation := range []string{"metadata", "delete", "archive", "restore", "purge"} {
		u := &url.URL{Path: "/api/mobile/cli/sessions/12345678-1234-4321-aaaa-123456789abc/" + operation}
		if !allowed("POST", u) || allowed("GET", u) || allowed("DELETE", u) {
			t.Fatal("mutation method boundary", operation)
		}
		u.RawQuery = "after=1"
		if allowed("POST", u) {
			t.Fatal("mutation query accepted", operation)
		}
	}
}

func TestArchivedSessionQuery(t *testing.T) {
	u := &url.URL{Path: "/api/mobile/cli/sessions", RawQuery: "archived=true"}
	if !allowed("GET", u) || allowed("POST", u) {
		t.Fatal("archive query method")
	}
	for _, query := range []string{"archived=false", "archived=true&other=1", "archived=true&archived=true"} {
		u.RawQuery = query
		if allowed("GET", u) {
			t.Fatal("unexpected archive query", query)
		}
	}
}

func TestPhoneControlRoutes(t *testing.T) {
	const id = "0123456789abcdef"
	for _, tc := range []struct {
		method, path, query string
		want                bool
	}{
		{"GET", "/api/mobile/environment", "", true},
		{"POST", "/api/mobile/environment", "", false},
		{"GET", "/api/mobile/environment", "x=1", false},
		{"POST", "/api/mobile/environment/start", "", true},
		{"POST", "/api/mobile/environment/stop", "", true},
		{"POST", "/api/mobile/environment/restart", "", true},
		{"GET", "/api/mobile/environment/restart", "", false},
		{"POST", "/api/mobile/environment/update", "", false},
		{"GET", "/api/mobile/environment/proxy", "", true},
		{"POST", "/api/mobile/environment/proxy", "", false},
		{"POST", "/api/mobile/environment/proxy/select", "", true},
		{"POST", "/api/mobile/environment/proxy/delay", "", true},
		{"POST", "/api/mobile/environment/proxy/import", "", false},
		{"POST", "/api/mobile/environment/proxy/select", "node=1", false},
		{"GET", "/api/mobile/terminals", "", true},
		{"POST", "/api/mobile/terminals", "", true},
		{"DELETE", "/api/mobile/terminals", "", false},
		{"GET", "/api/mobile/terminals/" + id + "/output", "after=0", true},
		{"GET", "/api/mobile/terminals/" + id + "/output", "after=18446744073709551615", true},
		{"GET", "/api/mobile/terminals/" + id + "/output", "after=18446744073709551616", false},
		{"GET", "/api/mobile/terminals/" + id + "/output", "after=01", false},
		{"GET", "/api/mobile/terminals/" + id + "/output", "after=-1", false},
		{"GET", "/api/mobile/terminals/" + id + "/output", "after=1&after=2", false},
		{"GET", "/api/mobile/terminals/" + id + "/output", "after=1&epoch=2", false},
		{"GET", "/api/mobile/terminals/" + id + "/output", "", false},
		{"POST", "/api/mobile/terminals/" + id + "/output", "after=0", false},
		{"POST", "/api/mobile/terminals/" + id + "/input", "", true},
		{"POST", "/api/mobile/terminals/" + id + "/resize", "", true},
		{"POST", "/api/mobile/terminals/" + id + "/close", "", true},
		{"GET", "/api/mobile/terminals/" + id + "/input", "", false},
		{"POST", "/api/mobile/terminals/" + id + "/input", "after=0", false},
		{"POST", "/api/mobile/terminals/" + id + "/stream", "", false},
		{"POST", "/api/mobile/terminals/" + strings.ToUpper(id) + "/input", "", false},
		{"POST", "/api/mobile/terminals/" + id[:15] + "/input", "", false},
		{"POST", "/api/mobile/terminals/" + id + "0/input", "", false},
		{"POST", "/api/mobile/files/list", "", true},
		{"POST", "/api/mobile/files/read", "", true},
		{"POST", "/api/mobile/files/mkdir", "", true},
		{"POST", "/api/mobile/files/upload/start", "", true},
		{"POST", "/api/mobile/files/upload/chunk", "", true},
		{"POST", "/api/mobile/files/upload/finish", "", true},
		{"POST", "/api/mobile/files/upload/abort", "", true},
		{"GET", "/api/mobile/files/list", "", false},
		{"POST", "/api/mobile/files/delete", "", false},
		{"POST", "/api/mobile/files/upload", "", false},
		{"POST", "/api/mobile/files/read", "path=x", false},
	} {
		u := &url.URL{Path: tc.path, RawQuery: tc.query}
		if got := allowed(tc.method, u); got != tc.want {
			t.Errorf("%s %s allowed=%v, want %v", tc.method, u.RequestURI(), got, tc.want)
		}
	}
	if allowed("GET", &url.URL{Path: "/api/mobile/environment", ForceQuery: true}) {
		t.Fatal("empty query accepted")
	}
}
