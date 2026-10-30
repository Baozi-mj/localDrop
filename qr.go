package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	qr "github.com/piglig/go-qr"
)

// qrQuietZone is the white border required by the QR spec for scanning.
const qrQuietZone = 4

// qrMatrix is the subset of the encoder's API we use.
type qrMatrix interface {
	Size() int
	Module(x, y int) bool
}

// encodeQR encodes text at EC level M. piglig/go-qr picks a compact symbol
// version (v3 for a typical LocalDrop URL) which scans better at distance
// than the over-provisioned versions some other encoders choose.
func encodeQR(text string) (qrMatrix, error) {
	return qr.EncodeText(text, qr.Medium)
}

// qrSVG renders text as a crisp SVG QR code with a small quiet zone (the
// white card around it provides the rest of the padding).
func qrSVG(text string) (string, error) {
	q, err := encodeQR(text)
	if err != nil {
		return "", err
	}
	n := q.Size()
	const pad = 2
	size := n + pad*2
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, size, size)
	b.WriteString(`<rect width="100%" height="100%" fill="#fff"/>`)
	b.WriteString(`<path fill="#000" d="`)
	for y := 0; y < n; y++ {
		x := 0
		for x < n {
			if !q.Module(x, y) {
				x++
				continue
			}
			run := 1
			for x+run < n && q.Module(x+run, y) {
				run++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", x+pad, y+pad, run, run)
			x += run
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}

// qrPNG renders text as a PNG QR code roughly px pixels wide.
func qrPNG(text string, px int) ([]byte, error) {
	q, err := encodeQR(text)
	if err != nil {
		return nil, err
	}
	n := q.Size()
	total := n + qrQuietZone*2
	scale := 1
	if px > 0 {
		scale = px / total
	}
	if scale < 1 {
		scale = 1
	}
	img := image.NewGray(image.Rect(0, 0, total*scale, total*scale))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	dark := color.Gray{0}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !q.Module(x, y) {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				row := (y+qrQuietZone)*scale + dy
				for dx := 0; dx < scale; dx++ {
					img.SetGray((x+qrQuietZone)*scale+dx, row, dark)
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
