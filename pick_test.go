package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// The real runner opens a Finder dialog; tests swap in a fake.
func withFakePick(t *testing.T, supported bool, out string, err error) {
	t.Helper()
	oldS, oldR := pickSupported, runPick
	pickSupported = supported
	runPick = func(ctx context.Context, script string) ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { pickSupported, runPick = oldS, oldR })
}

func TestPickReturnsChosenPaths(t *testing.T) {
	srv, _, _ := newTestServer(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	writeTestFile(t, f, []byte("x"))
	// choose folder appends a slash; a stale path must be dropped
	withFakePick(t, true, f+"\n"+dir+"/\n"+filepath.Join(dir, "missing")+"\n", nil)

	rec := doReq(srv, "POST", "/api/pick", []byte(`{"mode":"files"}`),
		map[string]string{"Content-Type": "application/json"}, "127.0.0.1:1")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var v struct {
		Items []pickedItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Items) != 2 {
		t.Fatalf("want 2 items, got %+v", v.Items)
	}
	if v.Items[0].Path != f || v.Items[0].Dir || v.Items[0].Name != "a.txt" {
		t.Fatalf("file item %+v", v.Items[0])
	}
	if v.Items[1].Path != dir || !v.Items[1].Dir {
		t.Fatalf("folder item %+v", v.Items[1])
	}
}

func TestPickCancelUnsupportedAndJSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	hdr := map[string]string{"Content-Type": "application/json"}

	withFakePick(t, true, "", errPickCancelled)
	rec := doReq(srv, "POST", "/api/pick", []byte(`{"mode":"files"}`), hdr, "127.0.0.1:1")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cancelled":true`) {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}

	withFakePick(t, false, "", nil)
	rec = doReq(srv, "POST", "/api/pick", []byte(`{"mode":"files"}`), hdr, "127.0.0.1:1")
	if rec.Code != 501 {
		t.Fatalf("unsupported platform should be 501, got %d", rec.Code)
	}

	// admin gate still applies: a form post is refused before any dialog
	withFakePick(t, true, "/tmp\n", nil)
	rec = doReq(srv, "POST", "/api/pick", []byte(`{"mode":"files"}`),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, "127.0.0.1:1")
	if rec.Code != 415 {
		t.Fatalf("form post should be 415, got %d", rec.Code)
	}
	rec = doReq(srv, "POST", "/api/pick", []byte(`{"mode":"files"}`), hdr, "192.168.1.9:4")
	if rec.Code != 401 {
		t.Fatalf("LAN peer without token should be 401, got %d", rec.Code)
	}
}

func TestPickScriptAndCancelDetection(t *testing.T) {
	if !strings.Contains(pickScript(false), "choose file with prompt") {
		t.Fatal("file script")
	}
	if !strings.Contains(pickScript(true), "choose folder with prompt") {
		t.Fatal("folder script")
	}
	if !strings.HasPrefix(pickScript(true), "activate\n") {
		t.Fatal("dialog must be brought to the front")
	}
	if !isPickCancel("execution error: User canceled. (-128)") || isPickCancel("some other error (-1713)") {
		t.Fatal("cancel detection")
	}
}
