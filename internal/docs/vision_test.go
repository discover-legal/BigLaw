// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/model"
)

// pngOf is a small grey PNG (seed varies it).
func pngOf(seed int) []byte {
	img := image.NewGray(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = byte(i*13 + seed)
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// visionService has a Fake vision model ("fake-vl") whose two readings answer a[i] and b[i] for the
// i-th page read; a Reply's Err is returned instead when set.
func visionService(t *testing.T, a, b []model.Reply) (*Service, *model.Fake) {
	t.Helper()
	s := newService(t)
	f := model.NewFake(map[string][]model.Reply{"transcribe-a": a, "transcribe-b": b})
	s.UseVision(f, "fake-vl")
	return s, f
}

func texts(ss ...string) []model.Reply {
	var r []model.Reply
	for _, s := range ss {
		r = append(r, model.Reply{Text: s})
	}
	return r
}

// imageParts are the decoded images a request carried.
func imageParts(t *testing.T, req model.Request) (out [][]byte) {
	for _, m := range req.Messages {
		parts, _ := m.Content.([]model.Part)
		for _, p := range parts {
			if p.Type == "image_url" {
				_, b64, _ := strings.Cut(p.ImageURL["url"], ";base64,")
				data, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, data)
			}
		}
	}
	return out
}

// mustBeEmpty fails unless no file, document, section or chunk is stored.
func mustBeEmpty(t *testing.T, s *Service) {
	t.Helper()
	for _, table := range []string{"files", "documents", "sections", "chunks"} {
		if n := count(t, s, table); n != 0 {
			t.Errorf("%s: %d rows, want none", table, n)
		}
	}
}

func TestSniffImageMIME(t *testing.T) { // v1 services/extract vector, plus WebP and TIFF
	for want, data := range map[string][]byte{
		"image/png": {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, "image/jpeg": {0xff, 0xd8, 0xff, 0xe0}, "image/gif": []byte("GIF89a..."),
		"image/webp": []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), "image/tiff": []byte("II*\x00\x08"), "": []byte("RIFF\x10\x00\x00\x00WAVE"),
	} {
		if got := sniffImage(data); got != want {
			t.Errorf("sniffImage(%q) = %q, want %q", data, got, want)
		}
	}
}

// TestExtractImageWithoutVisionDegrades (v1 vector, changed by DESIGN revision 5): with no vision
// model an image is not text and not refused: it is kept, waiting, with a note that names vision.
func TestExtractImageWithoutVisionDegrades(t *testing.T) {
	t.Parallel()
	s := newService(t)
	d, err := s.Ingest(t.Context(), "scan.png", "image/png", bytes.NewReader(pngOf(1)))
	if !errors.Is(err, ErrVisionNotConfigured) || d.Method != "vision" || !d.NoText || d.Text != "" || d.Chunks != 0 ||
		len(d.Notes) == 0 || !strings.Contains(strings.ToLower(strings.Join(d.Notes, " ")+err.Error()), "vision") {
		t.Errorf("image without vision: %+v %v", d, err)
	}
}

// TestVisionNotConfiguredTyped503KeepsOriginal: the upload is 503 vision_not_configured with the
// stored document (status needs_transcription), and the original is downloadable byte for byte.
func TestVisionNotConfiguredTyped503KeepsOriginal(t *testing.T) {
	t.Parallel()
	s := newService(t)
	srv := serveDocs(t, s)
	img := pngOf(2)
	resp, out := upload(t, srv.URL, "letter.png", img)
	doc, _ := out["document"].(map[string]any)
	if resp.StatusCode != 503 || out["errorType"] != "vision_not_configured" || !strings.Contains(out["error"].(string), "BIGLAW_MODELS") ||
		doc["status"] != "needs_transcription" || doc["id"] == "" || doc["noText"] != true {
		t.Fatalf("upload without vision: %d %v", resp.StatusCode, out)
	}
	got, err := fileData(s, []string{doc["id"].(string)}, doc["id"].(string))
	if err != nil || !bytes.Equal(got, img) || count(t, s, "chunks") != 0 {
		t.Errorf("the original is kept as uploaded: %v", err)
	}
}

// TestTextModelNeverSentImages (trap): with no vision model the service never calls a model for an
// image, even one that has a text model's script; the role map never falls back (TestRolesModel,
// and cmd's TestServeNeverSendsImagesToTheLead end to end).
func TestTextModelNeverSentImages(t *testing.T) {
	t.Parallel()
	s := newService(t)
	f := model.NewFake(map[string][]model.Reply{"lead": texts("PLAUSIBLE LETTER"), "transcribe-a": texts("x"), "transcribe-b": texts("x")})
	s.UseEmbedder(f, "embed") // a configured model client for another role does not make images go anywhere
	f.Vectors = func(string) ([]float32, error) { return []float32{1}, nil }
	if _, err := s.Ingest(t.Context(), "photo.jpg", "image/jpeg", bytes.NewReader(jpegOf(8, 8, 1))); !errors.Is(err, ErrVisionNotConfigured) {
		t.Fatalf("ingest: %v", err)
	}
	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("an image reached a model with no vision role: %+v", calls)
	}
	if _, err := (model.Roles{Lead: "qwen2.5:14b"}).Model(model.Vision); !errors.Is(err, model.ErrNotConfigured) {
		t.Errorf("the vision role falls back: %v", err)
	}
}

// TestUnreadableImageNotFabricated (trap, G11): for a noise image the two readings are different
// plausible letters, so the upload is 422 transcription_unreliable with no text and no chunks: nothing
// is stored at all.
func TestUnreadableImageNotFabricated(t *testing.T) {
	t.Parallel()
	s, f := visionService(t, texts("Dear Mr Smith, your lease of Unit 4 ends on 1 May 2026. Yours, J. Ortiz"),
		texts("To whom it may concern: payment of the invoice is overdue by thirty days. Regards, Accounts"))
	resp, out := upload(t, serveDocs(t, s).URL, "noise.png", pngOf(3))
	if resp.StatusCode != 422 || out["errorType"] != "transcription_unreliable" || !strings.Contains(out["error"].(string), "nothing was stored") {
		t.Errorf("noise image: %d %v", resp.StatusCode, out)
	}
	mustBeEmpty(t, s)
	if n := len(f.Calls()); n != 2 {
		t.Errorf("%d vision calls, want 2 independent readings", n)
	}
	a, b := f.Calls()[0], f.Calls()[1]
	if a.Messages[0].Content == b.Messages[0].Content || a.Model != "fake-vl" || a.Role != model.Vision || len(imageParts(t, a)) != 1 {
		t.Errorf("the readings are not independent calls to the vision model: %+v %+v", a, b)
	}
}

// TestIllegibleNeverFilled (G11): a reading marking a word [illegible] is kept as it is; the other
// reading's guess never fills it in.
func TestIllegibleNeverFilled(t *testing.T) {
	t.Parallel()
	s, _ := visionService(t, texts("The monthly rent for the premises is $500 payable on the first day of each month to the Landlord"),
		texts("The monthly rent for the premises is [illegible] payable on the first day of each month to the Landlord"))
	d, err := s.Ingest(t.Context(), "rent.png", "image/png", bytes.NewReader(pngOf(4)))
	if err != nil || !strings.Contains(d.Text, "[illegible]") || strings.Contains(d.Text, "$500") || d.Transcript == nil || d.Transcript.Agreement == 1 {
		t.Errorf("illegible reading: %q %+v %v", d.Text, d.Transcript, err)
	}
}

func TestBlankImageNoContent(t *testing.T) {
	t.Parallel()
	s, _ := visionService(t, texts("[blank]"), []model.Reply{{Err: &model.Error{Kind: model.ErrNoContent, Detail: "empty"}}})
	resp, out := upload(t, serveDocs(t, s).URL, "white.png", pngOf(5))
	if resp.StatusCode != 422 || out["errorType"] != "no_content" {
		t.Errorf("blank image: %d %v", resp.StatusCode, out)
	}
	mustBeEmpty(t, s)
	s2, _ := visionService(t, texts("[illegible] [illegible]"), texts("[blank]"))
	if _, err := s2.Ingest(t.Context(), "smudge.png", "image/png", bytes.NewReader(pngOf(6))); !errors.Is(err, ErrNoContent) {
		t.Errorf("only illegible marks is no content: %v", err)
	}
	s3, _ := visionService(t, texts("[blank]"), texts("Pay the sum of ten thousand dollars to the bearer"))
	if _, err := s3.Ingest(t.Context(), "half.png", "image/png", bytes.NewReader(pngOf(7))); !errors.Is(err, ErrUnreliable) {
		t.Errorf("one blank reading beside text is a disagreement, never a page of text: %v", err)
	}
}

// TestTranscriptCitationCappedAndDisclosed: a quote found in transcribed text is exact@transcript or
// tolerant@transcript at most 0.85, names the model and says to check the original; the document
// and its chunks carry the provenance, the calls' usage is recorded, and text documents are unchanged.
func TestTranscriptCitationCappedAndDisclosed(t *testing.T) {
	t.Parallel()
	c := 0.002
	letter := "NOTICE TO QUIT. The tenancy of 12 Elm Street ends on 30 June 2026. Signed, the Landlord."
	s, _ := visionService(t, []model.Reply{{Text: letter, Usage: model.Usage{InputTokens: 900, OutputTokens: 30, CostUSD: &c}}},
		[]model.Reply{{Text: letter, Usage: model.Usage{InputTokens: 950, OutputTokens: 30, CostUSD: &c}}})
	d, err := s.Ingest(t.Context(), "notice.jpg", "image/jpeg", bytes.NewReader(jpegOf(8, 8, 2)))
	if err != nil || d.Method != "vision" || d.Status != "ready" || d.Text != letter || d.Transcript == nil || d.Transcript.Model != "fake-vl" ||
		d.Transcript.Agreement != 1 || d.Transcript.Tokens != 1910 || d.Transcript.CostUSD == nil || *d.Transcript.CostUSD != 2*c || d.Chunks == 0 {
		t.Fatalf("transcribed: %+v %+v %v", d, d.Transcript, err)
	}
	if !strings.Contains(strings.Join(d.Notes, " "), "machine transcription of an image by fake-vl") {
		t.Errorf("notes: %v", d.Notes)
	}
	scope := []string{d.ID}
	for quote, method := range map[string]string{"ends on 30 June 2026": "exact@transcript", "ENDS ON  30 june 2026": "tolerant@transcript"} {
		v, err := s.VerifyQuote(t.Context(), scope, d.ID, quote, nil)
		if err != nil || !v.Verified || v.Method != method || v.Confidence != TranscriptCap || v.TranscribedBy != "fake-vl" || v.LowConfidence ||
			!strings.Contains(v.Note, "check against the original") {
			t.Errorf("%q: %+v %v", quote, v, err)
		}
	}
	hits, err := s.Search(t.Context(), scope, "tenancy", 5, 0)
	if err != nil || len(hits) == 0 || !hits[0].Transcribed {
		t.Errorf("search hit provenance: %+v %v", hits, err)
	}
	got, err := s.Get(t.Context(), scope, d.ID)
	if err != nil || got.Transcript == nil || len(got.Transcript.Pages) != 1 || got.Transcript.Pages[0].End != len(letter) || got.Method != "vision" {
		t.Errorf("stored provenance: %+v %v", got.Transcript, err)
	}
	text := ingestText(t, s, "lease.txt", "The tenancy of 12 Elm Street ends on 30 June 2026.")
	if v, _ := s.VerifyQuote(t.Context(), []string{text.ID}, text.ID, "ends on 30 June 2026", nil); v.Method != "exact" || v.Confidence != 1 || v.TranscribedBy != "" {
		t.Errorf("a text document's citation is not capped: %+v", v)
	}
}

// TestLowAgreementFindingsGatedIndividually: in a two-page scan whose second page's readings agree
// on 80% of their words, a quote from that page is flagged lowConfidence (PR 4b gates the finding)
// and one from the first page is not.
func TestLowAgreementFindingsGatedIndividually(t *testing.T) {
	t.Parallel()
	p1 := "Clause one. The Buyer shall pay the deposit within five business days of signing"
	s, _ := visionService(t, texts(p1, "The Seller warrants title to the goods free of liens and encumbrances of any kind"),
		texts(p1, "The Seller warrants title to the good free of lien and encumbrance of any kind"))
	d, err := s.Ingest(t.Context(), "contract.pdf", "application/pdf", bytes.NewReader(makePDF("", "")))
	if err != nil || d.Transcript == nil || len(d.Transcript.Pages) != 2 || !d.Transcript.LowConfidence || d.Transcript.Pages[0].Agreement != 1 ||
		d.Transcript.Pages[1].Agreement < unreliableBelow || d.Transcript.Pages[1].Agreement >= lowBelow {
		t.Fatalf("two-page scan: %+v %v", d.Transcript, err)
	}
	for quote, low := range map[string]bool{"pay the deposit within five business days": false, "warrants title to the goods": true} {
		if v, err := s.VerifyQuote(t.Context(), []string{d.ID}, d.ID, quote, nil); err != nil || !v.Verified || v.LowConfidence != low {
			t.Errorf("%q: lowConfidence %v, want %v (%v)", quote, v.LowConfidence, low, err)
		}
	}
}

// TestScannedPDFDCTPagesExtracted: a 2-page JPEG-scanned PDF gives 2 scans whose images are the
// embedded JPEGs byte for byte, sent as image parts (one per page reading), never decoded.
func TestScannedPDFDCTPagesExtracted(t *testing.T) {
	t.Parallel()
	j1, j2 := jpegOf(8, 8, 1), jpegOf(8, 8, 2)
	pdfData := buildPDF(2, pdfPage{Image: dctImage(j1)}, pdfPage{Image: dctImage(j2, "[/DCTDecode]")})
	x, err := Extract(t.Context(), pdfData)
	if err != nil || len(x.Scans) != 2 || x.ScanPages != 2 || len(x.Unsupported) != 0 {
		t.Fatalf("scans: %+v %v", x, err)
	}
	for i, want := range [][]byte{j1, j2} {
		im := x.Scans[i].Images[0]
		if x.Scans[i].Page != i+1 || len(x.Scans[i].Images) != 1 || !bytes.Equal(pdfData[im[0]:im[0]+im[1]], want) {
			t.Errorf("scan %d: %+v", i, x.Scans[i])
		}
	}
	s, f := visionService(t, texts("Page one of the deed", "Page two of the deed"), texts("Page one of the deed", "Page two of the deed"))
	d, err := s.Ingest(t.Context(), "deed.pdf", "application/pdf", bytes.NewReader(pdfData))
	if err != nil || d.Text != "Page one of the deed\fPage two of the deed" || d.Method != "vision" || d.Pages != 2 {
		t.Fatalf("ingest: %+v %v", d, err)
	}
	var sent [][]byte
	for _, c := range f.Calls() {
		sent = append(sent, imageParts(t, c)...)
	}
	if len(sent) != 4 || !bytes.Equal(sent[0], j1) || !bytes.Equal(sent[1], j1) || !bytes.Equal(sent[2], j2) || !bytes.Equal(sent[3], j2) {
		t.Errorf("image parts sent: %d", len(sent))
	}
}

// TestScanEncodingUnsupportedTyped: a scan stored as CCITT (or JBIG2, a Flate bitmap, a JPEG under
// another filter) and a TIFF are 422 scan_encoding_unsupported, nothing stored; in a PDF with a text
// layer such a page is only noted.
func TestScanEncodingUnsupportedTyped(t *testing.T) {
	t.Parallel()
	s, f := visionService(t, nil, nil)
	srv := serveDocs(t, s)
	for name, data := range map[string][]byte{
		"fax.pdf":   buildPDF(1, pdfPage{Image: dctImage([]byte("\x00\x01fax"), "/CCITTFaxDecode")}),
		"jbig2.pdf": buildPDF(1, pdfPage{Image: dctImage([]byte("\x00jb2"), "/JBIG2Decode")}),
		"flate.pdf": buildPDF(2, pdfPage{Image: dctImage(jpegOf(8, 8, 0))}, pdfPage{Image: dctImage(jpegOf(8, 8, 0), "[/FlateDecode /DCTDecode]")}),
		"empty.pdf": buildPDF(1, pdfPage{Image: strings.Replace(dctImage(jpegOf(8, 8, 0)), "/Length", "/Length -5 /Was", 1)}), // no image bytes
		"scan.tif":  append([]byte("II*\x00\x08\x00\x00\x00"), make([]byte, 64)...),
		"scan.tiff": append([]byte("MM\x00*\x00\x00\x00\x08"), make([]byte, 64)...),
	} {
		resp, out := upload(t, srv.URL, name, data)
		if resp.StatusCode != 422 || out["errorType"] != "scan_encoding_unsupported" || !strings.Contains(out["error"].(string), "JPEG") {
			t.Errorf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
	mustBeEmpty(t, s)
	d, err := s.Ingest(t.Context(), "mixed.pdf", "application/pdf", bytes.NewReader(buildPDF(2, pdfPage{Text: "Clause 1. Rent."},
		pdfPage{Image: dctImage([]byte("\x00fax"), "/CCITTFaxDecode")})))
	if err != nil || !strings.Contains(d.Text, "Clause 1. Rent.") || d.Transcript != nil || !strings.Contains(strings.Join(d.Notes, " "), "page 2") || len(f.Calls()) != 0 {
		t.Errorf("mixed PDF with a CCITT page: %+v %v", d, err)
	}
}

func TestTooManyPages413(t *testing.T) {
	t.Parallel()
	s, f := visionService(t, nil, nil)
	pages := make([]string, maxVisionPages+1) // all scans
	resp, out := upload(t, serveDocs(t, s).URL, "bundle.pdf", makePDF(pages...))
	if resp.StatusCode != http.StatusRequestEntityTooLarge || out["errorType"] != "too_many_pages" || !strings.Contains(out["error"].(string), "21") {
		t.Errorf("21 scanned pages: %d %v", resp.StatusCode, out)
	}
	mustBeEmpty(t, s)
	if len(f.Calls()) != 0 {
		t.Errorf("the vision model was called %d times for a refused document", len(f.Calls()))
	}
}

// TestMixedPDFTextLayerNotRewritten (trap): the text-less page of a mixed PDF is transcribed into its
// own place; the text-layer pages stay byte for byte what the text layer says, even when the vision
// model "reads" different words for them, and quotes from them are plain exact matches.
func TestMixedPDFTextLayerNotRewritten(t *testing.T) {
	t.Parallel()
	data := makePDF("The Lessee shall pay rent of 900 dollars.", "", "Clause 3. Notices in writing.")
	plain, err := Extract(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	lie := "The Lessee shall pay NO rent. Clause 3 is void. Signed by the Lessor on 2 May 2026."
	s, f := visionService(t, texts(lie), texts(lie))
	d, err := s.Ingest(t.Context(), "lease.pdf", "application/pdf", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	pages, want := strings.Split(d.Text, "\f"), strings.Split(plain.Text, "\f")
	if len(f.Calls()) != 2 || len(pages) != 3 || pages[0] != want[0] || pages[2] != want[2] || pages[1] != lie || d.Method != "text_layer" {
		t.Fatalf("mixed PDF: %d calls, %q", len(f.Calls()), d.Text)
	}
	if d.Transcript == nil || len(d.Transcript.Pages) != 1 || d.Transcript.Pages[0].Page != 2 {
		t.Errorf("only page 2 is transcribed: %+v", d.Transcript)
	}
	if v, _ := s.VerifyQuote(t.Context(), []string{d.ID}, d.ID, "pay rent of 900 dollars", nil); v.Method != "exact" || v.Confidence != 1 {
		t.Errorf("a text-layer quote: %+v", v)
	}
	var mixed int
	if err := s.db.QueryRow(`SELECT count(*) FROM chunks WHERE document_id = $1 AND transcribed AND byte_end <= $2`, d.ID, len(want[0])).Scan(&mixed); err != nil || mixed != 0 {
		t.Errorf("a chunk of page 1 alone is marked transcribed: %d %v", mixed, err)
	}
	s2, _ := visionService(t, texts("Signed by the Lessor"), texts("Totally different words here now"))
	d2, err := s2.Ingest(t.Context(), "lease2.pdf", "application/pdf", bytes.NewReader(data))
	if err != nil || d2.Text != plain.Text || d2.Transcript != nil || !strings.Contains(strings.Join(d2.Notes, " "), "disagreed") {
		t.Errorf("an unreliable page of a mixed PDF is noted, the text layer kept: %+v %v", d2, err)
	}
}

// blockingChat stands in for a vision model call that never answers: it ends as model.Client's
// does, ErrTimeout at a deadline and context.Canceled on a cancel (a server killed mid-call).
type blockingChat struct{ started chan struct{} }

func (b blockingChat) Chat(ctx context.Context, _ model.Request) (*model.Response, error) {
	close(b.started)
	<-ctx.Done()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, &model.Error{Kind: model.ErrTimeout, Detail: "the caller's deadline passed"}
	}
	return nil, ctx.Err()
}

// TestTranscriptionJobResumesAfterRestart (DB): an image stored while no vision role was set is
// transcribed by a later server. A server killed mid-job leaves it leased; after a restart it is taken
// again once the lease ends, and completes under its original id with its original file. A document
// whose readings disagree becomes failed with the reason; a model outage is retried, then failed.
func TestTranscriptionJobResumesAfterRestart(t *testing.T) {
	t.Parallel()
	s := newService(t)
	img := pngOf(9)
	pending, err := s.Ingest(t.Context(), "photo.png", "image/png", bytes.NewReader(img))
	if !errors.Is(err, ErrVisionNotConfigured) {
		t.Fatal(err)
	}
	killed := New(s.st)
	bc := blockingChat{make(chan struct{})}
	killed.UseVision(bc, "fake-vl")
	ctx, kill := context.WithCancel(t.Context())
	go func() { <-bc.started; kill() }()
	if _, _, err := killed.TranscribePending(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("killed job: %v", err)
	}
	var status string
	var leased bool
	s.db.QueryRow(`SELECT status, lease_until > now() FROM documents WHERE id = $1`, pending.ID).Scan(&status, &leased)
	if status != "needs_transcription" || !leased {
		t.Fatalf("after the kill: %s leased=%v", status, leased)
	}
	letter := "Dear Ms Rao, the hearing is adjourned to 4 March 2027."
	restarted := New(s.st)
	f := model.NewFake(map[string][]model.Reply{"transcribe-a": texts(letter, "A cat sat"), "transcribe-b": texts(letter, "Invoice overdue please pay")})
	restarted.UseVision(f, "fake-vl")
	if done, failed, err := restarted.TranscribePending(t.Context()); done+failed != 0 || err != nil {
		t.Fatalf("a leased job is not taken: %d %d %v", done, failed, err)
	}
	s.db.Exec(`UPDATE documents SET lease_until = now() - interval '1 second' WHERE id = $1`, pending.ID) // its lease has ended
	other, _ := s.Ingest(t.Context(), "other.png", "image/png", bytes.NewReader(pngOf(10)))
	done, failed, err := restarted.TranscribePending(t.Context())
	got, gerr := s.Get(t.Context(), []string{pending.ID}, pending.ID)
	if done != 1 || failed != 1 || err != nil || gerr != nil || got.Status != "ready" || got.Text != letter || got.Method != "vision" || got.Chunks == 0 || got.Transcript == nil {
		t.Fatalf("resumed: %d %d %v; %+v %v", done, failed, err, got, gerr)
	}
	if data, err := fileData(s, []string{pending.ID}, pending.ID); err != nil || !bytes.Equal(data, img) {
		t.Errorf("the original file is kept: %v", err)
	}
	bad, _ := s.Get(t.Context(), []string{other.ID}, other.ID)
	if bad.Status != "failed" || len(bad.Notes) != 1 || !strings.Contains(bad.Notes[0], "transcription unreliable") || bad.Chunks != 0 {
		t.Errorf("refused job: %+v", bad)
	}
	third, _ := s.Ingest(t.Context(), "third.png", "image/png", bytes.NewReader(pngOf(11)))
	down := model.NewFake(map[string][]model.Reply{"transcribe-a": {{Err: &model.Error{Kind: model.ErrUnavailable, Detail: "502"}}, {Err: &model.Error{Kind: model.ErrUnavailable, Detail: "502"}},
		{Err: &model.Error{Kind: model.ErrUnavailable, Detail: "502"}}}})
	outage := New(s.st)
	outage.UseVision(down, "fake-vl")
	for attempt := 1; attempt <= 3; attempt++ {
		_, failed, err := outage.TranscribePending(t.Context())
		if attempt < 3 && (!errors.Is(err, model.ErrUnavailable) || failed != 0) || attempt == 3 && (err != nil || failed != 1) {
			t.Fatalf("attempt %d: %d %v", attempt, failed, err)
		}
		s.db.Exec(`UPDATE documents SET lease_until = now() - interval '1 second' WHERE id = $1`, third.ID)
	}
	if d, _ := s.Get(t.Context(), []string{third.ID}, third.ID); d.Status != "failed" || !strings.Contains(d.Notes[0], "3 times") {
		t.Errorf("after 3 outages: %+v", d)
	}
	// A document whose three claims all died with their server (OOM-kill, a crash): the fourth claim
	// fails it without calling the model, instead of retrying it every lease forever.
	crasher, _ := s.Ingest(t.Context(), "crasher.png", "image/png", bytes.NewReader(pngOf(14)))
	s.db.Exec(`UPDATE documents SET attempts = 3, lease_until = now() - interval '1 second' WHERE id = $1`, crasher.ID)
	untouched := model.NewFake(nil)
	last := New(s.st)
	last.UseVision(untouched, "fake-vl")
	if _, failed, err := last.TranscribePending(t.Context()); failed != 1 || err != nil || len(untouched.Calls()) != 0 {
		t.Errorf("fourth claim: failed %d, %v, %d calls", failed, err, len(untouched.Calls()))
	}
	if d, _ := s.Get(t.Context(), []string{crasher.ID}, crasher.ID); d.Status != "failed" || !strings.Contains(d.Notes[0], "server stopped") {
		t.Errorf("after 3 server deaths: %+v", d)
	}
}

// TestVisionFailuresTyped: a failed vision call is its G6 status and stores nothing; a reading cut at
// the output limit is never stored as a whole page, nor one over the text cap; the vision calls are
// bounded by visionWait.
func TestVisionFailuresTyped(t *testing.T) { // not parallel: it shortens maxText and visionWait
	oldText := maxText
	maxText = 64 << 10 // the server's cap only (images never reach the child)
	defer func() { maxText = oldText }()
	for _, c := range []struct {
		reply model.Reply
		code  int
		kind  string
	}{
		{model.Reply{Err: &model.Error{Kind: model.ErrUnavailable, Detail: "the endpoint answered 500"}}, 502, "model_unavailable"},
		{model.Reply{Err: &model.Error{Kind: model.ErrRateLimited, Detail: "429"}}, 503, "model_rate_limited"},
		{model.Reply{Err: &model.Error{Kind: model.ErrTimeout, Detail: "timeout"}}, 504, "model_timeout"},
		{model.Reply{Text: "The first half of the page", Truncated: true}, 422, "transcription_unreliable"},
		{model.Reply{Text: strings.Repeat("runaway ", maxText/8+1)}, 422, "unreadable"}, // more text than a document holds: stopped
	} {
		s, _ := visionService(t, []model.Reply{c.reply}, texts("The first half of the page"))
		resp, out := upload(t, serveDocs(t, s).URL, "p.png", pngOf(12))
		if resp.StatusCode != c.code || out["errorType"] != c.kind {
			t.Errorf("%+v: %d %v", c.reply, resp.StatusCode, out)
		}
		mustBeEmpty(t, s)
	}
	old := visionWait
	visionWait = 200 * time.Millisecond
	defer func() { visionWait = old }()
	s := newService(t)
	s.UseVision(blockingChat{make(chan struct{})}, "fake-vl")
	start := time.Now()
	if _, err := s.Ingest(t.Context(), "slow.png", "image/png", bytes.NewReader(pngOf(13))); !errors.Is(err, model.ErrTimeout) || time.Since(start) > 5*time.Second {
		t.Errorf("a hung vision call: %v after %s", err, time.Since(start))
	}
}
