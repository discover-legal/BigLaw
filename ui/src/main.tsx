// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createTransport } from './api/engine.ts';
import { App } from './App.tsx';
import './styles.css';

createRoot(document.getElementById('root')!).render(<StrictMode><App api={createTransport()} /></StrictMode>);
