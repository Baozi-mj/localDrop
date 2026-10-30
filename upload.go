package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Dropped files. A browser never reveals where a dragged file lives on disk,
// only its bytes, so drag-and-drop has to stage a copy. The copy travels over
// loopback (fast) into a per-process temp root, is served like any other
// file, and is removed when its session stops or the process exits. Files
// added through the chooser still stream from their original location.

var (
	batchIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)
	errBadRel = errors.New("无效的文件路径")
)

// uploadHeader must accompany every upload. Browsers only send custom
// headers after a CORS preflight, which a foreign origin never passes.
const uploadHeader = "X-LocalDrop-Upload"

type uploads struct {
	mu   sync.Mutex
	root string
}

func (u *uploads) ensureRoot() (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.root != "" {
		return u.root, nil
	}
	dir, err := os.MkdirTemp("", "localdrop-drop-")
	if err != nil {
		return "", err
	}
	u.root = dir
	return dir, nil
}

// Root returns the staging root, or "" if nothing was staged yet.
func (u *uploads) Root() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.root
}

// Close removes every staged file.
func (u *uploads) Close() {
	u.mu.Lock()
	root := u.root
	u.root = ""
	u.mu.Unlock()
	if root != "" {
		_ = os.RemoveAll(root)
	}
}

// batchDir validates a client batch id and returns its directory.
func (u *uploads) batchDir(id string) (string, error) {
	if !batchIDRe.MatchString(id) {
		return "", errors.New("无效的批次标识")
	}
	root, err := u.ensureRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

// batchOf reports which batch directory a staged path belongs to, if any.
func (u *uploads) batchOf(p string) (string, bool) {
	root := u.Root()
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	if !batchIDRe.MatchString(first) {
		return "", false
	}
	return filepath.Join(root, first), true
}

// cleanRel validates a client-supplied relative path: slash separated, no
// absolute, no empty or dot segments, no traversal, no control characters.
func cleanRel(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", errBadRel
	}
	segs := strings.Split(rel, "/")
	if len(segs) > maxWalkDepth+1 {
		return "", errBadRel
	}
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return "", errBadRel
		}
		for _, c := range seg {
			if c < 0x20 || c == 0x7f {
				return "", errBadRel
			}
		}
	}
	return filepath.Join(segs...), nil
}

// noteTemp records, on the session, every staged batch the given paths come
// from, so stopping the session deletes the copies.
func (s *server) noteTemp(sess *Session, paths []string) {
	for _, p := range paths {
		if dir, ok := s.uploads.batchOf(filepath.Clean(p)); ok {
			sess.noteTempDir(dir)
		}
	}
}

// handleUpload: POST /api/upload?batch=<id>&rel=<path>, raw body. Streams the
// body to <root>/<batch>/<rel> and returns that absolute path.
func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(uploadHeader) == "" {
		writeErr(w, http.StatusBadRequest, "缺少上传标记")
		return
	}
	q := r.URL.Query()
	dir, err := s.uploads.batchDir(q.Get("batch"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rel, err := cleanRel(q.Get("rel"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dst := filepath.Join(dir, rel)
	if inside, err := filepath.Rel(dir, dst); err != nil || inside == "." || strings.HasPrefix(inside, "..") {
		writeErr(w, http.StatusBadRequest, errBadRel.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "无法创建临时目录")
		return
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "无法创建临时文件")
		return
	}
	n, copyErr := io.Copy(f, r.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dst)
		writeErr(w, http.StatusInternalServerError, "写入临时文件失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dst, "batchDir": dir, "size": n})
}

// handleUploadDiscard: POST {"batch":id} deletes a staged batch that no live
// session uses (the user removed it from the selection before sharing).
func (s *server) handleUploadDiscard(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req struct {
		Batch string `json:"batch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	dir, err := s.uploads.batchDir(req.Batch)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if sess := s.store.Active(); sess != nil && !sess.isStopped() && sess.usesTempDir(dir) {
		writeErr(w, http.StatusConflict, "该批文件正在投送中")
		return
	}
	_ = os.RemoveAll(dir)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
