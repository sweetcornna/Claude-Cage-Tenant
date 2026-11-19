package fileutils

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/opencontainers/go-digest"
)

func TestPinnedLinuxImagesShareCache(t *testing.T) {
	shared, fallback := t.TempDir(), t.TempDir()
	t.Setenv("CAGE_LIMA_LINUX_IMAGE_CACHE", shared)
	t.Setenv("CAGE_LIMA_CACHE_DIR", fallback)
	var requests atomic.Int32
	var version atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprintf(w, "image-version-%d", version.Load())
	}))
	defer server.Close()

	download := func(destination string, release int) string {
		t.Helper()
		image := limatype.File{Location: server.URL, Arch: limatype.AARCH64, Digest: digest.FromString(fmt.Sprintf("image-version-%d", release))}
		cached, err := DownloadLinuxImage(t.Context(), destination, image, image.Arch, nil)
		if err != nil {
			t.Fatal(err)
		}
		if rel, err := filepath.Rel(shared, cached); err != nil || !filepath.IsLocal(rel) {
			t.Fatalf("image cached outside the shared cache: %q", cached)
		}
		return cached
	}
	first, second := filepath.Join(t.TempDir(), "disk"), filepath.Join(t.TempDir(), "disk")
	cached := download(first, 0)
	if err := os.WriteFile(first, []byte("private changes"), 0600); err != nil {
		t.Fatal(err)
	}
	if next := download(second, 0); next != cached || requests.Load() != 1 {
		t.Fatalf("same image was downloaded again: %d requests", requests.Load())
	}
	if data, err := os.ReadFile(second); err != nil || string(data) != "image-version-0" {
		t.Fatalf("second disk inherited private changes: %q, %v", data, err)
	}
	version.Store(1)
	if next := download(filepath.Join(t.TempDir(), "disk"), 1); next == cached || requests.Load() != 2 {
		t.Fatalf("different digest reused the same disk: %d requests", requests.Load())
	}
	if data, err := os.ReadFile(cached); err != nil || string(data) != "image-version-0" {
		t.Fatalf("first image was overwritten: %q, %v", data, err)
	}
}
