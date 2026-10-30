package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Native file chooser. On macOS the server itself asks for the system Open
// dialog (via osascript), so the user gets the familiar Finder window and we
// still get real absolute paths: files keep streaming straight from disk with
// no upload and no copy. Other platforms report "unsupported" and the page
// falls back to the built-in directory browser.

var (
	errPickUnsupported = errors.New("当前系统不支持原生选择框")
	errPickBusy        = errors.New("已有一个选择窗口打开，请先完成或取消")
	errPickCancelled   = errors.New("已取消")
)

// Overridable in tests: the real runner opens a dialog on the desktop.
var (
	pickSupported = runtime.GOOS == "darwin"
	runPick       = runOsascript
)

var pickMu sync.Mutex

// An abandoned dialog must not pin a request forever.
const pickTimeout = 10 * time.Minute

type pickedItem struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
}

// pickScript builds the AppleScript for a multi-select file or folder dialog.
// The leading `activate` brings osascript, and so its dialog, to the front; it
// sends no Apple events to other apps, so macOS asks for no automation consent.
func pickScript(folder bool) string {
	kind, prompt := "file", "选择要投送到手机的文件"
	if folder {
		kind, prompt = "folder", "选择要投送到手机的文件夹"
	}
	return "activate\n" +
		"set picked to choose " + kind + " with prompt \"" + prompt + "\" with multiple selections allowed without invisibles\n" +
		"set out to \"\"\n" +
		"repeat with p in picked\n" +
		"\tset out to out & (POSIX path of p) & linefeed\n" +
		"end repeat\n" +
		"return out\n"
}

// isPickCancel recognises AppleScript's "User canceled" (-128).
func isPickCancel(stderr string) bool {
	return strings.Contains(stderr, "-128")
}

func runOsascript(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "osascript")
	cmd.Stdin = strings.NewReader(script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil || isPickCancel(errb.String()) {
			return nil, errPickCancelled
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return out.Bytes(), nil
}

// nativePick shows the dialog and returns the chosen items (existing paths only).
func nativePick(ctx context.Context, folder bool) ([]pickedItem, error) {
	if !pickSupported {
		return nil, errPickUnsupported
	}
	if !pickMu.TryLock() {
		return nil, errPickBusy
	}
	defer pickMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, pickTimeout)
	defer cancel()
	out, err := runPick(ctx, pickScript(folder))
	if err != nil {
		return nil, err
	}
	items := make([]pickedItem, 0, 4)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if len(line) > 1 {
			line = strings.TrimSuffix(line, "/") // choose folder appends one
		}
		p := filepath.Clean(line)
		if !filepath.IsAbs(p) {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		items = append(items, pickedItem{Path: p, Name: st.Name(), Dir: st.IsDir()})
	}
	return items, nil
}

// handlePick: POST {"mode":"files"|"folder"} -> {"items":[...]} or
// {"items":[],"cancelled":true}. 501 when the platform has no native dialog.
func (s *server) handlePick(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	items, err := nativePick(r.Context(), req.Mode == "folder")
	switch {
	case errors.Is(err, errPickUnsupported):
		writeErr(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, errPickBusy):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, errPickCancelled):
		writeJSON(w, http.StatusOK, map[string]any{"items": []pickedItem{}, "cancelled": true})
	case err != nil:
		log.Printf("原生选择框失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "无法打开系统选择框："+err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}
