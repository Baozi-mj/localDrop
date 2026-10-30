package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSEStream(t *testing.T) {
	store := NewStore("tok")
	store.BaseURL = "http://127.0.0.1:9999"
	br := newBroker()
	srv := newServer(store, br)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := &http.Client{Timeout: 10 * time.Second}

	// create a session over HTTP (loopback => admin allowed)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), []byte("aaa"))
	body := fmt.Sprintf(`{"paths":[%q]}`, filepath.Join(dir, "a.txt"))
	resp, err := client.Post(ts.URL+"/api/session", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// subscribe to admin events
	sseResp, err := client.Get(ts.URL + "/api/events?t=tok")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	if ct := sseResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q", ct)
	}
	reader := bufio.NewReader(sseResp.Body)

	readEvent := func() map[string]any {
		t.Helper()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read sse: %v", err)
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				t.Fatalf("unexpected sse line: %q", line)
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("bad sse json: %v", err)
			}
			return ev
		}
	}

	ev := readEvent()
	if ev["type"] != "state" {
		t.Fatalf("first event type %v", ev["type"])
	}

	// progress ticker publishes once there is a subscriber
	srv.publishProgress()
	ev = readEvent()
	if ev["type"] != "progress" {
		t.Fatalf("second event type %v", ev["type"])
	}
	if _, ok := ev["files"]; !ok {
		t.Fatalf("progress event missing files: %v", ev)
	}
}

func TestBrokerDropsSlowSubscriber(t *testing.T) {
	br := newBroker()
	id, ch := br.subscribe("k")
	if br.count("k") != 1 {
		t.Fatal("subscribe failed")
	}
	for i := 0; i < 200; i++ {
		br.publish("k", []byte(`{"type":"x"}`))
	}
	if br.count("k") != 0 {
		t.Fatal("slow subscriber should have been dropped")
	}
	// channel is closed; drain to completion
	n := 0
	for range ch {
		n++
	}
	if n == 0 {
		t.Fatal("expected some buffered events before the drop")
	}
	br.unsubscribe(id) // already removed: must be a safe no-op
}

func TestBrokerRouting(t *testing.T) {
	br := newBroker()
	_, adminCh := br.subscribe("")
	_, codeCh := br.subscribe("abc")
	_, otherCh := br.subscribe("xyz")

	br.publish("abc", []byte(`{"type":"for-abc"}`))
	if got := drainOne(adminCh); got != "" {
		t.Fatalf("admin must not receive session-scoped publish, got %q", got)
	}
	if got := drainOne(codeCh); got != `{"type":"for-abc"}` {
		t.Fatalf("code subscriber got %q", got)
	}
	if got := drainOne(otherCh); got != "" {
		t.Fatalf("other session got %q", got)
	}

	br.publishAdmin([]byte(`{"type":"for-admin"}`))
	if got := drainOne(adminCh); got != `{"type":"for-admin"}` {
		t.Fatalf("admin got %q", got)
	}

	br.broadcast([]byte(`{"type":"all"}`))
	if got := drainOne(codeCh); got != `{"type":"all"}` {
		t.Fatalf("broadcast to code subscriber: %q", got)
	}
	if got := drainOne(adminCh); got != `{"type":"all"}` {
		t.Fatalf("broadcast to admin: %q", got)
	}
}

func drainOne(ch <-chan []byte) string {
	select {
	case b := <-ch:
		return string(b)
	case <-time.After(50 * time.Millisecond):
		return ""
	}
}
