// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // image.DecodeConfig checks an image's header before it is stored or sent
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/model"
)

// The vision read (README § Vision). Each page is read twice, independently (two prompts), and the
// readings' words compared: below unreliableBelow nothing is stored as text; below lowBelow the
// page is low confidence. A citation into transcribed text is capped at TranscriptCap.
const (
	unreliableBelow, lowBelow, TranscriptCap = 0.75, 0.9, 0.85
	pendingNote                              = "Waiting for transcription: this is an image or a scan and no vision model is configured. The original is kept."
)

var (
	visionWait = 10 * time.Minute // all the vision calls of one document
	//go:embed vision_prompts.txt
	prompts                string
	readPrompts            = strings.Split(strings.TrimSpace(prompts), "\n") // two independent readings
	ErrVisionNotConfigured = errors.New("docs: no vision model is configured (set vision=<model id> in BIGLAW_MODELS): the original is stored with status needs_transcription and is transcribed once one is")
	ErrUnreliable          = errors.New("docs: transcription unreliable")
	ErrNoContent           = errors.New("docs: no content: the vision model found no readable text")
	ErrScanEncoding        = errors.New("docs: scan encoding unsupported: BigLaw sends PNG, JPEG, GIF, WebP and PDFs whose pages are JPEG scans; export the scan as JPEG or as a PDF from a phone scanner app")
	ErrTooManyPages        = fmt.Errorf("docs: too many pages: the vision model reads at most %d scanned pages per document (split it)", maxVisionPages)
)

// Transcript is the provenance of transcribed text: the model, each page's place in the text (byte
// offsets) and its readings' agreement, the least agreement, and the calls' usage (cost nil:
// unpriced, unknown, never $0).
type Transcript struct {
	Model         string     `json:"model"`
	Pages         []PageRead `json:"pages"`
	Agreement     float64    `json:"agreement"`
	LowConfidence bool       `json:"lowConfidence"`
	TranscribedAt time.Time  `json:"transcribedAt"`
	Tokens        int        `json:"tokens"`
	CostUSD       *float64   `json:"costUsd"`
}

type PageRead struct {
	Page      int     `json:"page"`
	Start     int     `json:"start"`
	End       int     `json:"end"`
	Agreement float64 `json:"agreement"`
}

// UseVision makes c the vision role's model modelID (a text role is never used for images).
func (s *Service) UseVision(c model.Chatter, modelID string) { s.vision, s.visionModel = c, modelID }

// sniffImage is an image file's type from its magic bytes ("" if none BigLaw knows).
func sniffImage(data []byte) string {
	for _, m := range [][2]string{{"\x89PNG\r\n\x1a\n", "image/png"}, {"\xff\xd8\xff", "image/jpeg"}, {"GIF87a", "image/gif"}, {"GIF89a", "image/gif"},
		{"II*\x00", "image/tiff"}, {"MM\x00*", "image/tiff"}} {
		if bytes.HasPrefix(data, []byte(m[0])) {
			return m[1]
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
}

// imageOf is an image file as one page to transcribe (a TIFF cannot be sent: no decoder or model takes it).
func imageOf(data []byte, mime string) (Extraction, error) {
	if mime == "image/tiff" {
		return Extraction{}, fmt.Errorf("%w (this is %s)", ErrScanEncoding, mime)
	}
	return Extraction{Method: "vision", ContentType: mime, NoText: true, Pages: 1, ScanPages: 1, Blank: []int{1}, Scans: []Scan{{1, [][2]int{{0, len(data)}}}}}, nil
}

// readScans transcribes x's scans (data is the file) into x. With no text layer the transcript is
// the document: an unsupported page is 422, over maxVisionPages 413, a page whose readings disagree
// 422 ErrUnreliable, nothing readable 422 ErrNoContent, no vision model ErrVisionNotConfigured. A
// mixed PDF keeps its text layer verbatim: only its text-less pages are read, and one that cannot be
// is noted, never guessed.
func (s *Service) readScans(ctx context.Context, data []byte, x *Extraction) (err error) {
	only, pages, mime := strings.TrimSpace(x.Text) == "", strings.Split(x.Text, "\f"), "image/jpeg"
	if only {
		pages = make([]string, x.Pages)
	}
	if x.Method == "vision" {
		mime = x.ContentType
	}
	switch {
	case len(x.Scans) == 0 && (!only || len(x.Unsupported) == 0):
		return nil
	case only && len(x.Unsupported) > 0:
		return fmt.Errorf("%w (page %s)", ErrScanEncoding, pageList(x.Unsupported))
	case only && x.ScanPages > maxVisionPages:
		return fmt.Errorf("%w: it has %d", ErrTooManyPages, x.ScanPages)
	}
	for _, sc := range x.Scans {
		for i, total := 0, 0; i < len(sc.Images); i++ { // the child's answer is checked too
			im := sc.Images[i]
			if total += im[1]; sc.Page < 1 || sc.Page > x.Pages || im[0] < 0 || im[1] < 1 || total > maxImageBytes || im[0] > len(data)-im[1] {
				return fmt.Errorf("%w: page %d's images are over %d MiB in all (or the file reader misplaced them)", ErrUnreadable, sc.Page, maxImageBytes>>20)
			} else if _, _, err := image.DecodeConfig(bytes.NewReader(data[im[0] : im[0]+im[1]])); err != nil && mime != "image/webp" {
				return fmt.Errorf("%w: the image on page %d is damaged", ErrUnreadable, sc.Page)
			}
		}
	}
	if s.vision == nil && only {
		x.Notes, x.Method = []string{pendingNote}, "vision"
		return ErrVisionNotConfigured
	} else if s.vision == nil || len(pages) != x.Pages { // no model, or a form feed inside a page: pages cannot be placed
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, visionWait)
	defer cancel()
	t := &Transcript{Model: s.visionModel, Agreement: 1, TranscribedAt: time.Now().UTC(), CostUSD: new(float64)}
	defer func() {
		if err != nil { // the calls were priced as any call; the document is not stored, so say what they used
			err = fmt.Errorf("%w (the vision calls used %d tokens)", err, t.Tokens)
		}
	}()
	agree, skipped, read, size := map[int]float64{}, []int{}, []int{}, len(x.Text)
	for _, sc := range x.Scans[:min(len(x.Scans), maxVisionPages)] {
		text, a, err := s.readPage(ctx, data, mime, sc, t)
		switch size += len(text); {
		case err != nil:
			return err
		case size > maxText: // a runaway endpoint: stop before holding more
			return textTooLarge()
		case text != "" && a < unreliableBelow && only:
			return fmt.Errorf("%w: the two readings of page %d agree on %.0f%% of their words (%.0f%% is needed); nothing was stored as text",
				ErrUnreliable, sc.Page, 100*a, 100*unreliableBelow)
		case text != "" && a < unreliableBelow:
			skipped = append(skipped, sc.Page)
		case text != "":
			pages[sc.Page-1], agree[sc.Page], read = clean(text), a, append(read, sc.Page)
		}
	}
	if len(read) == 0 && only {
		return ErrNoContent
	}
	var b strings.Builder
	x.Blank = x.Blank[:0]
	for i, p := range pages {
		if i > 0 {
			b.WriteByte('\f')
		}
		if a, ok := agree[i+1]; ok {
			t.Pages = append(t.Pages, PageRead{i + 1, b.Len(), b.Len() + len(p), a})
			t.Agreement, t.LowConfidence = min(t.Agreement, a), t.LowConfidence || a < lowBelow
		} else if strings.TrimSpace(p) == "" {
			x.Blank = append(x.Blank, i+1)
		}
		b.WriteString(p)
	}
	x.Text, x.NoText, x.Notes = b.String(), false, pageNotes(x)
	if only {
		x.Method = "vision"
	}
	if len(read) > 0 {
		x.Transcript, x.Notes = t, append(x.Notes, fmt.Sprintf("Page %s: machine transcription of an image by %s (its two readings agreed on at least %.0f%% of their words): check quotes against the original.",
			pageList(read), t.Model, 100*t.Agreement))
	}
	if len(skipped) > 0 {
		x.Notes = append(x.Notes, fmt.Sprintf("Page %s: the vision model's two readings disagreed, so it was not transcribed.", pageList(skipped)))
	}
	return nil
}

// covers is the transcribed page (the least agreeing, if several) that bytes [start, end) overlap.
func (t *Transcript) covers(start, end int) (p *PageRead) {
	for i := 0; t != nil && i < len(t.Pages); i++ {
		if q := &t.Pages[i]; q.Start < end && start < q.End && (p == nil || q.Agreement < p.Agreement) {
			p = q
		}
	}
	return p
}

// readPage reads one page with both prompts and returns the reading to keep ("" for a blank page)
// and the readings' agreement. The reading with more [illegible] marks is kept, so a mark is never
// filled in from the other; a blank reading beside a non-blank one is a disagreement.
func (s *Service) readPage(ctx context.Context, data []byte, mime string, sc Scan, t *Transcript) (string, float64, error) {
	parts := []model.Part{model.TextPart(fmt.Sprintf("Page %d:", sc.Page))}
	for _, im := range sc.Images {
		parts = append(parts, model.ImagePart("data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data[im[0]:im[0]+im[1]])))
	}
	var reads [2]string
	for i := range reads {
		resp, err := s.vision.Chat(ctx, model.Request{Agent: fmt.Sprintf("transcribe-%c", 'a'+i), Role: model.Vision, Model: s.visionModel, MaxTokens: 8192,
			Messages: []model.Message{{Role: "system", Content: readPrompts[i]}, {Role: "user", Content: parts}}})
		var me *model.Error
		u := model.Usage{}
		if resp != nil {
			u = resp.Usage
		} else if errors.As(err, &me) && me.Usage != nil { // a refused answer may be billed too
			u = *me.Usage
		}
		if t.Tokens += u.InputTokens + u.OutputTokens; t.CostUSD != nil && u.CostUSD != nil {
			*t.CostUSD += *u.CostUSD
		} else if resp != nil || me != nil && me.Usage != nil {
			t.CostUSD = nil
		}
		switch {
		case errors.Is(err, model.ErrNoContent): // an empty answer is a blank reading
		case err != nil:
			return "", 0, err
		case resp.Truncated:
			return "", 0, fmt.Errorf("%w: the reading of page %d stopped at the model's output limit", ErrUnreliable, sc.Page)
		case len(words(resp.Text)) > 0: // else a blank reading: no words but [blank] and [illegible]
			reads[i] = strings.TrimSpace(resp.Text)
		}
	}
	keep, marks := reads[0], func(s string) int { return strings.Count(strings.ToLower(s), "[illegible]") }
	if marks(reads[1]) > marks(keep) || keep == "" {
		keep = reads[1]
	}
	return keep, agreement(words(reads[0]), words(reads[1])), nil
}

// words are a reading's words, case folded, without the [blank] and [illegible] marks.
func words(s string) (w []string) {
	for _, x := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if x != "blank" && x != "illegible" {
			w = append(w, x)
		}
	}
	return w
}

// agreement is the share of words two readings have in common (Dice over word multisets; order is
// not compared): 1 for the same words, 0 for none in common.
func agreement(wa, wb []string) float64 {
	left, common := map[string]int{}, 0
	for _, w := range wa {
		left[w]++
	}
	for _, w := range wb {
		if left[w] > 0 {
			left[w], common = left[w]-1, common+1
		}
	}
	return 2 * float64(common) / float64(max(1, len(wa)+len(wb)))
}
