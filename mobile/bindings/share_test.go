package cagemobile

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeSharedFileAcceptsExport(t *testing.T) {
	file, err := DecodeSharedFile("  会话记录.md ", "Text/Markdown; charset=utf-8", base64.StdEncoding.EncodeToString([]byte("# 标题\n")))
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != "会话记录.md" || file.Mime != "text/markdown" || string(file.Data) != "# 标题\n" {
		t.Fatalf("unexpected file %+v", file)
	}
}

func TestDecodeSharedFileDefaultsToBinary(t *testing.T) {
	file, err := DecodeSharedFile("data.bin", "", "")
	if err != nil || file.Mime != "application/octet-stream" || len(file.Data) != 0 {
		t.Fatalf("file %+v, err %v", file, err)
	}
}

func TestDecodeSharedFileRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", "  ", ".", "..", "../notes.txt", "dir/notes.txt", `dir\notes.txt`, "a\x00b", "line\nbreak", strings.Repeat("a", 256), "\xff.txt"} {
		if _, err := DecodeSharedFile(name, "text/plain", "aGk="); err == nil || !strings.Contains(err.Error(), "文件名") {
			t.Errorf("name %q: err %v", name, err)
		}
	}
}

func TestDecodeSharedFileRejectsInvalidMediaTypes(t *testing.T) {
	for _, kind := range []string{"text", "text/", "/plain", "text/plain/extra", "text plain", strings.Repeat("a", 64) + "/" + strings.Repeat("b", 64)} {
		if _, err := DecodeSharedFile("a.txt", kind, "aGk="); err == nil || !strings.Contains(err.Error(), "类型") {
			t.Errorf("type %q: err %v", kind, err)
		}
	}
}

func TestDecodeSharedFileRejectsInvalidData(t *testing.T) {
	for _, data := range []string{"aGk", "a#k=", "data:text/plain;base64,aGk=", "aGl="} {
		if _, err := DecodeSharedFile("a.txt", "text/plain", data); err == nil || !strings.Contains(err.Error(), "内容") {
			t.Errorf("data %q: err %v", data, err)
		}
	}
}

func TestDecodeSharedFileLimitsSize(t *testing.T) {
	limit := bytes.Repeat([]byte{7}, MaxSharedFileBytes)
	file, err := DecodeSharedFile("limit.bin", "application/octet-stream", base64.StdEncoding.EncodeToString(limit))
	if err != nil || len(file.Data) != MaxSharedFileBytes {
		t.Fatalf("file at the limit: %v", err)
	}
	over := base64.StdEncoding.EncodeToString(append(limit, 7))
	if _, err := DecodeSharedFile("over.bin", "application/octet-stream", over); err == nil || !strings.Contains(err.Error(), "8 MB") {
		t.Fatalf("file over the limit: %v", err)
	}
	if _, err := DecodeSharedFile("huge.bin", "", strings.Repeat("A", 32<<20)); err == nil || !strings.Contains(err.Error(), "8 MB") {
		t.Fatalf("oversized base64 must be refused before decoding: %v", err)
	}
}
