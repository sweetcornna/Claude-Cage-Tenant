package tenant

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const assetStamp = "Mon, 02 Jan 2006 15:04:05 GMT"

// assetGuest models the workbench's static handler: hashed assets with a
// cache policy, a validator and an optional gzip variant.
func assetGuest(id string) http.Handler {
	base := fixtureGuest(id)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policies := map[string]string{
			"/assets/app-abc.js":    "public, max-age=31536000, immutable",
			"/assets/old-abc.js":    "no-store",
			"/assets/odd-abc.js":    "max-age=60, s-maxage=1, foo",
			"/assets/huge-abc.js":   "public, max-age=99999999999",
			"/assets/silent-abc.js": "",
			"/assets/revalidate.js": "no-cache",
			"/index.html":           "public, max-age=31536000",
			"/api/assets/app.js":    "public, max-age=31536000",
			"/assets/abort.js":      "public, max-age=31536000",
		}
		policy, known := policies[r.URL.Path]
		if !known || r.Header.Get("Cookie") != guestCookie+"="+fixtureSession {
			base.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/assets/abort.js" {
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		if policy != "" {
			w.Header().Set("Cache-Control", policy)
		}
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", assetStamp)
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("X-Guest-Accept-Encoding", r.Header.Get("Accept-Encoding"))
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && r.URL.Path == "/assets/app-abc.js" {
			var packed bytes.Buffer
			compressor := gzip.NewWriter(&packed)
			_, _ = compressor.Write([]byte("console.log(1)"))
			_ = compressor.Close()
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(packed.Bytes())
			return
		}
		_, _ = io.WriteString(w, "console.log(1)")
	})
}

func (f *portalFixture) asset(method, path string, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Host = strings.TrimPrefix(vmOrigin(f.workspaceID()), "https://")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return f.serve(request)
}

func (f *portalFixture) workspaceID() string {
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	for id := range f.server.workspaces {
		return id
	}
	f.t.Fatal("no workspace")
	return ""
}

func TestTenantForwardsCachingOfHashedAssetsOnly(t *testing.T) {
	fixture := newFixture(t, false)
	fixture.runtime.guestHandler = assetGuest
	alice, _ := fixture.login("register", "alice")
	record := fixture.create(alice, "workspace")
	cookie := fixture.connect(alice, record.ID)

	// A hashed asset keeps the workbench's policy, as one header, and becomes
	// private to the browser: a shared cache in front must not store it.
	response := fixture.asset("GET", "/assets/app-abc.js", cookie, map[string]string{"Accept-Encoding": "gzip, br"})
	if response.Code != 200 || len(response.Header().Values("Cache-Control")) != 1 || response.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("asset: HTTP %d, Cache-Control %q", response.Code, response.Header().Values("Cache-Control"))
	}
	if response.Header().Get("ETag") != `"abc"` || response.Header().Get("Last-Modified") != assetStamp || response.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("asset validators: %v", response.Header())
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("X-Guest-Accept-Encoding") != "" {
		t.Fatalf("gateway headers changed or a guest header leaked: %v", response.Header())
	}
	// The browser's own Accept-Encoding reaches the workbench and a compressed
	// answer is passed on as it is.
	if reader, err := gzip.NewReader(response.Body); response.Header().Get("Content-Encoding") != "gzip" || err != nil {
		t.Fatalf("the asset was not passed on compressed: %q, %v", response.Header().Get("Content-Encoding"), err)
	} else if plain, _ := io.ReadAll(reader); string(plain) != "console.log(1)" {
		t.Fatalf("compressed asset decodes to %q", plain)
	}

	// Revalidation: the validator reaches the workbench and its 304 keeps the
	// policy.
	response = fixture.asset("GET", "/assets/app-abc.js", cookie, map[string]string{"If-None-Match": `"abc"`})
	if response.Code != 304 || response.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" || response.Header().Get("ETag") != `"abc"` {
		t.Fatalf("revalidation: HTTP %d %v", response.Code, response.Header())
	}

	// A client that does not accept compression gets the plain asset: the
	// gateway's own transport asks for gzip and decodes it, as before.
	response = fixture.asset("GET", "/assets/app-abc.js", cookie, nil)
	if response.Header().Get("Content-Encoding") != "" || response.Body.String() != "console.log(1)" {
		t.Fatalf("plain asset: %q %q", response.Header().Get("Content-Encoding"), response.Body.String())
	}

	if response = fixture.asset("HEAD", "/assets/app-abc.js", cookie, nil); response.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("HEAD: %v", response.Header())
	}
	if response = fixture.asset("GET", "/assets/huge-abc.js", cookie, nil); response.Header().Get("Cache-Control") != "private, max-age=31536000" {
		t.Fatalf("a very long lifetime must be capped: %q", response.Header().Get("Cache-Control"))
	}
	if response = fixture.asset("GET", "/assets/revalidate.js", cookie, nil); response.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("no-cache: %q", response.Header().Get("Cache-Control"))
	}

	// Everything else stays uncacheable: an older workbench, an unknown policy,
	// no policy, other paths, other methods, and the gateway's own failures.
	for _, path := range []string{"/assets/old-abc.js", "/assets/odd-abc.js", "/assets/silent-abc.js", "/index.html", "/api/assets/app.js"} {
		if response = fixture.asset("GET", path, cookie, nil); response.Code != 200 || response.Header().Values("Cache-Control")[0] != "no-store" || len(response.Header().Values("Cache-Control")) != 1 || response.Header().Get("ETag") != "" {
			t.Fatalf("%s: HTTP %d, Cache-Control %q, ETag %q", path, response.Code, response.Header().Values("Cache-Control"), response.Header().Get("ETag"))
		}
	}
	if response = fixture.asset("POST", "/assets/app-abc.js", cookie, nil); len(response.Header().Values("Cache-Control")) != 1 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("POST: %q", response.Header().Values("Cache-Control"))
	}
	wantStatus(t, fixture.asset("GET", "/assets/abort.js", cookie, nil), 503, "runtime_unavailable")
	wantStatus(t, fixture.asset("GET", "/assets/app-abc.js", nil, nil), 401, "unauthenticated")
}

func TestAssetCacheControlAcceptsOnlyWhatItUnderstands(t *testing.T) {
	for _, test := range []struct {
		values []string
		want   string
		ok     bool
	}{
		{[]string{"public, max-age=31536000, immutable"}, "private, max-age=31536000, immutable", true},
		{[]string{"Max-Age=60", "Must-Revalidate"}, "max-age=60, must-revalidate", true},
		{[]string{"private, private"}, "private", true},
		{[]string{"max-age=99999999999999999999999"}, "max-age=31536000", true},
		{[]string{"no-cache"}, "no-cache", true},
		{[]string{"no-store"}, "", false},
		{[]string{"max-age=60, no-store"}, "", false},
		{[]string{"max-age="}, "", false},
		{[]string{"max-age=-1"}, "", false},
		{[]string{"max-age=1.5"}, "", false},
		{[]string{"s-maxage=60"}, "", false},
		{[]string{"max-age=60, community=x"}, "", false},
		{[]string{""}, "", false},
		{nil, "", false},
	} {
		got, ok := assetCacheControl(test.values)
		if got != test.want || ok != test.ok {
			t.Errorf("assetCacheControl(%q) = %q, %v; want %q, %v", test.values, got, ok, test.want, test.ok)
		}
	}
}
