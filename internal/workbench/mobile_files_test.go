package workbench

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mobileFileProject(t *testing.T) (*Server, string) {
	t.Helper()
	s := testServer(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.rememberSharedProject(project); err != nil {
		t.Fatal(err)
	}
	return s, project
}

func postFiles(t *testing.T, s *Server, operation string, input any, status int) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/mobile/files/"+operation, s.origin, string(body), true)
	if w.Code != status {
		t.Fatalf("%s %s: got %d, want %d: %s", operation, body, w.Code, status, w.Body.String())
	}
	return w
}

func TestMobileFilesListAndReadWithoutFollowingLinks(t *testing.T) {
	s, project := mobileFileProject(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("host secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"src", "Beta"} {
		if err := os.Mkdir(filepath.Join(project, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	payload := bytes.Repeat([]byte("0123456789"), (mobileFileChunk/10)+3)
	for name, data := range map[string][]byte{"src/main.go": []byte("package main\n"), ".env": []byte("A=1"), "big.bin": payload, "alpha.txt": []byte("a")} {
		if err := os.WriteFile(filepath.Join(project, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(project, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(project, "secret.txt")); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Entries   []mobileFileEntry
		Truncated bool
	}
	w := postFiles(t, s, "list", mobileFileRequest{Project: project}, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range list.Entries {
		names = append(names, entry.Name+":"+entry.Type)
	}
	if got := strings.Join(names, ","); got != "Beta:dir,src:dir,.env:file,alpha.txt:file,big.bin:file,outside:symlink,secret.txt:symlink" || list.Truncated {
		t.Fatalf("listing: %s", got)
	}
	if list.Entries[4].Size != int64(len(payload)) || list.Entries[4].MTime == 0 {
		t.Fatalf("file metadata: %+v", list.Entries[4])
	}
	postFiles(t, s, "list", mobileFileRequest{Project: project, Path: "src"}, 200)
	postFiles(t, s, "list", mobileFileRequest{Project: project, Path: "outside"}, 409)
	postFiles(t, s, "list", mobileFileRequest{Project: project, Path: "missing"}, 404)
	for _, bad := range []string{"../", "..", "src/../..", "/src", "src/", "a//b", "./src", "src\x00"} {
		postFiles(t, s, "list", mobileFileRequest{Project: project, Path: bad}, 400)
	}
	postFiles(t, s, "list", mobileFileRequest{Project: outside}, 404)
	postFiles(t, s, "list", mobileFileRequest{Project: clientMountRoot + "/x"}, 409)
	postFiles(t, s, "list", mobileFileRequest{Project: project + "/src"}, 404)

	type readReply struct {
		Size    int64
		Version string
		Mime    string
		Offset  int64
		Data    []byte
	}
	read := func(path string, offset int64, version string, status int) readReply {
		t.Helper()
		var reply readReply
		w := postFiles(t, s, "read", map[string]any{"project": project, "path": path, "offset": offset, "version": version}, status)
		if status == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
		}
		return reply
	}
	first := read("big.bin", 0, "", 200)
	if len(first.Data) != mobileFileChunk || first.Size != int64(len(payload)) || first.Version == "" {
		t.Fatalf("first chunk: %d %d", len(first.Data), first.Size)
	}
	rest := read("big.bin", int64(len(first.Data)), first.Version, 200)
	if !bytes.Equal(append(first.Data, rest.Data...), payload) || rest.Offset != int64(len(first.Data)) {
		t.Fatal("chunks do not rebuild the file")
	}
	read("big.bin", 1, "", 400)
	read("big.bin", 1, "stale", 409)
	read("big.bin", int64(len(payload))+1, first.Version, 400)
	if text := read("src/main.go", 0, "", 200); text.Mime != "text/plain; charset=utf-8" || string(text.Data) != "package main\n" {
		t.Fatalf("text read: %+v", text)
	}
	read("secret.txt", 0, "", 409)
	read("outside/secret.txt", 0, "", 409)
	read("src", 0, "", 409)
	read("", 0, "", 400)
	read("gone.txt", 0, "", 404)
	if err := os.WriteFile(filepath.Join(project, "big.bin"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	read("big.bin", int64(len(first.Data)), first.Version, 409)
	s.account.lock()
	postFiles(t, s, "list", mobileFileRequest{Project: project}, 423)
}

func TestMobileFilesMkdir(t *testing.T) {
	s, project := mobileFileProject(t)
	postFiles(t, s, "mkdir", mobileFileRequest{Project: project, Path: "a/b/c"}, 204)
	if info, err := os.Stat(filepath.Join(project, "a", "b", "c")); err != nil || !info.IsDir() {
		t.Fatal("directories not created", err)
	}
	postFiles(t, s, "mkdir", mobileFileRequest{Project: project, Path: "a/b"}, 204)
	postFiles(t, s, "mkdir", mobileFileRequest{Project: project}, 204)
	if err := os.WriteFile(filepath.Join(project, "a", "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	postFiles(t, s, "mkdir", mobileFileRequest{Project: project, Path: "a/file"}, 409)
	postFiles(t, s, "mkdir", mobileFileRequest{Project: project, Path: "a/file/x"}, 409)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	postFiles(t, s, "mkdir", mobileFileRequest{Project: project, Path: "link/x"}, 409)
	if _, err := os.Stat(filepath.Join(outside, "x")); !os.IsNotExist(err) {
		t.Fatal("mkdir followed a link out of the project")
	}
	for _, blocked := range []string{".git", ".git/hooks", "a/.GIT/x", ".hg"} {
		postFiles(t, s, "mkdir", mobileFileRequest{Project: project, Path: blocked}, 400)
	}
	postFiles(t, s, "mkdir", map[string]any{"project": project, "path": "x", "mode": 7}, 400)
}

func startUpload(t *testing.T, s *Server, input map[string]any, status int) string {
	t.Helper()
	w := postFiles(t, s, "upload/start", input, status)
	var reply struct{ ID string }
	if status == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !mobileUploadID.MatchString(reply.ID) {
			t.Fatalf("upload id: %s", w.Body.String())
		}
	}
	return reply.ID
}

func uploadTemps(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".super-cage-upload-*.part"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestMobileUploadCreatesDirectoriesAndVerifiesContent(t *testing.T) {
	s, project := mobileFileProject(t)
	t.Cleanup(s.mobileUploads.close)
	payload := bytes.Repeat([]byte("phone photo "), 9000)
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	dir := filepath.Join(project, ".super-cage", "attachments")
	id := startUpload(t, s, map[string]any{"project": project, "dir": ".super-cage/attachments", "name": "图片 1.png", "size": len(payload), "overwrite": false}, 200)
	if len(uploadTemps(t, dir)) != 1 {
		t.Fatal("temporary file not created next to the destination")
	}
	postFiles(t, s, "upload/finish", map[string]any{"id": id, "sha256": digest}, 409)
	received := 0
	for offset := 0; offset < len(payload); offset += mobileUploadChunk {
		chunk := payload[offset:min(len(payload), offset+mobileUploadChunk)]
		w := postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": offset, "data": chunk}, 200)
		received += len(chunk)
		if want := `{"received":` + strings.TrimSpace(string(must(json.Marshal(received)))) + "}"; strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("chunk reply %s, want %s", w.Body.String(), want)
		}
		// The same chunk again is a retry of a lost reply.
		postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": offset, "data": chunk}, 200)
	}
	postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": 0, "data": []byte("x")}, 409)
	postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": len(payload), "data": []byte("x")}, 413)
	w := postFiles(t, s, "upload/finish", map[string]any{"id": id, "sha256": digest}, 200)
	if !strings.Contains(w.Body.String(), `"path":".super-cage/attachments/图片 1.png"`) {
		t.Fatalf("finish reply: %s", w.Body.String())
	}
	if data, err := os.ReadFile(filepath.Join(dir, "图片 1.png")); err != nil || !bytes.Equal(data, payload) {
		t.Fatal("uploaded content differs", err)
	}
	if len(uploadTemps(t, dir)) != 0 {
		t.Fatal("temporary file left after finish")
	}
	postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": len(payload), "data": []byte("x")}, 404)

	// An existing file needs an explicit overwrite; a wrong digest keeps it.
	start := map[string]any{"project": project, "dir": ".super-cage/attachments", "name": "图片 1.png", "size": 3, "overwrite": false}
	startUpload(t, s, start, 409)
	start["overwrite"] = true
	id = startUpload(t, s, start, 200)
	postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": 0, "data": []byte("new")}, 200)
	postFiles(t, s, "upload/finish", map[string]any{"id": id, "sha256": strings.Repeat("0", 64)}, 409)
	if data, _ := os.ReadFile(filepath.Join(dir, "图片 1.png")); !bytes.Equal(data, payload) || len(uploadTemps(t, dir)) != 0 {
		t.Fatal("failed upload changed the file or left its temporary file")
	}
	postFiles(t, s, "upload/finish", map[string]any{"id": id, "sha256": strings.Repeat("0", 64)}, 404)
	id = startUpload(t, s, start, 200)
	postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": 0, "data": []byte("new")}, 200)
	sum = sha256.Sum256([]byte("new"))
	postFiles(t, s, "upload/finish", map[string]any{"id": id, "sha256": hex.EncodeToString(sum[:])}, 200)
	if data, _ := os.ReadFile(filepath.Join(dir, "图片 1.png")); string(data) != "new" {
		t.Fatal("overwrite did not replace the file")
	}
	// An empty file needs no chunk.
	id = startUpload(t, s, map[string]any{"project": project, "dir": "", "name": "empty", "size": 0, "overwrite": false}, 200)
	sum = sha256.Sum256(nil)
	postFiles(t, s, "upload/finish", map[string]any{"id": id, "sha256": hex.EncodeToString(sum[:])}, 200)
	if info, err := os.Stat(filepath.Join(project, "empty")); err != nil || info.Size() != 0 {
		t.Fatal("empty upload", err)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func TestMobileUploadRefusesUnsafeTargets(t *testing.T) {
	s, project := mobileFileProject(t)
	t.Cleanup(s.mobileUploads.close)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(project, "dangling")); err != nil {
		t.Fatal(err)
	}
	base := func(dir, name string, size int64) map[string]any {
		return map[string]any{"project": project, "dir": dir, "name": name, "size": size, "overwrite": true}
	}
	startUpload(t, s, base("link", "x", 1), 409)
	startUpload(t, s, base("link/sub", "x", 1), 409)
	startUpload(t, s, base("", "folder", 1), 409)
	startUpload(t, s, base("", "dangling", 1), 409)
	for _, name := range []string{"", ".", "..", "a/b", "a\x01", strings.Repeat("n", 256)} {
		startUpload(t, s, base("", name, 1), 400)
	}
	startUpload(t, s, base("", ".git", 1), 400)
	startUpload(t, s, base(".git", "config", 1), 400)
	startUpload(t, s, base("../x", "a", 1), 400)
	startUpload(t, s, base("", "big", mobileUploadLimit+1), 413)
	startUpload(t, s, base("", "neg", -1), 413)
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("upload reached outside the project")
	}
	postFiles(t, s, "upload/chunk", map[string]any{"id": strings.Repeat("a", 32), "offset": 0, "data": []byte("x")}, 404)
	postFiles(t, s, "upload/chunk", map[string]any{"id": "bad", "offset": 0, "data": []byte("x")}, 400)
	id := startUpload(t, s, base("", "big-chunk", mobileUploadChunk+1), 200)
	postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": 0, "data": make([]byte, mobileUploadChunk+1)}, 413)
	postFiles(t, s, "upload/abort", map[string]any{"id": id}, 204)
	postFiles(t, s, "upload/abort", map[string]any{"id": id}, 204)
	if len(uploadTemps(t, project)) != 0 {
		t.Fatal("abort left the temporary file")
	}
}

func TestMobileUploadLimitsAndExpiry(t *testing.T) {
	s, project := mobileFileProject(t)
	t.Cleanup(s.mobileUploads.close)
	for i := range mobileUploadSlots {
		startUpload(t, s, map[string]any{"project": project, "dir": "", "name": "f" + string(rune('a'+i)), "size": 1, "overwrite": false}, 200)
	}
	startUpload(t, s, map[string]any{"project": project, "dir": "", "name": "extra", "size": 1, "overwrite": false}, 409)
	if len(uploadTemps(t, project)) != mobileUploadSlots {
		t.Fatal("refused upload left a temporary file")
	}
	s.mobileUploads.close()
	if len(uploadTemps(t, project)) != 0 {
		t.Fatal("service close left temporary files")
	}
	startUpload(t, s, map[string]any{"project": project, "dir": "", "name": "late", "size": 1, "overwrite": false}, 503)

	s, project = mobileFileProject(t)
	t.Cleanup(s.mobileUploads.close)
	previous := mobileUploadIdle
	mobileUploadIdle = 30 * time.Millisecond
	t.Cleanup(func() { mobileUploadIdle = previous })
	id := startUpload(t, s, map[string]any{"project": project, "dir": "", "name": "idle", "size": 2, "overwrite": false}, 200)
	deadline := time.Now().Add(5 * time.Second)
	for len(uploadTemps(t, project)) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(uploadTemps(t, project)) != 0 {
		t.Fatal("idle upload kept its temporary file")
	}
	postFiles(t, s, "upload/chunk", map[string]any{"id": id, "offset": 0, "data": []byte("x")}, 404)
	s.account.lock()
	startUpload(t, s, map[string]any{"project": project, "dir": "", "name": "locked", "size": 1, "overwrite": false}, 423)
}
