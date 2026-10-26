// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

// fnd is a verified finding with one quote in doc (the join repros of lab-r3 r2 and lab-r4 r4).
func fnd(fid, doc, quote string, ents []Entity, vals ...Value) Finding {
	return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Entities: ents, Values: vals}
}
