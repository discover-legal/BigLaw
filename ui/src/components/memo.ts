// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The memo as Download and Copy hand it over: a recorded demo's memo carries the recording's
// provenance first, so a copy never passes for a live run's output.
export const memoExport = (text: string, provenance: string | null) => (provenance ? `> ${provenance}\n\n${text}` : text);
