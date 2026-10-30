package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContentRangeStart(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"bytes 100-199/1048576", 100, true},
		{"bytes 0-9/10", 0, true},
		{"bytes */10", 0, false}, // 416 shape
		{"", 0, false},
		{"items 1-2/3", 0, false},
		{"bytes abc-199/1000", 0, false},
	}
	for _, c := range cases {
		got, ok := contentRangeStart(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("contentRangeStart(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestTrackableRange(t *testing.T) {
	mk := func(h string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if h != "" {
			r.Header.Set("Range", h)
		}
		return r
	}
	cases := map[string]bool{
		"":                true, // full download
		"bytes=100-":      true, // download-manager resume
		"bytes=0-99":      false,
		"bytes=-500":      false, // suffix probe
		"bytes=0-9,20-29": false, // multi-range
		"items=1-":        false,
		"bytes=abc-":      false,
	}
	for h, want := range cases {
		if got := trackableRange(mk(h)); got != want {
			t.Errorf("trackableRange(%q) = %v, want %v", h, got, want)
		}
	}
}
