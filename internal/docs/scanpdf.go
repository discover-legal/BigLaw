// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

const (
	maxVisionPages = 20       // scanned pages one document may send to the vision model (413 too_many_pages)
	maxPageImages  = 4        // images on one scanned page
	maxImageBytes  = 20 << 20 // a page's images, sent in one vision call
)

// Scan is a page to transcribe: its images as {offset, length} in the file, sent as they are.
// For a PDF they are /DCTDecode streams (JPEG, stored whole), found without decoding anything.
type Scan struct {
	Page   int
	Images [][2]int
}

var streamAt = regexp.MustCompile(`@(\d+)$`) // ledongthuc/pdf prints a stream as <<dict>>@offset

// pageScan reads the image XObjects of page n, which has no text layer: JPEG streams stored whole
// make it a scan; any other image encoding (CCITT, JBIG2, Flate bitmaps), a form XObject, more than
// maxPageImages or maxImageBytes, or an encrypted file make it unsupported; no XObject leaves it blank.
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
func pageNotes(x *Extraction) []string {
	switch n := len(x.Blank); {
	case n > 0 && n == x.Pages:
		return []string{fmt.Sprintf("This PDF has no text layer (%d scanned or blank page(s)): nothing in it can be searched or quoted. "+
			"Re-export it with text, or as a JPEG-scanned PDF for the vision model.", x.Pages)}
	case n > 0:
		return []string{fmt.Sprintf("%d of %d pages have no text layer (scanned images?) and were not read: page %s.", n, x.Pages, pageList(x.Blank))}
	}
	return nil
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
