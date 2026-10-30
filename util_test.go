package main

import (
	"strings"
	"testing"
)

func TestNaturalLess(t *testing.T) {
	ordered := []string{"a1", "a2", "a10", "b1", "B2"}
	for i := 0; i < len(ordered)-1; i++ {
		if !naturalLess(ordered[i], ordered[i+1]) {
			t.Errorf("expected %q < %q", ordered[i], ordered[i+1])
		}
	}
	if naturalLess("a2", "a2") {
		t.Error("equal strings must not compare less")
	}
	if naturalLess("b", "a") {
		t.Error("plain lexical order broken")
	}
}

func TestContentDisposition(t *testing.T) {
	got := contentDisposition("我的 视频 (1).mp4")
	if !strings.Contains(got, "filename*=UTF-8''") {
		t.Fatalf("missing filename*: %s", got)
	}
	if !strings.Contains(got, "%E6%88%91") { // 我
		t.Fatalf("UTF-8 not percent-encoded: %s", got)
	}
	inj := contentDisposition("evil\r\nX-Bad: 1.mp4")
	if strings.ContainsAny(inj, "\r\n") {
		t.Fatalf("CRLF not stripped: %q", inj)
	}
	ascii := contentDisposition("plain.mp4")
	if !strings.Contains(ascii, `filename="plain.mp4"`) {
		t.Fatalf("ascii fallback wrong: %s", ascii)
	}
	p := contentDisposition("/tmp/dir/file.txt")
	if !strings.Contains(p, `filename="file.txt"`) {
		t.Fatalf("path not stripped: %s", p)
	}
	empty := contentDisposition("")
	if !strings.Contains(empty, `filename="download"`) {
		t.Fatalf("empty name fallback wrong: %s", empty)
	}
}

func TestRFC5987Encode(t *testing.T) {
	got := rfc5987Encode("a b'c*")
	if got != "a%20b%27c%2A" {
		t.Fatalf("got %q", got)
	}
}
