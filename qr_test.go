package main

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// TestQRGoldenSVG locks the QR output for the canonical phone URL.
//
// The matrix produced by piglig/go-qr was verified against Apple's Vision
// decoder (see tools/qrcheck.swift): the PNG we render decodes to the exact
// URL. If this golden hash ever changes, re-run tools/qrcheck.swift before
// updating it.
func TestQRGoldenSVG(t *testing.T) {
	text := "http://192.168.1.23:8787/r/abcd234567"
	svg, err := qrSVG(text)
	if err != nil {
		t.Fatalf("qrSVG: %v", err)
	}
	if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "<path") {
		t.Fatalf("qrSVG output looks wrong: %.120s", svg)
	}
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(svg)))
	want := "8c694cb3268894f920d4ab75b96f5ce62bb9b114db86c0e105838e240f60aebe"
	if got != want {
		t.Fatalf("golden hash mismatch:\n got %s\nwant %s", got, want)
	}
	if _, err := qrPNG(text, 768); err != nil {
		t.Fatalf("qrPNG: %v", err)
	}
}
