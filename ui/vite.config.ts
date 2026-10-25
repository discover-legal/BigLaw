// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// VITE_ENGINE=recorded swaps the transport for the recorded engine (no network code in the bundle);
// UI_BASE sets the base path (default "/" live, "./" recorded, e.g. /demo/biglaw/).
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';

const recorded = process.env.VITE_ENGINE === 'recorded';

export default defineConfig({
  base: process.env.UI_BASE || (recorded ? './' : '/'),
  oxc: { jsx: { runtime: 'automatic' } },
  resolve: {
    alias: recorded
      ? [{ find: /^\.\/api\/engine\.ts$/, replacement: fileURLToPath(new URL('./src/api/recorded.ts', import.meta.url)) }]
      : [],
  },
  build: { modulePreload: false, sourcemap: false, chunkSizeWarningLimit: 1200 },
  server: { proxy: Object.fromEntries(['/health', '/documents', '/tasks', '/clients', '/playbooks', '/time-entries', '/profiles', '/me', '/auth', '/manage']
    .map((p) => [p, process.env.UI_API || 'http://127.0.0.1:3101'])) },
});
