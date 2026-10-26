// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

// The lab-r3 round-2 verifier's join repros (M2, M3), kept as regression tests; each failed on 7a5d6be.

func r2vF(fid, doc, quote string, v Value) Finding {
	return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Values: []Value{v}}
}
