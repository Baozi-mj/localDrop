package main

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminAuth(t *testing.T) {
	srv, _, tok := newTestServer(t)

	// non-loopback without token
	rec := doReq(srv, "GET", "/api/session", nil, nil, "")
	if rec.Code != 401 {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	// token via header
	rec = doReq(srv, "GET", "/api/session", nil, map[string]string{"X-Admin-Token": tok}, "")
	if rec.Code != 200 {
		t.Fatalf("header token: %d", rec.Code)
	}
	// token via query (EventSource cannot set headers)
	rec = doReq(srv, "GET", "/api/session?t="+tok, nil, nil, "")
	if rec.Code != 200 {
		t.Fatalf("query token: %d", rec.Code)
	}
	// loopback passes without token
	rec = doReq(srv, "GET", "/api/session", nil, nil, "127.0.0.1:5000")
	if rec.Code != 200 {
		t.Fatalf("loopback: %d", rec.Code)
	}
	// wrong token
	rec = doReq(srv, "GET", "/api/session", nil, map[string]string{"X-Admin-Token": "nope"}, "")
	if rec.Code != 401 {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	// no active session yet
	if body := rec.Body.String(); body == "" {
		t.Fatal("expected JSON body")
	}
}

func TestSessionHTTPFlow(t *testing.T) {
	srv, store, _ := newTestServer(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), []byte("aaa"))

	body := []byte(`{"paths":[` + jsonString(filepath.Join(dir, "a.txt")) + `]}`)
	jsonHdr := map[string]string{"Content-Type": "application/json"}
	rec := doReq(srv, "POST", "/api/session", body, jsonHdr, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var out sessionJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Active || out.Code == "" || len(out.Files) != 1 {
		t.Fatalf("bad session json: %+v", out)
	}
	if out.URL != "http://127.0.0.1:9999/r/"+out.Code {
		t.Fatalf("url %q", out.URL)
	}

	// second create without replace conflicts
	rec = doReq(srv, "POST", "/api/session", body, jsonHdr, "127.0.0.1:1")
	if rec.Code != 409 {
		t.Fatalf("conflict: %d", rec.Code)
	}

	// stop
	rec = doReq(srv, "POST", "/api/session/stop", nil, nil, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("stop: %d", rec.Code)
	}
	if store.Active() != nil {
		t.Fatal("session should be stopped")
	}
	rec = doReq(srv, "GET", "/api/session", nil, nil, "127.0.0.1:1")
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatal("invalid json")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestBrowseAPI(t *testing.T) {
	srv, _, _ := newTestServer(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "b.txt"), []byte("x"))
	writeTestFile(t, filepath.Join(dir, "A子目录", "inner.txt"), []byte("y"))
	writeTestFile(t, filepath.Join(dir, ".hidden"), []byte("z"))

	rec := doReq(srv, "GET", "/api/fs?path="+url.QueryEscape(dir), nil, nil, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("browse: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Path    string        `json:"path"`
		Entries []fsEntryJSON `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("entries: %+v", out.Entries)
	}
	if !out.Entries[0].Dir || out.Entries[0].Name != "A子目录" {
		t.Fatalf("dirs should sort first: %+v", out.Entries)
	}
	if out.Entries[1].Name != "b.txt" || out.Entries[1].Dir {
		t.Fatalf("file entry wrong: %+v", out.Entries[1])
	}

	// relative path rejected
	rec = doReq(srv, "GET", "/api/fs?path=relative/path", nil, nil, "127.0.0.1:1")
	if rec.Code != 400 {
		t.Fatalf("relative path: %d", rec.Code)
	}
	// nonexistent dir
	rec = doReq(srv, "GET", "/api/fs?path="+url.QueryEscape(filepath.Join(dir, "nope")), nil, nil, "127.0.0.1:1")
	if rec.Code != 404 {
		t.Fatalf("missing dir: %d", rec.Code)
	}
	// unauthenticated remote request
	rec = doReq(srv, "GET", "/api/fs?path="+url.QueryEscape(dir), nil, nil, "")
	if rec.Code != 401 {
		t.Fatalf("remote browse: %d", rec.Code)
	}
}

func TestQREndpoints(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doReq(srv, "GET", "/api/qr.svg?text=http%3A%2F%2F192.168.1.5%3A8787%2Fr%2Fabc", nil, nil, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("qr.svg: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml; charset=utf-8" {
		t.Fatalf("content-type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Fatal("not an svg")
	}
	rec = doReq(srv, "GET", "/api/qr.png?text=hello", nil, nil, "127.0.0.1:1")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("qr.png: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = doReq(srv, "GET", "/api/qr.svg", nil, nil, "127.0.0.1:1")
	if rec.Code != 400 {
		t.Fatalf("missing text: %d", rec.Code)
	}
}
