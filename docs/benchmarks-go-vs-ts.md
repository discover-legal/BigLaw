# BigLaw backend benchmark — Go port vs TypeScript original

**Date:** 2026-06-10
**Hardware:** Intel Core Ultra 9 275HX, 64 GB RAM, Windows 11
**Load generator:** autocannon, 50 concurrent connections, 10 s measured runs after a 3 s warmup pass per endpoint
**Backends under test:**
- **TypeScript** (`src/`) — Node v25.6.0, run natively via tsx, `BIG_MICHAEL_MODE=backend`
- **Go** (`biglaw-go/`) — Go 1.25/gin, running **inside Docker Desktop** (Linux VM + port forwarding — the Go numbers carry virtualization overhead the TS numbers don't)

Both backends served identical route contracts with equivalent data (same agent
registry content, same template set). Both were idle apart from the benchmark load.

## Results

| Endpoint | Payload | TS req/s | Go req/s | Speedup | TS p50 | Go p50 | TS p99 | Go p99 |
|---|---|---:|---:|---:|---:|---:|---:|---:|
| `/health` | ~0.3 KB | 29,260 | 36,525 | **1.25×** | 1 ms | 1 ms | 5 ms | 4 ms |
| `/templates` | ~33 KB | 4,912 | 18,462 | **3.8×** | 10 ms | 2 ms | 16 ms | 5 ms |
| `/agents` | ~850 KB (199 agents) | 125 | 864 | **6.9×** | 389 ms | 53 ms | 678 ms | 123 ms |

Throughput on the heavy endpoint: **683 MB/s (Go) vs 103 MB/s (TS)**.

## Reading the results

- Tiny responses are network/syscall-bound — both runtimes saturate similarly,
  Go takes a modest 1.25× lead despite the Docker VM tax.
- The gap scales with serialization weight. The heavier the JSON, the harder
  Go's `encoding/json` + static structs win over V8: 3.8× at 33 KB,
  6.9× at 850 KB — with p50 latency dropping from 389 ms to 53 ms on the
  registry endpoint.
- The Go figures are **understated**: they include Docker Desktop's
  virtualization and port-forwarding overhead, while TS ran native.

## Why this matters

The Go port (`biglaw-go/`) targets Raspberry Pi / ARM64 SBCs with 4 GB RAM —
the entire firm stack (orchestrator, vector registry, REST API, conflict graph
client) in a single static binary. These numbers are from a desktop, but the
shape of the result (CPU-bound serialization dominating) is what makes
low-end-hardware deployment viable.

## Reproducing

```bash
# Terminal 1 — TS backend
npm run serve                       # :3101

# Terminal 2 — Go stack
cd biglaw-go && docker compose up -d --build   # :3102

# Terminal 3 — benchmark
npx autocannon -d 10 -c 50 http://localhost:3101/agents
npx autocannon -d 10 -c 50 http://localhost:3102/agents
```
