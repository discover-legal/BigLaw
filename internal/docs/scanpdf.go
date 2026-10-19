// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

const (
	maxVisionPages = 20       // scanned pages one document may send to the vision model (413 too_many_pages)
	maxPageImages  = 4        // images on one scanned page
	maxImageBytes  = 20 << 20 // a page's images, sent in one vision call
	stampMax       = 80       // characters of text layer beside page images that make a page a scan (a Bates number, a header; as v1)
	// mixedMax: under it (5 lines of 80), text over a page image is MIXED: the image is read and adds only what the text layer
	// lacks. A legend with its protective order and Bates number runs 55–266 characters; a page of text, ~3,700.
	mixedMax = 400
)

// Scan is a page to transcribe: its images as {offset, length} in the file, sent as they are.
// For a PDF they are /DCTDecode streams (JPEG, stored whole), found without decoding anything.
type Scan struct {
	Page   int
	Images [][2]int
}

var streamAt = regexp.MustCompile(`@(\d+)$`) // ledongthuc/pdf prints a stream as <<dict>>@offset

// pageScan reads the image XObjects of page n: JPEG streams stored whole make it a scan; another encoding (CCITT,
// JBIG2, Flate bitmaps), a form XObject, over maxPageImages or maxImageBytes, or an encrypted file, unsupported.
func pageScan(r *pdf.Reader, p pdf.Page, n int, data []byte) (sc Scan, unsupported bool) {
	xo, total := p.Resources().Key("XObject"), 0
	sc.Page = n
	for _, k := range xo.Keys() {
		v := xo.Key(k)
		f, m := v.Key("Filter"), streamAt.FindStringSubmatch(v.String())
		if f.Kind() == pdf.Array && f.Len() == 1 {
			f = f.Index(0)
		}
		if v.Key("Subtype").Name() != "Image" || f.Name() != "DCTDecode" || m == nil || r.Trailer().Key("Encrypt").Kind() != pdf.Null || len(sc.Images) == maxPageImages {
			return Scan{}, true
		}
		off, _ := strconv.Atoi(m[1])
		size := int(min(max(v.Key("Length").Int64(), 0), maxImageBytes+1))
		if total += size; size < 1 || total > maxImageBytes || off > len(data)-size || !bytes.HasPrefix(data[off:], []byte("\xff\xd8\xff")) {
			return Scan{}, true
		}
		sc.Images = append(sc.Images, [2]int{off, size})
	}
	return sc, false
}

// pageNotes says which pages have no text (scanned pages BigLaw did not read, or blank ones).
func pageNotes(x *Extraction) (notes []string) {
	switch n := len(x.Blank); {
	case n > 0 && n == x.Pages && strings.TrimSpace(x.Text) == "":
		notes = []string{fmt.Sprintf("This PDF has no text layer (%d scanned or blank page(s)): nothing in it can be searched or quoted. "+
			"Re-export it with text, or as a JPEG-scanned PDF for the vision model.", x.Pages)}
	case n > 0:
		notes = []string{fmt.Sprintf("%d of %d pages have no text layer, or only a stamp beside a page image, and were not read: page %s (%s).", n, x.Pages, pageList(x.Blank), map[bool]string{true: "no vision role is set: upload the file again once one is", false: "blank, or not transcribed: see the other notes"}[x.Transcript == nil])}
	}
	if len(x.Unsupported) > 0 {
		notes = append(notes, fmt.Sprintf("Page %s: its image is damaged, too large, or in an encoding BigLaw cannot send, and was not sent: export the page as a JPEG to have it read.", pageList(slices.Sorted(slices.Values(x.Unsupported)))))
	}
	if len(x.Imaged) > 0 { // a page image under a text layer not read: an OCR'd scan, a logo, letterhead (mixedMax or more, or no vision)
		notes = append(notes, fmt.Sprintf("Page %s: an image on the page was not read, only its text layer: if the page is a scan, its text may be missing: check it against the original.", pageList(x.Imaged)))
	}
	return notes
}

// pageList is "1, 2, 3", the first ten pages then "…".
func pageList(pages []int) string {
	var s []string
	for i, p := range pages {
		if i == 10 {
			s = append(s, "…")
			break
		}
		s = append(s, strconv.Itoa(p))
	}
	return strings.Join(s, ", ")
}
