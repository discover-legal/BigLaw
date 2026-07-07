// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package pdfgen

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"golang.org/x/image/bmp"
)

func makeBMP(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Set(x, y, color.RGBA{R: 200, G: uint8(x), B: uint8(y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, im); err != nil {
		t.Fatalf("encode bmp: %v", err)
	}
	return buf.Bytes()
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestGenerateTextOnly(t *testing.T) {
	out, err := Generate("Memo", "# Heading\n\nA paragraph.\n- a bullet\n- another", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Errorf("not a PDF: %q", out[:8])
	}
	if len(out) < 500 {
		t.Errorf("PDF suspiciously small: %d bytes", len(out))
	}
}

func TestGenerateEmbedsImage(t *testing.T) {
	pngBytes := makePNG(t, 120, 80)
	withImg, err := Generate("Exhibit A", "See the figure below.", []Image{
		{MediaType: "image/png", Data: pngBytes, Caption: "Figure 1"},
	})
	if err != nil {
		t.Fatalf("Generate with image: %v", err)
	}
	textOnly, err := Generate("Exhibit A", "See the figure below.", nil)
	if err != nil {
		t.Fatalf("Generate text only: %v", err)
	}
	if !bytes.HasPrefix(withImg, []byte("%PDF")) {
		t.Fatal("not a PDF")
	}
	// The image-bearing PDF must be meaningfully larger (the image bytes are
	// embedded), proving the image was placed, not dropped.
	if len(withImg) <= len(textOnly)+len(pngBytes)/2 {
		t.Errorf("image does not appear embedded: withImg=%d textOnly=%d png=%d",
			len(withImg), len(textOnly), len(pngBytes))
	}
}

func TestGenerateNormalizesUnsupportedImage(t *testing.T) {
	// A BMP is not natively embeddable by fpdf; the generator must decode and
	// re-encode it rather than fail.
	bmp := makeBMP(t, 40, 40)
	out, err := Generate("Scan", "", []Image{{MediaType: "image/bmp", Data: bmp}})
	if err != nil {
		t.Fatalf("Generate bmp: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Error("not a PDF")
	}
}
