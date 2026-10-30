package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func uploadReq(srv *server, batch, rel string, body []byte, marker bool) (int, map[string]any) {
	h := map[string]string{"Content-Type": "application/octet-stream"}
	if marker {
		h[uploadHeader] = "1"
	}
	rec := doReq(srv, "POST", "/api/upload?batch="+url.QueryEscape(batch)+"&rel="+url.QueryEscape(rel), body, h, "127.0.0.1:1")
	var v map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v
}

func TestUploadStagesSharesAndCleansUp(t *testing.T) {
	srv, store, _ := newTestServer(t)
	t.Cleanup(srv.uploads.Close)
	json1 := map[string]string{"Content-Type": "application/json"}
	const batch = "batch_abc123"

	code, res := uploadReq(srv, batch, "Photos/a.jpg", []byte("JPEGDATA"), true)
	if code != 200 {
		t.Fatalf("upload: %d %v", code, res)
	}
	path, batchDir := res["path"].(string), res["batchDir"].(string)
	if got, _ := os.ReadFile(path); string(got) != "JPEGDATA" {
		t.Fatalf("staged content %q", got)
	}
	if filepath.Dir(filepath.Dir(path)) != batchDir || filepath.Base(filepath.Dir(path)) != "Photos" {
		t.Fatalf("layout: path=%s batchDir=%s", path, batchDir)
	}
	if code, _ := uploadReq(srv, batch, "Photos/sub/b.txt", []byte("B"), true); code != 200 {
		t.Fatalf("nested upload: %d", code)
	}

	// share the dropped folder through the normal API: layout is kept and
	// the session remembers the staged batch
	body, _ := json.Marshal(map[string]any{"paths": []string{filepath.Join(batchDir, "Photos")}})
	rec := doReq(srv, "POST", "/api/session", body, json1, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	sess := store.Active()
	files := sess.filesSnapshot()
	if len(files) != 2 || files[0].RelPath != "Photos/a.jpg" || files[1].RelPath != "Photos/sub/b.txt" {
		t.Fatalf("files %+v", []string{files[0].RelPath, files[1].RelPath})
	}
	if !sess.usesTempDir(batchDir) {
		t.Fatal("session must track the staged batch")
	}

	// in use: discard refused; download serves the staged copy
	rec = doReq(srv, "POST", "/api/upload/discard", []byte(`{"batch":"`+batch+`"}`), json1, "127.0.0.1:1")
	if rec.Code != 409 {
		t.Fatalf("discard of a live batch should be 409, got %d", rec.Code)
	}
	rec = doReq(srv, "GET", "/d/"+sess.Code+"/1", nil, nil, "")
	if rec.Code != 200 || rec.Body.String() != "JPEGDATA" {
		t.Fatalf("download %d %q", rec.Code, rec.Body.String())
	}

	// stopping the session deletes the copies
	doReq(srv, "POST", "/api/session/stop", []byte(`{}`), json1, "127.0.0.1:1")
	if _, err := os.Stat(batchDir); !os.IsNotExist(err) {
		t.Fatalf("batch dir should be removed on stop, stat err=%v", err)
	}

	// adding staged files to a running session is tracked too
	dir := t.TempDir()
	plain := filepath.Join(dir, "p.txt")
	writeTestFile(t, plain, []byte("p"))
	rec = doReq(srv, "POST", "/api/session", mustJSON(map[string]any{"paths": []string{plain}, "replace": true}), json1, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("create2: %d", rec.Code)
	}
	_, res = uploadReq(srv, "batch_second", "x.bin", []byte("x"), true)
	rec = doReq(srv, "POST", "/api/session/add", mustJSON(map[string]any{"paths": []string{res["path"].(string)}}), json1, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	if !store.Active().usesTempDir(res["batchDir"].(string)) {
		t.Fatal("added staged file must be tracked")
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestUploadRejectsBadPathsAndStaysContained(t *testing.T) {
	srv, _, _ := newTestServer(t)
	t.Cleanup(srv.uploads.Close)
	cases := []struct {
		batch, rel string
		marker     bool
		want       int
	}{
		{"batch_abc123", "../x", true, 400},
		{"batch_abc123", "/abs", true, 400},
		{"batch_abc123", "a/../../b", true, 400},
		{"batch_abc123", "a/./b", true, 400},
		{"batch_abc123", "a//b", true, 400},
		{"batch_abc123", "", true, 400},
		{"batch_abc123", "bad\x00name", true, 400},
		{"batch_abc123", `..\x`, true, 400},
		{"bad id!", "a.txt", true, 400},
		{"short", "a.txt", true, 400},
		{"batch_abc123", "ok.txt", false, 400}, // missing marker header
		{"batch_abc123", "ok.txt", true, 200},
	}
	for _, c := range cases {
		code, _ := uploadReq(srv, c.batch, c.rel, []byte("x"), c.marker)
		if code != c.want {
			t.Errorf("batch=%q rel=%q marker=%v: want %d got %d", c.batch, c.rel, c.marker, c.want, code)
		}
	}
	// only the one valid file exists under the root
	root := srv.uploads.Root()
	var found []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(root, p)
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(found) != 1 || found[0] != "batch_abc123/ok.txt" {
		t.Fatalf("staged files: %v", found)
	}

	// cross-site browsers are refused before anything is written
	rec := doReq(srv, "POST", "/api/upload?batch=batch_abc123&rel=evil.txt", []byte("x"),
		map[string]string{uploadHeader: "1", "Sec-Fetch-Site": "cross-site"}, "127.0.0.1:1")
	if rec.Code != 403 {
		t.Fatalf("cross-site upload should be 403, got %d", rec.Code)
	}
}

func TestUploadDiscardRemovesUnusedBatch(t *testing.T) {
	srv, _, _ := newTestServer(t)
	t.Cleanup(srv.uploads.Close)
	_, res := uploadReq(srv, "batch_unused", "a.txt", []byte("a"), true)
	dir := res["batchDir"].(string)
	rec := doReq(srv, "POST", "/api/upload/discard", []byte(`{"batch":"batch_unused"}`),
		map[string]string{"Content-Type": "application/json"}, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("discard: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("batch dir should be gone")
	}
	rec = doReq(srv, "POST", "/api/upload/discard", []byte(`{"batch":"nope!"}`),
		map[string]string{"Content-Type": "application/json"}, "127.0.0.1:1")
	if rec.Code != 400 {
		t.Fatalf("bad batch id should be 400, got %d", rec.Code)
	}
}

func TestCleanRel(t *testing.T) {
	ok := map[string]string{
		"a.txt":         "a.txt",
		"Photos/a.jpg":  filepath.Join("Photos", "a.jpg"),
		"中文/视频 (2).mp4": filepath.Join("中文", "视频 (2).mp4"),
	}
	for in, want := range ok {
		got, err := cleanRel(in)
		if err != nil || got != want {
			t.Errorf("cleanRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "/a", "../a", "a/..", "a/../b", ".", "a/./b", "a//b", "a\tb", "..\\a"} {
		if _, err := cleanRel(bad); err == nil {
			t.Errorf("cleanRel(%q) should fail", bad)
		}
	}
}
