// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // image.DecodeConfig checks an image's header before it is stored or sent
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/model"
)

// The vision read (README § Vision). Each page is read twice, independently (two prompts), and the
// readings' words compared: below unreliableBelow nothing is stored as text; below lowBelow the
// page is low confidence. A citation into transcribed text is capped at TranscriptCap.
const (
	unreliableBelow, lowBelow, TranscriptCap = 0.75, 0.9, 0.85
	pendingNote                              = "Waiting for transcription: this is an image or a scan and no vision model is configured. The original is kept."
	readTokens, maxCompare                   = 4096, 2000         // a reading's output limit; the words compare aligns
	maxSide, maxPixels                       = 12_000, 50_000_000 // an image's size as its header declares it
)

var (
	visionWait = 10 * time.Minute // all the vision calls of one document
	//go:embed vision_prompts.txt
	prompts                string
	readPrompts            = strings.Split(strings.TrimSpace(prompts), "\n") // two independent readings
	markRe, tokenRe        = regexp.MustCompile(`(?i)\[(illegible|blank)[^\]\n]{0,200}\]`), regexp.MustCompile(`(?i)\[(?:illegible|blank)\]|[\p{L}\p{N}]+`)
	ErrVisionNotConfigured = errors.New("docs: no vision model is configured (set vision=<model id> in BIGLAW_MODELS): the original is stored with status needs_transcription and is transcribed once one is")
	ErrUnreliable          = errors.New("docs: transcription unreliable")
	ErrNoContent           = errors.New("docs: no content: the vision model found no readable text")
	ErrScanEncoding        = errors.New("docs: scan encoding unsupported (or the scan's image data is damaged): BigLaw sends PNG, JPEG, GIF, WebP and PDFs whose pages are JPEG scans; export the scan as JPEG or as a PDF from a phone scanner app")
	ErrTooManyPages        = fmt.Errorf("docs: too many pages: the vision model reads at most %d scanned pages per document (split it)", maxVisionPages)
)

// Transcript is the provenance of transcribed text: the model, each page's byte span, agreement and
// disputes, the least agreement, and the calls' usage (cost nil: unpriced, unknown, never $0).
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
	Page      int      `json:"page"`
	Start     int      `json:"start"`
	End       int      `json:"end"`
	Agreement float64  `json:"agreement"`
	Disputed  []string `json:"disputed,omitempty"` // compare's: what the readings differ on that matters
}

// low: the page's readings agreed below lowBelow, or differ on something that matters (compare).
func (p *PageRead) low() bool { return p.Agreement < lowBelow || len(p.Disputed) > 0 }

// UseVision makes c the vision role's model modelID (never a text role's); key is never stored, even
// echoed in a reading; log gets the cost of refused readings.
func (s *Service) UseVision(c model.Chatter, modelID, key string, log *slog.Logger) {
	s.vision, s.visionModel, s.visionKey, s.log = c, modelID, key, cmp.Or(log, s.log)
}

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

// readScans transcribes x's scans (data is the file) into x. With no text layer but stamps the
// transcript is the document: an unsupported page is 422, over maxVisionPages 413, an unreadable
// page readPage's error, nothing readable ErrNoContent, no vision model ErrVisionNotConfigured. A
// mixed PDF keeps its text layer verbatim; a scanned page there that cannot be read is noted.
func (s *Service) readScans(ctx context.Context, data []byte, x *Extraction) (err error) {
	only, pages, mime := len(x.Blank) == x.Pages, strings.Split(x.Text, "\f"), strings.Replace(x.ContentType, "application/pdf", "image/jpeg", 1)
	switch {
	case only && len(x.Unsupported) > 0 && strings.TrimSpace(x.Text) == "": // with stamps, as with a text layer, such a page is noted
		return fmt.Errorf("%w (page %s)", ErrScanEncoding, pageList(x.Unsupported))
	case len(x.Scans) == 0:
		return nil
	case only && x.ScanPages > maxVisionPages:
		return fmt.Errorf("%w: it has %d", ErrTooManyPages, x.ScanPages)
	}
	for k, sc := range x.Scans { // the child's answer is checked: pages in order, without a text layer, images in the file
		for i, total := 0, 0; i < len(sc.Images); i++ {
			im := sc.Images[i]
			if total += im[1]; sc.Page < 1 || sc.Page > x.Pages || k > 0 && sc.Page <= x.Scans[k-1].Page || len(pages) == x.Pages && len(strings.TrimSpace(pages[sc.Page-1])) >= stampMax ||
				im[0] < 0 || im[1] < 1 || total > maxImageBytes || im[0] > len(data)-im[1] {
				return fmt.Errorf("%w: page %d's images are over %d MiB in all (or the file reader misplaced them)", ErrUnreadable, sc.Page, maxImageBytes>>20)
			} else if c, _, err := image.DecodeConfig(bytes.NewReader(data[im[0] : im[0]+im[1]])); err != nil && mime != "image/webp" || max(c.Width, c.Height) > maxSide || c.Width*c.Height > maxPixels {
				return fmt.Errorf("%w: the image on page %d is damaged, or over %d pixels a side or %d million in all (%d×%d: a decompression bomb for the model's server)", ErrUnreadable, sc.Page, maxSide, maxPixels/1_000_000, c.Width, c.Height)
			}
		}
	}
	if only {
		x.Method = "vision"
	}
	if s.vision == nil && only {
		x.Notes = []string{pendingNote}
		return ErrVisionNotConfigured
	} else if s.vision == nil || len(pages) != x.Pages { // no model (README: upload it again once one is set), or a form feed inside a page
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, visionWait)
	defer cancel()
	t := &Transcript{Model: s.visionModel, Agreement: 1, TranscribedAt: time.Now().UTC(), CostUSD: new(float64)}
	defer func() {
		if cost, _ := json.Marshal(t.CostUSD); err != nil { // the calls were priced as any call, and nothing records them: the operator's log does
			s.log.Warn("vision transcription refused or failed after its calls", "tokens", t.Tokens, "costUsd", string(cost), "err", err)
		}
	}()
	read, notes, size := map[int]PageRead{}, []string{}, len(x.Text)
	for _, sc := range x.Scans[:min(len(x.Scans), maxVisionPages)] {
		text, p, err := s.readPage(wait, data, mime, sc, t)
		switch size += len(text); {
		case err != nil && (only || ctx.Err() != nil):
			return err
		case err != nil: // like a disagreement: the text layer is stored, the page noted
			notes = append(notes, fmt.Sprintf("Page %d was not transcribed: %s.", sc.Page, strings.TrimPrefix(err.Error(), "docs: ")))
		case size > maxText: // a runaway endpoint: stop before holding more
			return textTooLarge()
		case text != "": // a stamp in the text layer is kept after the transcript
			p.End = len(text)
			pages[sc.Page-1], read[sc.Page] = strings.TrimSuffix(text+"\n"+strings.TrimSpace(pages[sc.Page-1]), "\n"), p
			if len(p.Disputed) > 0 {
				notes = append(notes, fmt.Sprintf("Page %d is low confidence: its two readings differ on %q; check it against the original.", sc.Page, p.Disputed))
			}
		}
	}
	if len(read) == 0 && only {
		return ErrNoContent
	}
	for i, at := 0, 0; i < len(pages); at, i = at+len(pages[i])+1, i+1 { // each page's place in the text joined by form feeds
		if r, ok := read[i+1]; ok {
			r.Page, r.Start, r.End = i+1, at, at+r.End
			t.Pages, t.Agreement, t.LowConfidence = append(t.Pages, r), min(t.Agreement, r.Agreement), t.LowConfidence || r.low()
		}
	}
	x.Blank = slices.DeleteFunc(x.Blank, func(p int) bool { _, ok := read[p]; return ok })
	x.Text, x.NoText, x.Transcript, x.Notes = strings.Join(pages, "\f"), false, t, pageNotes(x) // the usage is kept even when no page was read
	if len(read) > 0 {
		x.Notes = append(x.Notes, fmt.Sprintf("Page %s: machine transcription of an image by %s (its two readings agreed on at least %d%% of their words): check quotes against the original.",
			pageList(slices.Sorted(maps.Keys(read))), t.Model, int(100*t.Agreement)))
	}
	x.Notes = append(x.Notes, notes...)
	return nil
}

// covers is the transcribed page that bytes [start, end) overlap (a low one, else the least agreeing, if several).
func (t *Transcript) covers(start, end int) (p *PageRead) {
	for i := 0; t != nil && i < len(t.Pages); i++ {
		if q := &t.Pages[i]; q.Start < end && start < q.End && (p == nil || q.low() || !p.low() && q.Agreement < p.Agreement) {
			p = q
		}
	}
	return p
}

// readPage reads one page with both prompts and returns the reading to keep (cleaned, masked; "" if
// blank) and compare's verdict. Readings agreeing below unreliableBelow, or a blank one beside text,
// are ErrUnreliable; an image the endpoint refuses (HTTP 4xx) is ErrUnreadable.
func (s *Service) readPage(ctx context.Context, data []byte, mime string, sc Scan, t *Transcript) (string, PageRead, error) {
	parts := []model.Part{model.TextPart(fmt.Sprintf("Page %d:", sc.Page))}
	for _, im := range sc.Images {
		parts = append(parts, model.ImagePart("data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data[im[0]:im[0]+im[1]])))
	}
	var reads []string // the non-blank readings
	for i := range 2 {
		resp, err := s.vision.Chat(ctx, model.Request{Agent: fmt.Sprintf("transcribe-%c", 'a'+i), Role: model.Vision, Model: s.visionModel, MaxTokens: readTokens,
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
		case me != nil && me.Status/100 == 4 && me.Status != 408: // damaged, animated, too large for this model: not an outage
			return "", PageRead{}, fmt.Errorf("%w: the vision model refused the image on page %d (HTTP %d)", ErrUnreadable, sc.Page, me.Status)
		case err != nil:
			return "", PageRead{}, err
		case resp.Truncated || len(resp.Text) > 16*readTokens:
			return "", PageRead{}, fmt.Errorf("%w: the reading of page %d stopped at, or ran past, the model's output limit", ErrUnreliable, sc.Page)
		case wordRe.MatchString(markRe.ReplaceAllString(resp.Text, "")): // else a blank reading: no words but [blank] and [illegible] marks; a guess in a mark is dropped
			reads = append(reads, markRe.ReplaceAllString(strings.TrimSpace(resp.Text), "[$1]"))
			if s.visionKey != "" { // an endpoint echoing the request
				reads[len(reads)-1] = strings.ReplaceAll(reads[len(reads)-1], s.visionKey, "[redacted]")
			}
		}
	}
	reads = append(reads, "", "") // the first non-blank reading is kept
	ta, at := tokensOf(reads[0])
	tb, _ := tokensOf(reads[1])
	var p PageRead
	mask := compare(ta, tb, &p)
	if reads[0] != "" && p.Agreement < unreliableBelow {
		return "", PageRead{}, fmt.Errorf("%w: the two readings of page %d agree on %d%% of their words (%.0f%% is needed)", ErrUnreliable, sc.Page, int(100*p.Agreement), 100*unreliableBelow)
	}
	var b strings.Builder
	last := 0
	for _, i := range mask {
		b.WriteString(reads[0][last:at[i][0]] + "[illegible]")
		last = at[i][1]
	}
	return clean(b.String() + reads[0][last:]), p, nil
}

// tokensOf are a reading's words, case folded, and its [illegible]/[blank] marks, with their places.
func tokensOf(s string) (w []string, at [][]int) {
	at = tokenRe.FindAllStringIndex(s, -1)
	for _, p := range at {
		w = append(w, strings.ToLower(s[p[0]:p[1]]))
	}
	return w, at
}

// diff is a's places whose token b lacks (multisets).
func diff(a, b []string) (only []int) {
	left := map[string]int{}
	for _, x := range b {
		left[x]++
	}
	for i, x := range a {
		if left[x]--; left[x] < 0 {
			only = append(only, i)
		}
	}
	return only
}

// compare sets p.Agreement (Dice over the readings' word multisets: 1 the same words, 0 none in
// common) and p.Disputed, what makes the page low confidence however high its agreement: a mark in
// either reading; a figure, negation or modal one has and the other lacks (every word one lacks, if
// 5 or more: a dropped line); the same words in another order (swapped parties: a word LCS, at most
// maxCompare² steps). If b has a mark it returns a's places b lacks, to mask: a mark is never filled.
func compare(a, b []string, p *PageRead) []int {
	wa, wb := slices.DeleteFunc(slices.Clone(a), isMark), slices.DeleteFunc(slices.Clone(b), isMark) // agreement counts words, not marks
	p.Agreement = 2 * float64(len(wa)-len(diff(wa, wb))) / float64(max(1, len(wa)+len(wb)))
	only := [2][]int{diff(a, b), diff(b, a)}
	for k, s := range [2][]string{a, b} {
		for _, i := range only[k] {
			if material(s[i]) || max(len(only[0]), len(only[1])) >= 5 {
				p.Disputed = append(p.Disputed, s[i])
			}
		}
	}
	if slices.ContainsFunc(a, isMark) || slices.ContainsFunc(b, isMark) {
		p.Disputed = append(p.Disputed, "[illegible]")
	}
	if len(a)*len(b) > maxCompare*maxCompare || lcs(a, b) < len(a)-len(only[0]) {
		p.Disputed = append(p.Disputed, "(the order of its words: it differs, or the page is too long to compare)")
	}
	slices.Sort(p.Disputed) // the order flag, figures and marks sort first: at most 12 are kept
	if p.Disputed = slices.Compact(p.Disputed); len(p.Disputed) > 12 {
		p.Disputed = p.Disputed[:12]
	}
	if slices.ContainsFunc(b, isMark) {
		return only[0]
	}
	return nil
}

// lcs is the length of a and b's longest common subsequence (two rows of the table).
func lcs(a, b []string) int {
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for _, x := range a {
		for j, y := range b {
			if cur[j+1] = max(prev[j+1], cur[j]); x == y {
				cur[j+1] = prev[j] + 1
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func isMark(x string) bool { return x[0] == '[' }

// material is a token whose difference changes what a page says: a mark, a figure, a negation or modal.
func material(x string) bool {
	return isMark(x) || strings.ContainsFunc(x, unicode.IsNumber) || strings.Contains(" not no nor never t cannot shall may must will ", " "+x+" ")
}
