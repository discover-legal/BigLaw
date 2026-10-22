// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The build picks the engine: vite.config.ts aliases this file to recorded.ts when
// VITE_ENGINE=recorded, so a recorded bundle never contains the HTTP transport.
export { createTransport } from './http.ts';
