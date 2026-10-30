package main

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*server, *Store, string) {
	t.Helper()
	store := NewStore("testtoken")
	store.BaseURL = "http://127.0.0.1:9999"
	srv := newServer(store, newBroker())
	return srv, store, "testtoken"
}

func doReq(srv *server, method, target string, body []byte, headers map[string]string, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Host = "127.0.0.1:8787" // what a browser on the computer actually sends
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	} else {
		req.RemoteAddr = "192.0.2.10:12345"
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func setupSession(t *testing.T, store *Store, content []byte) *Session {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "测试文件.bin")
	writeTestFile(t, p, content)
	sess, err := store.Create([]string{p}, "", true) // replace: each test session supersedes the previous
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestDownloadFull(t *testing.T) {
	srv, store, _ := newTestServer(t)
	content := []byte("0123456789")
	sess := setupSession(t, store, content)

	rec := doReq(srv, "GET", "/d/"+sess.Code+"/1", nil, nil, "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body mismatch: %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "filename*=UTF-8''") {
		t.Fatalf("disposition: %s", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("missing ETag")
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges: %q", rec.Header().Get("Accept-Ranges"))
	}
	f := sess.fileByID(1)
	if !f.done.Load() {
		t.Fatal("full download should mark file done")
	}
	if f.sent.Load() != int64(len(content)) {
		t.Fatalf("sent=%d want %d", f.sent.Load(), len(content))
	}
}

func TestDownloadRangeAndResume(t *testing.T) {
	srv, store, _ := newTestServer(t)
	content := []byte("0123456789")

	// closed range is a media-style probe: served, but not counted as progress
	sess := setupSession(t, store, content)
	rec := doReq(srv, "GET", "/d/"+sess.Code+"/1", nil, map[string]string{"Range": "bytes=2-5"}, "")
	if rec.Code != 206 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Body.String() != "2345" {
		t.Fatalf("body %q", rec.Body.String())
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 2-5/10" {
		t.Fatalf("Content-Range %q", cr)
	}
	f := sess.fileByID(1)
	if f.sent.Load() != 0 || f.done.Load() {
		t.Fatalf("closed range must not count: sent=%d done=%v", f.sent.Load(), f.done.Load())
	}

	// open-ended range is a download resume: counts toward progress
	sess2 := setupSession(t, store, content)
	rec = doReq(srv, "GET", "/d/"+sess2.Code+"/1", nil, map[string]string{"Range": "bytes=4-"}, "")
	if rec.Code != 206 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Body.String() != "456789" {
		t.Fatalf("body %q", rec.Body.String())
	}
	f2 := sess2.fileByID(1)
	if f2.sent.Load() != 10 || !f2.done.Load() {
		t.Fatalf("resume to EOF: sent=%d done=%v", f2.sent.Load(), f2.done.Load())
	}
}

func TestDownloadIfRangeAnd416(t *testing.T) {
	srv, store, _ := newTestServer(t)
	content := []byte("0123456789")
	sess := setupSession(t, store, content)

	full := doReq(srv, "GET", "/d/"+sess.Code+"/1", nil, nil, "")
	etag := full.Header().Get("ETag")

	// If-Range with matching ETag honors the range
	rec := doReq(srv, "GET", "/d/"+sess.Code+"/1", nil,
		map[string]string{"Range": "bytes=8-", "If-Range": etag}, "")
	if rec.Code != 206 || rec.Body.String() != "89" {
		t.Fatalf("If-Range match: %d %q", rec.Code, rec.Body.String())
	}

	// If-Range with stale validator serves the full file
	rec = doReq(srv, "GET", "/d/"+sess.Code+"/1", nil,
		map[string]string{"Range": "bytes=8-", "If-Range": `"stale"`}, "")
	if rec.Code != 200 || rec.Body.String() != string(content) {
		t.Fatalf("If-Range mismatch: %d %q", rec.Code, rec.Body.String())
	}

	// invalid range: 416, no progress counted
	sess2 := setupSession(t, store, content)
	rec = doReq(srv, "GET", "/d/"+sess2.Code+"/1", nil, map[string]string{"Range": "bytes=100-"}, "")
	if rec.Code != 416 {
		t.Fatalf("status %d", rec.Code)
	}
	f := sess2.fileByID(1)
	if f.sent.Load() != 0 || f.done.Load() {
		t.Fatalf("416 must not count: sent=%d done=%v", f.sent.Load(), f.done.Load())
	}

	// HEAD returns headers only and does not mark done
	sess3 := setupSession(t, store, content)
	rec = doReq(srv, "HEAD", "/d/"+sess3.Code+"/1", nil, nil, "")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d body=%d", rec.Code, rec.Body.Len())
	}
	if sess3.fileByID(1).done.Load() {
		t.Fatal("HEAD must not mark done")
	}
}

func TestDownloadUnknownSession(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doReq(srv, "GET", "/d/zzzzzzzzzz/1", nil, nil, "")
	if rec.Code != 404 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestReceiverAuthAndRateLimit(t *testing.T) {
	srv, store, _ := newTestServer(t)
	sess := setupSession(t, store, []byte("data"))

	rec := doReq(srv, "GET", "/api/r/"+sess.Code, nil, nil, "")
	if rec.Code != 200 {
		t.Fatalf("valid code status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"active":true`) {
		t.Fatalf("meta body: %s", rec.Body.String())
	}

	// hammering unknown codes eventually rate-limits
	got429 := false
	for i := 0; i < 60; i++ {
		r := doReq(srv, "GET", "/api/r/zzzzzzzzzz", nil, nil, "")
		if r.Code == 429 {
			got429 = true
			break
		}
		if r.Code != 404 {
			t.Fatalf("unexpected status %d", r.Code)
		}
	}
	if !got429 {
		t.Fatal("expected 429 after hammering unknown codes")
	}

	// stopped session is gone for receivers
	sess.stop()
	rec = doReq(srv, "GET", "/api/r/"+sess.Code, nil, nil, "198.51.100.7:4444")
	if rec.Code != 404 {
		t.Fatalf("stopped session status %d", rec.Code)
	}
}
