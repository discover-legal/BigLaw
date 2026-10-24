// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

// lab-r4 r4 verifier repros (critique/biglaw-lab-r4-r4-verifier.md), redone under the owner's
// decision that number meaning, whose figure it is included, is the figure reader's (Value.Fig).

func fnd(fid, doc, quote string, ents []Entity, vals ...Value) Finding {
	return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Entities: ents, Values: vals}
}
