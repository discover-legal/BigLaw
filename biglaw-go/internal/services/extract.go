// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Hybrid, LLM-centric document extraction for omnimodal uploads.
//
// The platform accepts more than plain text now: PDFs (digital and scanned),
// Word documents, and standalone images. Extraction follows the policy chosen
// for BigLaw — the embedded TEXT LAYER is ground truth (copied verbatim so a
// contract clause is never silently paraphrased), and a vision model (Qwen-VL
// by default) reconciles it: filling in what the text layer cannot carry
// (scans, tables, stamps, signatures, figures) and transcribing image-only
// documents outright.
//
// Pure-Go does the mechanical plumbing (text-layer extraction, DOCX XML, base64)
// but the understanding is the LLM's: vision transcription of images/scans and a
// reconciliation pass that merges the text layer with the vision read. PDF page
// rasterization for the vision pass uses the bundled PyMuPDF script when present
// and degrades gracefully (text-layer only, with a note) when it is not.
package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ledongthuc/pdf"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

// Extraction tuning. All overridable via env so an operator can trade cost for
// coverage without a rebuild.
var (
	// visionMaxPages caps how many PDF pages are rasterized + sent to the
	// vision model per upload (bounds per-upload token spend; the cap is
	// logged, never silently applied).
	visionMaxPages = envIntDefault("EXTRACT_VISION_MAX_PAGES", 8)
	// visionMaxTokens bounds each vision/reconcile completion.
	visionMaxTokens = envIntDefault("EXTRACT_VISION_MAX_TOKENS", 4096)
	// sparsePageChars: a PDF averaging fewer than this many text-layer chars
	// per page is treated as scanned/image-heavy and routed to vision.
	sparsePageChars = envIntDefault("EXTRACT_SPARSE_PAGE_CHARS", 80)
)

// imageExts are standalone image uploads handled vision-first (no text layer).
var imageExts = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
	".tif": "image/tiff", ".tiff": "image/tiff",
}

// ExtractMethod records how the text was produced (surfaced to the user/audit).
type ExtractMethod string

const (
	MethodPlain      ExtractMethod = "plain"             // read as text, no model
	MethodTextLayer  ExtractMethod = "text-layer"        // embedded text, verbatim
	MethodVision     ExtractMethod = "vision"            // VLM transcription (image/scan)
	MethodReconciled ExtractMethod = "hybrid-reconciled" // text layer + VLM reconcile
	MethodNone       ExtractMethod = "none"              // nothing extractable
)

// ExtractResult is the outcome of an extraction.
type ExtractResult struct {
	Text   string
	Method ExtractMethod
	Pages  int
	// Notes are human-facing remarks (e.g. degradations) for the API response.
	Notes []string
}

func (r *ExtractResult) note(format string, a ...interface{}) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, a...))
}

// DocumentExtractor performs hybrid extraction. The text provider/model handle
// the reconciliation call; the vision provider/model handle image and scanned
// content. They may be the same provider with different model IDs (the common
// Qwen case: qwen-plus reconciles, qwen-vl-max sees).
type DocumentExtractor struct {
	textProvider   providers.Provider
	textModel      string
	visionProvider providers.Provider
	visionModel    string
	pythonBin      string // PyMuPDF rasterizer for scanned/hybrid PDF (optional)
	pdfScript      string
}

// NewDocumentExtractor builds an extractor. visionProvider may be nil (vision
// disabled — extraction falls back to text-layer only with a note).
func NewDocumentExtractor(textProvider providers.Provider, textModel string,
	visionProvider providers.Provider, visionModel, pythonBin, pdfScript string) *DocumentExtractor {
	return &DocumentExtractor{
		textProvider:   textProvider,
		textModel:      textModel,
		visionProvider: visionProvider,
		visionModel:    visionModel,
		pythonBin:      pythonBin,
		pdfScript:      pdfScript,
	}
}

// Extract dispatches on file type and returns the extracted text. It never
// panics on malformed input; a genuinely unextractable file yields
// MethodNone with explanatory notes so the caller can return a helpful 422.
func (e *DocumentExtractor) Extract(filename string, data []byte) ExtractResult {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("document extraction panicked", "file", filename, "recover", r)
		}
	}()

	ext := strings.ToLower(filepath.Ext(filename))
	isPDF := ext == ".pdf" || bytes.HasPrefix(data, []byte("%PDF-"))
	isDOCX := ext == ".docx" || (bytes.HasPrefix(data, []byte("PK\x03\x04")) && ext != ".pdf" && imageExts[ext] == "")

	switch {
	case imageExts[ext] != "" || isImageMagic(data):
		return e.extractImage(filename, data)
	case isPDF:
		return e.extractPDF(data)
	case isDOCX && looksLikeDocx(data):
		return e.extractDOCX(data)
	default:
		// Plain text / Markdown / CSV / JSON / unknown-but-textual.
		return ExtractResult{Text: string(data), Method: MethodPlain}
	}
}

// VisionAvailable reports whether the extractor can use a vision model.
func (e *DocumentExtractor) VisionAvailable() bool {
	return e.visionProvider != nil && strings.TrimSpace(e.visionModel) != ""
}

// rasterAvailable reports whether PDF page rasterization (PyMuPDF) is usable.
func (e *DocumentExtractor) rasterAvailable() bool {
	if e.pythonBin == "" || e.pdfScript == "" {
		return false
	}
	if _, err := exec.LookPath(e.pythonBin); err != nil {
		return false
	}
	if _, err := os.Stat(e.pdfScript); err != nil {
		return false
	}
	return true
}

// ─── Images ─────────────────────────────────────────────────────────────────

func (e *DocumentExtractor) extractImage(filename string, data []byte) ExtractResult {
	res := ExtractResult{Method: MethodVision, Pages: 1}
	mt := imageExts[strings.ToLower(filepath.Ext(filename))]
	if mt == "" {
		mt = sniffImageMIME(data)
	}
	if !e.VisionAvailable() {
		res.Method = MethodNone
		res.note("Image uploads need a vision model. Configure MODEL_VISION (e.g. qwen-vl-max) and the stack API key.")
		return res
	}
	text, err := e.visionRead([]visionImage{{mediaType: mt, b64: base64.StdEncoding.EncodeToString(data)}}, imageInstruction)
	if err != nil {
		res.Method = MethodNone
		res.note("Vision extraction failed: %s", err.Error())
		return res
	}
	res.Text = text
	return res
}

// ─── PDF ────────────────────────────────────────────────────────────────────

func (e *DocumentExtractor) extractPDF(data []byte) ExtractResult {
	res := ExtractResult{}
	ground, pages := pdfTextLayer(data)
	res.Pages = pages
	groundTrim := strings.TrimSpace(ground)

	sparse := pages > 0 && len(groundTrim) < pages*sparsePageChars
	if pages == 0 {
		sparse = len(groundTrim) == 0
	}

	// Vision unavailable → best effort on the text layer.
	if !e.VisionAvailable() || !e.rasterAvailable() {
		switch {
		case groundTrim != "":
			res.Text = ground
			res.Method = MethodTextLayer
			if sparse {
				res.note("This PDF looks scanned/image-heavy and vision reconcile is unavailable (need MODEL_VISION + Python/PyMuPDF); returned the embedded text layer only, which may be incomplete.")
			}
		default:
			res.Method = MethodNone
			res.note("No embedded text and no vision pipeline available. Install Python + PyMuPDF (requirements.txt) and set MODEL_VISION to read scanned PDFs.")
		}
		return res
	}

	// Long, text-rich PDFs: skip the (expensive) per-page vision pass; the text
	// layer is already authoritative. Log the bound rather than silently cap.
	if !sparse && pages > visionMaxPages {
		res.Text = ground
		res.Method = MethodTextLayer
		res.note("Used the embedded text layer; skipped vision reconcile because the document is %d pages (> EXTRACT_VISION_MAX_PAGES=%d). Raise the cap to vision-reconcile long documents.", pages, visionMaxPages)
		return res
	}

	imgs, capped, err := e.rasterizePDF(data, visionMaxPages)
	if err != nil || len(imgs) == 0 {
		// Rasterization failed at runtime — fall back to the text layer.
		if groundTrim != "" {
			res.Text = ground
			res.Method = MethodTextLayer
			res.note("Vision reconcile unavailable at runtime (%v); used the embedded text layer.", errOrEmpty(err))
			return res
		}
		res.Method = MethodNone
		res.note("Could not rasterize this PDF for vision and it has no text layer: %v", errOrEmpty(err))
		return res
	}

	visionText, verr := e.visionRead(imgs, pdfPageInstruction)
	if verr != nil {
		if groundTrim != "" {
			res.Text = ground
			res.Method = MethodTextLayer
			res.note("Vision read failed (%s); used the embedded text layer.", verr.Error())
			return res
		}
		res.Method = MethodNone
		res.note("Vision read failed and no text layer present: %s", verr.Error())
		return res
	}

	if capped {
		res.note("Vision reconcile covered the first %d of %d pages (EXTRACT_VISION_MAX_PAGES); the text layer carries the remainder.", visionMaxPages, pages)
	}

	// Scanned/sparse: the vision read IS the document.
	if groundTrim == "" {
		res.Text = visionText
		res.Method = MethodVision
		return res
	}

	// Hybrid: text layer is ground truth, vision fills gaps.
	merged, merr := e.reconcile(ground, visionText)
	if merr != nil || strings.TrimSpace(merged) == "" {
		res.Text = ground
		res.Method = MethodTextLayer
		if merr != nil {
			res.note("Reconcile pass failed (%s); used the verbatim text layer.", merr.Error())
		}
		return res
	}
	res.Text = merged
	res.Method = MethodReconciled
	return res
}

// pdfTextLayer extracts the embedded text and page count via pure Go.
func pdfTextLayer(data []byte) (text string, pages int) {
	defer func() { _ = recover() }() // ledongthuc/pdf can panic on malformed PDFs
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", 0
	}
	pages = r.NumPage()
	rd, err := r.GetPlainText()
	if err != nil {
		return "", pages
	}
	var sb strings.Builder
	if _, err := io.Copy(&sb, rd); err != nil {
		return "", pages
	}
	return sb.String(), pages
}

// rasterizePDF renders up to maxPages pages to PNG via the PyMuPDF script and
// returns them as vision inputs. capped is true when the PDF had more pages
// than maxPages.
func (e *DocumentExtractor) rasterizePDF(data []byte, maxPages int) (imgs []visionImage, capped bool, err error) {
	tmp := filepath.Join(os.TempDir(), "biglaw-extract-"+uuid.New().String()+".pdf")
	if werr := os.WriteFile(tmp, data, 0600); werr != nil {
		return nil, false, werr
	}
	defer os.Remove(tmp)

	argsJSON, _ := json.Marshal(map[string]interface{}{"path": tmp, "maxPages": maxPages, "dpi": 150})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, cerr := exec.CommandContext(ctx, e.pythonBin, e.pdfScript, "render_pages", string(argsJSON)).Output()
	if cerr != nil {
		return nil, false, cerr
	}
	var result struct {
		Pages []struct {
			PNGBase64 string `json:"png_base64"`
		} `json:"pages"`
		TotalPages int  `json:"total_pages"`
		Capped     bool `json:"capped"`
	}
	if jerr := json.Unmarshal(out, &result); jerr != nil {
		return nil, false, jerr
	}
	for _, p := range result.Pages {
		if p.PNGBase64 != "" {
			imgs = append(imgs, visionImage{mediaType: "image/png", b64: p.PNGBase64})
		}
	}
	return imgs, result.Capped, nil
}

// ─── DOCX ─────────────────────────────────────────────────────────────────────

// extractDOCX pulls the full text from a Word document's word/document.xml.
// This is the verbatim ground truth; DOCX is rarely scanned so no vision pass
// is run (embedded images inside a DOCX are noted but not OCR'd here).
func (e *DocumentExtractor) extractDOCX(data []byte) ExtractResult {
	text := docxFullText(data)
	if strings.TrimSpace(text) == "" {
		return ExtractResult{Method: MethodNone, Notes: []string{"No readable text found in this Word document."}}
	}
	return ExtractResult{Text: text, Method: MethodTextLayer}
}

// ─── Vision + reconcile model calls ────────────────────────────────────────────

type visionImage struct {
	mediaType string
	b64       string
}

const imageInstruction = "You are a meticulous legal document transcriber. Transcribe ALL text visible in this image faithfully and completely, preserving structure, headings, lists, tables, and any stamps, signatures, or handwritten notes (describe the latter in [brackets]). Output only the transcription — no preamble, no commentary."

const pdfPageInstruction = "You are a meticulous legal document transcriber. These images are consecutive pages of one document. Transcribe ALL text faithfully and completely across the pages, preserving structure, headings, lists, and tables. Note stamps, signatures, seals, and handwriting in [brackets]. Output only the transcription — no preamble."

// visionRead sends one or more images to the vision model and returns the
// transcription. Records cost under the "document_extraction" context.
func (e *DocumentExtractor) visionRead(imgs []visionImage, instruction string) (string, error) {
	if e.visionProvider == nil {
		return "", fmt.Errorf("no vision provider configured")
	}
	blocks := []providers.ContentBlock{{Type: providers.BlockText, Text: instruction}}
	for _, im := range imgs {
		blocks = append(blocks, providers.ImageBlock(im.mediaType, im.b64))
	}
	return e.chat(e.visionProvider, e.visionModel, "", []providers.Message{{Role: "user", Content: blocks}})
}

// reconcile merges the verbatim text layer with the vision read, anchoring on
// the text layer (never reworded) and folding in vision-only content.
func (e *DocumentExtractor) reconcile(textLayer, visionText string) (string, error) {
	if e.textProvider == nil {
		return "", fmt.Errorf("no text provider configured")
	}
	system := "You reconcile two extractions of the SAME legal document into one faithful text. " +
		"RULES: (1) The TEXT LAYER is authoritative — reproduce it VERBATIM; never paraphrase, summarize, correct, or reorder its wording. " +
		"(2) Use the VISION READ ONLY to add content the text layer is missing — text inside tables/figures, stamps, seals, signatures, handwriting, or pages that were image-only. " +
		"(3) Insert vision-only additions in their correct position; mark purely visual elements like [STAMP: ...], [SIGNATURE: ...]. " +
		"(4) Do not invent content. Output ONLY the reconciled document text, no preamble or commentary."
	user := fmt.Sprintf("=== TEXT LAYER (authoritative, verbatim) ===\n%s\n\n=== VISION READ (gap-fill only) ===\n%s", textLayer, visionText)
	return e.chat(e.textProvider, e.textModel, system, []providers.Message{{Role: "user", Content: user}})
}

// chat issues a completion and records its cost, returning the text content.
func (e *DocumentExtractor) chat(p providers.Provider, model, system string, msgs []providers.Message) (string, error) {
	start := time.Now()
	resp, err := p.Chat(providers.ChatParams{
		Model:     model,
		MaxTokens: visionMaxTokens,
		System:    system,
		Messages:  msgs,
	})
	if err != nil {
		return "", err
	}
	dms := time.Since(start).Milliseconds()
	var cw, cr int
	if resp.Usage.CacheWriteTokens != nil {
		cw = *resp.Usage.CacheWriteTokens
	}
	if resp.Usage.CacheReadTokens != nil {
		cr = *resp.Usage.CacheReadTokens
	}
	cost.Default.Record(cost.RecordRequest{
		Model:        model,
		Provider:     providerName(model),
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		CostUSD:      cost.CalcCostUSD(model, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr),
		DurationMs:   dms,
		Context:      "document_extraction",
	})
	var sb strings.Builder
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			sb.WriteString(blk.Text)
		}
	}
	return sb.String(), nil
}

// ─── small helpers ──────────────────────────────────────────────────────────

func providerName(model string) string {
	if strings.HasPrefix(strings.ToLower(model), "claude") {
		return "anthropic"
	}
	return "openai-compatible"
}

func errOrEmpty(err error) interface{} {
	if err == nil {
		return "no pages produced"
	}
	return err
}

func looksLikeDocx(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04"))
}

func isImageMagic(data []byte) bool { return sniffImageMIME(data) != "" }

// sniffImageMIME identifies common image formats by magic bytes.
func sniffImageMIME(d []byte) string {
	switch {
	case len(d) >= 8 && bytes.Equal(d[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png"
	case len(d) >= 3 && d[0] == 0xff && d[1] == 0xd8 && d[2] == 0xff:
		return "image/jpeg"
	case len(d) >= 6 && (bytes.HasPrefix(d, []byte("GIF87a")) || bytes.HasPrefix(d, []byte("GIF89a"))):
		return "image/gif"
	case len(d) >= 12 && bytes.Equal(d[:4], []byte("RIFF")) && bytes.Equal(d[8:12], []byte("WEBP")):
		return "image/webp"
	case len(d) >= 2 && d[0] == 'B' && d[1] == 'M':
		return "image/bmp"
	case len(d) >= 4 && (bytes.HasPrefix(d, []byte{0x49, 0x49, 0x2a, 0x00}) || bytes.HasPrefix(d, []byte{0x4d, 0x4d, 0x00, 0x2a})):
		return "image/tiff"
	}
	return ""
}

func envIntDefault(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}
