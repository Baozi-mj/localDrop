package main

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type fsEntryJSON struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

type shortcutJSON struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

const maxBrowseEntries = 2000

// handleBrowse lists a directory on the computer's disk. Admin-only: it can
// read any path, but only for requests from loopback or with the admin token.
func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/"
		}
		p = home
	}
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		writeErr(w, http.StatusBadRequest, "路径必须是绝对路径")
		return
	}
	des, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, browseErrStatus(err), browseErrMsg(err))
		return
	}
	entries := make([]fsEntryJSON, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, fsEntryJSON{
			Name:  name,
			Path:  filepath.Join(p, name),
			Dir:   de.IsDir(),
			Size:  info.Size(),
			MTime: info.ModTime().Unix(),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return naturalLess(entries[i].Name, entries[j].Name)
	})
	truncated := false
	if len(entries) > maxBrowseEntries {
		entries = entries[:maxBrowseEntries]
		truncated = true
	}
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      p,
		"parent":    parent,
		"entries":   entries,
		"truncated": truncated,
		"shortcuts": browseShortcuts(),
	})
}

func browseShortcuts() []shortcutJSON {
	var out []shortcutJSON
	add := func(name, p string) {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, shortcutJSON{Name: name, Path: p})
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add("主目录", home)
		add("桌面", filepath.Join(home, "Desktop"))
		add("下载", filepath.Join(home, "Downloads"))
		add("影片", filepath.Join(home, "Movies"))
		add("图片", filepath.Join(home, "Pictures"))
		add("文稿", filepath.Join(home, "Documents"))
	}
	if runtime.GOOS == "darwin" {
		add("外接磁盘", "/Volumes")
	}
	add("根目录", "/")
	return out
}

func browseErrStatus(err error) int {
	switch {
	case os.IsNotExist(err):
		return http.StatusNotFound
	case os.IsPermission(err):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func browseErrMsg(err error) string {
	switch {
	case os.IsNotExist(err):
		return "目录不存在"
	case os.IsPermission(err):
		return "没有权限访问该目录"
	default:
		return "无法读取目录"
	}
}
