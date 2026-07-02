// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package pdfgen renders documents — text plus placed images — to PDF using a
// pure-Go engine (go-pdf/fpdf, no cgo, single static binary, Pi-friendly). It
// is the output side of BigLaw's "ingest and place images": a document's
// retained image attachments are embedded inline. PyMuPDF is no longer used for
// generation (it remains the reader/rasterizer for the ingest pipeline).
package pdfgen

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"

	"github.com/go-pdf/fpdf"

	// Image decoders for the normalize-to-PNG fallback (formats fpdf can't embed
	// natively). Stdlib covers png/jpeg/gif; these add webp/bmp/tiff.
	_ "image/gif"
	_ "image/jpeg"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Image is one image to place in the document.
type Image struct {
	MediaType string // e.g. "image/png"
	Data      []byte
	Caption   string
}

const (
	pageW      = 210.0 // A4 mm
	margin     = 20.0
	contentW   = pageW - 2*margin
	maxImageH  = 210.0 // cap image height so a tall scan doesn't overflow a page
	bodyFont   = "Helvetica"
)

// Generate renders a title, a text body (light Markdown: #/##/### headings and
// - bullets), and a sequence of images into a PDF, returning the bytes.
func Generate(title, body string, images []Image) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(margin, margin, margin)
	pdf.SetAutoPageBreak(true, margin)
	pdf.AddPage()
	tr := pdf.UnicodeTranslatorFromDescriptor("") // UTF-8 → core-font encoding

	if title != "" {
		pdf.SetFont(bodyFont, "B", 16)
		pdf.MultiCell(0, 8, tr(title), "", "L", false)
		pdf.Ln(3)
	}

	renderBody(pdf, tr, body)

	for i, img := range images {
		if err := placeImage(pdf, tr, fmt.Sprintf("img%d", i), img); err != nil {
			// A bad image must not abort the whole document; note it inline.
			pdf.SetFont(bodyFont, "I", 9)
			pdf.MultiCell(0, 5, tr(fmt.Sprintf("[image could not be embedded: %s]", err.Error())), "", "L", false)
		}
	}

	if pdf.Err() {
		return nil, pdf.Error()
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderBody writes the text body with light Markdown handling.
func renderBody(pdf *fpdf.Fpdf, tr func(string) string, body string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "### "):
			pdf.SetFont(bodyFont, "B", 11)
			pdf.MultiCell(0, 6, tr(line[4:]), "", "L", false)
		case strings.HasPrefix(line, "## "):
			pdf.SetFont(bodyFont, "B", 12)
			pdf.MultiCell(0, 7, tr(line[3:]), "", "L", false)
		case strings.HasPrefix(line, "# "):
			pdf.SetFont(bodyFont, "B", 13)
			pdf.MultiCell(0, 7, tr(line[2:]), "", "L", false)
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			pdf.SetFont(bodyFont, "", 11)
			pdf.MultiCell(0, 5.5, tr("  • "+line[2:]), "", "L", false)
		case strings.TrimSpace(line) == "":
			pdf.Ln(3)
		default:
			pdf.SetFont(bodyFont, "", 11)
			pdf.MultiCell(0, 5.5, tr(line), "", "L", false)
		}
	}
}

// placeImage embeds one image, scaled to the content width (aspect preserved,
// height capped). Formats fpdf can't read natively are normalized to PNG first.
func placeImage(pdf *fpdf.Fpdf, tr func(string) string, name string, img Image) error {
	imgType, data, err := toEmbeddable(img.MediaType, img.Data)
	if err != nil {
		return err
	}
	opts := fpdf.ImageOptions{ImageType: imgType, ReadDpi: true}
	info := pdf.RegisterImageOptionsReader(name, opts, bytes.NewReader(data))
	if pdf.Err() {
		err := pdf.Error()
		pdf.ClearError()
		return err
	}
	if info == nil || info.Width() <= 0 {
		return fmt.Errorf("unreadable image")
	}

	w := contentW
	h := w * info.Height() / info.Width()
	if h > maxImageH {
		h = maxImageH
		w = h * info.Width() / info.Height()
	}

	pdf.Ln(3)
	pdf.ImageOptions(name, margin, pdf.GetY(), w, h, true, opts, 0, "")
	if img.Caption != "" {
		pdf.SetFont(bodyFont, "I", 9)
		pdf.MultiCell(0, 5, tr(img.Caption), "", "L", false)
	}
	pdf.Ln(3)
	return nil
}

// toEmbeddable maps a media type to an fpdf image type, decoding+re-encoding to
// PNG for formats fpdf cannot read directly (webp/bmp/tiff and anything else).
func toEmbeddable(mediaType string, data []byte) (fpdfType string, out []byte, err error) {
	switch strings.ToLower(mediaType) {
	case "image/png":
		return "PNG", data, nil
	case "image/jpeg", "image/jpg":
		return "JPG", data, nil
	case "image/gif":
		return "GIF", data, nil
	}
	// Fallback: decode (png/jpeg/gif/webp/bmp/tiff are registered) → PNG.
	im, _, derr := image.Decode(bytes.NewReader(data))
	if derr != nil {
		return "", nil, fmt.Errorf("unsupported image type %q", mediaType)
	}
	var buf bytes.Buffer
	if eerr := png.Encode(&buf, im); eerr != nil {
		return "", nil, eerr
	}
	return "PNG", buf.Bytes(), nil
}
