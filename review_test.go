package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for defects found in the adversarial review.

func TestAdminRejectsForeignHostAndCrossSite(t *testing.T) {
	srv, _, tok := newTestServer(t)
	mk := func(host string, hdr map[string]string, remote string) int {
		req := httptest.NewRequest("GET", "/api/session", nil)
		req.RemoteAddr = remote
		req.Host = host
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := mk("127.0.0.1:8787", nil, "127.0.0.1:1"); c != 200 {
		t.Fatalf("loopback host: %d", c)
	}
	if c := mk("localhost:8787", nil, "127.0.0.1:1"); c != 200 {
		t.Fatalf("localhost host: %d", c)
	}
	// DNS rebinding: the TCP peer is loopback but the browser addressed attacker.example
	if c := mk("attacker.example:8787", nil, "127.0.0.1:1"); c != 401 {
		t.Fatalf("rebound host should be refused: %d", c)
	}
	// CSRF: a cross-site page cannot drive the admin API even from loopback
	if c := mk("127.0.0.1:8787", map[string]string{"Sec-Fetch-Site": "cross-site"}, "127.0.0.1:1"); c != 403 {
		t.Fatalf("cross-site: %d", c)
	}
	if c := mk("127.0.0.1:8787", map[string]string{"Origin": "http://evil.example"}, "127.0.0.1:1"); c != 403 {
		t.Fatalf("foreign origin: %d", c)
	}
	if c := mk("127.0.0.1:8787", map[string]string{"Origin": "http://127.0.0.1:8787", "Sec-Fetch-Site": "same-origin"}, "127.0.0.1:1"); c != 200 {
		t.Fatalf("same-origin browser request: %d", c)
	}
	// token from the LAN still works, but not from a cross-site page
	if c := mk("192.168.1.5:8787", map[string]string{"X-Admin-Token": tok, "Sec-Fetch-Site": "same-origin"}, "192.168.1.9:4"); c != 200 {
		t.Fatalf("token same-origin: %d", c)
	}
	if c := mk("192.168.1.5:8787", map[string]string{"X-Admin-Token": tok, "Sec-Fetch-Site": "cross-site"}, "192.168.1.9:4"); c != 403 {
		t.Fatalf("token cross-site: %d", c)
	}
}

func TestAdminWritesRequireJSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	// a plain HTML form post has a form content type
	rec := doReq(srv, "POST", "/api/session", []byte(`{"paths":[]}`),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, "127.0.0.1:1")
	if rec.Code != 415 {
		t.Fatalf("want 415, got %d", rec.Code)
	}
}

func TestETagTracksFileChanges(t *testing.T) {
	srv, store, _ := newTestServer(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "movie.bin")
	writeTestFile(t, p, []byte("0123456789"))
	sess, err := store.Create([]string{p}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	first := doReq(srv, "GET", "/d/"+sess.Code+"/1", nil, nil, "")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no etag")
	}
	// the file is rewritten while the session is still live
	writeTestFile(t, p, []byte("ABCDEFGHIJKLMNOPQRST"))
	rec := doReq(srv, "GET", "/d/"+sess.Code+"/1", nil,
		map[string]string{"Range": "bytes=5-", "If-Range": etag}, "")
	if rec.Code != 200 {
		t.Fatalf("stale If-Range must downgrade to a full 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ABCDEFGHIJKLMNOPQRST" {
		t.Fatalf("body %q", rec.Body.String())
	}
	if rec.Header().Get("ETag") == etag {
		t.Fatal("etag must change when the file changes")
	}
}

func TestZeroByteFileMarkedDone(t *testing.T) {
	srv, store, _ := newTestServer(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.txt")
	writeTestFile(t, p, nil)
	sess, err := store.Create([]string{p}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	rec := doReq(srv, "GET", "/d/"+sess.Code+"/1", nil, nil, "")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("status %d body=%d", rec.Code, rec.Body.Len())
	}
	if !sess.fileByID(1).done.Load() {
		t.Fatal("zero-byte file should be marked done after a full GET")
	}
	// HEAD must not mark anything
	sess2, _ := store.Create([]string{p}, "", true)
	doReq(srv, "HEAD", "/d/"+sess2.Code+"/1", nil, nil, "")
	if sess2.fileByID(1).done.Load() {
		t.Fatal("HEAD must not mark done")
	}
}

func TestReplaceKeepsOldSessionWhenNewIsInvalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	writeTestFile(t, p, []byte("a"))
	store := NewStore("tok")
	old, err := store.Create([]string{p}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, stopped, err := store.CreateReplacing([]string{filepath.Join(dir, "missing.txt")}, "", true)
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if stopped != nil {
		t.Fatal("nothing may be stopped when the replacement is invalid")
	}
	if store.Active() != old || old.isStopped() {
		t.Fatal("old session must stay active")
	}
	nw, stopped2, err := store.CreateReplacing([]string{p}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if stopped2 != old || nw == old || !old.isStopped() {
		t.Fatal("successful replace must stop and return the old session")
	}
}

func TestFilesKeyAlwaysPresent(t *testing.T) {
	srv, store, _ := newTestServer(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	writeTestFile(t, p, []byte("a"))
	sess, err := store.Create([]string{p}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	sess.removeFiles([]int{1})
	rec := doReq(srv, "GET", "/api/r/"+sess.Code, nil, nil, "")
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	files, ok := v["files"].([]any)
	if !ok || len(files) != 0 {
		t.Fatalf("files must be an empty array: %s", rec.Body.String())
	}
	rec = doReq(srv, "GET", "/api/session", nil, nil, "127.0.0.1:1")
	if !strings.Contains(rec.Body.String(), `"files":[]`) {
		t.Fatalf("admin files key missing: %s", rec.Body.String())
	}
}

func TestProgressEventsOnlyWhenChanged(t *testing.T) {
	store := NewStore("tok")
	br := newBroker()
	srv := newServer(store, br)
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	writeTestFile(t, p, []byte("abcdef"))
	sess, err := store.Create([]string{p}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	_, ch := br.subscribe("")

	srv.publishProgress()
	if got := drainOne(ch); got == "" {
		t.Fatal("first tick should publish the initial progress")
	}
	srv.publishProgress()
	if got := drainOne(ch); got != "" {
		t.Fatalf("idle tick must stay silent, got %s", got)
	}
	sess.fileByID(1).sent.Store(4)
	srv.publishProgress()
	got := drainOne(ch)
	if !strings.Contains(got, `"sent":4`) || !strings.Contains(got, `"total":6`) {
		t.Fatalf("change not published with totals: %s", got)
	}
}
