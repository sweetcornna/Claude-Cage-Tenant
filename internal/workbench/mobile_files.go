package workbench

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/sylvinus/agent-vm/internal/mounts"
	"github.com/sylvinus/agent-vm/internal/paths"
)

const (
	mobileFileEntries = 1000
	mobileFileScan    = 20000
	mobileFileChunk   = 2 << 20
)

// mobileFileError is a reply the phone shows as it is.
type mobileFileError struct {
	status  int
	message string
}

func (e *mobileFileError) Error() string { return e.message }

func fileFailure(status int, message string) error { return &mobileFileError{status, message} }

func replyFileError(w http.ResponseWriter, err error) {
	var failure *mobileFileError
	if !errors.As(err, &failure) {
		failure = &mobileFileError{http.StatusConflict, "文件操作失败，请重试"}
	}
	http.Error(w, failure.message, failure.status)
}

// mobileProjectRoot opens a granted host project folder. The phone may name
// any folder of the project list, never a client mount, a moved folder or a
// folder that is no longer a valid project.
func (s *Server) mobileProjectRoot(project string) (*os.Root, error) {
	if s.windowsGuest != nil && s.windowsGuest.Remote() {
		return nil, fileFailure(http.StatusConflict, "此客机暂不支持在手机上管理项目文件")
	}
	if project == "" || len(project) > 4096 || !utf8.ValidString(project) {
		return nil, fileFailure(http.StatusBadRequest, "请选择项目")
	}
	if isClientMountPath(project) {
		return nil, fileFailure(http.StatusConflict, "本机共享的项目不能在手机上管理文件")
	}
	granted, err := s.projectPaths()
	if err != nil {
		return nil, fileFailure(http.StatusInternalServerError, "无法读取已授权的项目")
	}
	if !slices.Contains(granted, project) {
		return nil, fileFailure(http.StatusNotFound, "项目未授权或已移除")
	}
	if _, err := validateWorkspace(project, s.profile); err != nil {
		return nil, fileFailure(http.StatusConflict, err.Error())
	}
	canonical, err := paths.Real(project)
	if err != nil || !paths.Equal(canonical, paths.Host(project)) {
		return nil, fileFailure(http.StatusConflict, "项目目录已变化，请重新打开项目。")
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fileFailure(http.StatusNotFound, "项目目录暂时不可用。")
	}
	return root, nil
}

// mobileRelative splits a slash-separated path below the project into names.
// "" is the project root.
func mobileRelative(rel string) ([]string, error) {
	if rel == "" {
		return nil, nil
	}
	if len(rel) > 4096 || !utf8.ValidString(rel) || !fs.ValidPath(rel) || strings.ContainsFunc(rel, unicode.IsControl) {
		return nil, fileFailure(http.StatusBadRequest, "路径无效")
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if len(part) > 255 {
			return nil, fileFailure(http.StatusBadRequest, "路径无效")
		}
		if _, err := filepath.Localize(part); err != nil {
			return nil, fileFailure(http.StatusBadRequest, "路径无效")
		}
	}
	return parts, nil
}

// openChild opens one directory below parent. A symbolic link is never
// followed: the name must be a directory itself, and the directory opened
// must be the one that was inspected.
func openChild(parent *os.Root, name string, create bool) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if create && errors.Is(err, fs.ErrNotExist) {
		if err = parent.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fileFailure(http.StatusConflict, "无法创建文件夹，请检查权限和磁盘空间")
		}
		info, err = parent.Lstat(name)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fileFailure(http.StatusNotFound, "文件夹不存在或已移动")
	}
	if err != nil {
		return nil, fileFailure(http.StatusConflict, "无法读取文件夹")
	}
	if !info.IsDir() {
		return nil, fileFailure(http.StatusConflict, "路径中有同名的文件或链接")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fileFailure(http.StatusConflict, "无法打开文件夹")
	}
	if opened, err := child.Stat("."); err != nil || !os.SameFile(info, opened) {
		child.Close()
		return nil, fileFailure(http.StatusConflict, "文件夹已变化，请重试")
	}
	return child, nil
}

// openDirectory opens the directory named by parts without following links.
func openDirectory(root *os.Root, parts []string, create bool) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, fileFailure(http.StatusNotFound, "项目目录暂时不可用。")
	}
	for _, part := range parts {
		next, err := openChild(current, part, create)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

// writableName refuses version-control metadata, which a share keeps read-only.
func writableName(rel string) error {
	if mounts.UnderName(rel, nil) {
		return fileFailure(http.StatusBadRequest, "不能写入 Git 等版本控制目录")
	}
	return nil
}

type mobileFileRequest struct {
	Project string `json:"project"`
	Path    string `json:"path"`
}

type mobileFileEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

func fileType(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "dir"
	case mode.IsRegular():
		return "file"
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	}
	return "other"
}

func (s *Server) mobileFileList(w http.ResponseWriter, r *http.Request) {
	var in mobileFileRequest
	if !readJSON(w, r, &in, 16<<10) {
		return
	}
	entries, truncated, err := s.listProjectDirectory(in.Project, in.Path)
	if err != nil {
		replyFileError(w, err)
		return
	}
	jsonReply(w, map[string]any{"project": in.Project, "path": in.Path, "entries": entries, "truncated": truncated})
}

func (s *Server) listProjectDirectory(project, rel string) ([]mobileFileEntry, bool, error) {
	parts, err := mobileRelative(rel)
	if err != nil {
		return nil, false, err
	}
	root, err := s.mobileProjectRoot(project)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	dir, err := openDirectory(root, parts, false)
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	f, err := dir.Open(".")
	if err != nil {
		return nil, false, fileFailure(http.StatusConflict, "无法读取文件夹")
	}
	defer f.Close()
	var items []fs.DirEntry
	truncated := false
	for {
		batch, err := f.ReadDir(256)
		for _, item := range batch {
			// A name the phone cannot send back is not listed.
			if name := item.Name(); utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl) {
				items = append(items, item)
			}
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, false, fileFailure(http.StatusConflict, "无法读取文件夹")
		}
		if len(items) >= mobileFileScan {
			truncated = true
			break
		}
	}
	slices.SortFunc(items, func(a, b fs.DirEntry) int {
		if a.IsDir() != b.IsDir() {
			if a.IsDir() {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name())); c != 0 {
			return c
		}
		return strings.Compare(a.Name(), b.Name())
	})
	if len(items) > mobileFileEntries {
		items, truncated = items[:mobileFileEntries], true
	}
	entries := []mobileFileEntry{}
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		entry := mobileFileEntry{Name: item.Name(), Type: fileType(info.Mode()), MTime: info.ModTime().UnixMilli()}
		if entry.Type == "file" {
			entry.Size = info.Size()
		}
		entries = append(entries, entry)
	}
	return entries, truncated, nil
}

func fileVersion(info fs.FileInfo) string {
	return fmt.Sprintf("%x-%x", info.Size(), info.ModTime().UnixNano())
}

// openProjectFile opens a regular file without following a link. O_NONBLOCK
// keeps a file swapped for a FIFO from holding the request.
func openProjectFile(root *os.Root, parts []string) (*os.File, fs.FileInfo, error) {
	if len(parts) == 0 {
		return nil, nil, fileFailure(http.StatusBadRequest, "请选择文件")
	}
	dir, err := openDirectory(root, parts[:len(parts)-1], false)
	if err != nil {
		return nil, nil, err
	}
	defer dir.Close()
	name := parts[len(parts)-1]
	info, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fileFailure(http.StatusNotFound, "文件已移动或删除")
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, fileFailure(http.StatusConflict, "只能读取普通文件")
	}
	file, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, fileFailure(http.StatusConflict, "文件无法读取")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		return nil, nil, fileFailure(http.StatusConflict, "文件已变化，请重新读取")
	}
	return file, opened, nil
}

func (s *Server) mobileFileRead(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project string `json:"project"`
		Path    string `json:"path"`
		Offset  int64  `json:"offset"`
		Version string `json:"version"`
	}
	if !readJSON(w, r, &in, 16<<10) {
		return
	}
	if in.Offset < 0 || (in.Offset > 0 && in.Version == "") || len(in.Version) > 64 {
		http.Error(w, "读取位置无效", http.StatusBadRequest)
		return
	}
	parts, err := mobileRelative(in.Path)
	if err != nil {
		replyFileError(w, err)
		return
	}
	root, err := s.mobileProjectRoot(in.Project)
	if err != nil {
		replyFileError(w, err)
		return
	}
	defer root.Close()
	file, info, err := openProjectFile(root, parts)
	if err != nil {
		replyFileError(w, err)
		return
	}
	defer file.Close()
	version := fileVersion(info)
	if in.Version != "" && in.Version != version {
		http.Error(w, "文件已变化，请重新读取", http.StatusConflict)
		return
	}
	if in.Offset > info.Size() {
		http.Error(w, "读取位置无效", http.StatusBadRequest)
		return
	}
	var head [512]byte
	n, err := file.ReadAt(head[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "文件无法读取", http.StatusConflict)
		return
	}
	chunk := make([]byte, min(int64(mobileFileChunk), info.Size()-in.Offset))
	if read, err := file.ReadAt(chunk, in.Offset); read != len(chunk) || (err != nil && !errors.Is(err, io.EOF)) {
		http.Error(w, "文件已变化，请重新读取", http.StatusConflict)
		return
	}
	if end, err := file.Stat(); err != nil || fileVersion(end) != version {
		http.Error(w, "文件已变化，请重新读取", http.StatusConflict)
		return
	}
	jsonReply(w, map[string]any{"size": info.Size(), "version": version, "mime": http.DetectContentType(head[:n]), "offset": in.Offset, "data": chunk})
}

func (s *Server) mobileFileMkdir(w http.ResponseWriter, r *http.Request) {
	var in mobileFileRequest
	if !readJSON(w, r, &in, 16<<10) {
		return
	}
	parts, err := mobileRelative(in.Path)
	if err == nil {
		err = writableName(in.Path)
	}
	if err != nil {
		replyFileError(w, err)
		return
	}
	if !s.mobileAllowed(w, r) {
		return
	}
	root, err := s.mobileProjectRoot(in.Project)
	if err != nil {
		replyFileError(w, err)
		return
	}
	defer root.Close()
	dir, err := openDirectory(root, parts, true)
	if err != nil {
		replyFileError(w, err)
		return
	}
	dir.Close()
	w.WriteHeader(http.StatusNoContent)
}
