"""Scripted stand-in for a model server, for credential-free plumbing smokes.

Serves the two wire protocols a BigLaw backend speaks when pointed at local
inference — OpenAI-compatible ``/v1/chat/completions`` (LOCAL_INFERENCE_URL)
and Ollama ``/api/embeddings`` (LOCAL_EMBEDDINGS=true, OLLAMA_URL) — and
answers from the proof-run answer keys:

* a findings request (the agent prompt that asks for FINDING blocks) gets one
  FINDING per planted fact of the matter whose instructions appear in the
  prompt, each citing the fact's evidence verbatim with the source document's
  filename — so the real citation gate, debate and verification run on known
  inputs;
* any request that mentions the matter's deliverable filenames (synthesis /
  writer calls) gets the oracle deliverable text for that matter;
* everything else gets a short neutral reply.

Embeddings are deterministic hashed bag-of-words vectors (so semantic search
and Need/Offer matching are exercised, though not meaningfully ranked).

This is NOT a model and its outputs are NOT results: a smoke proves that
fixtures ingest, tasks submit and complete, gates resolve, deliverables render
and the scorer reads the run dir. Nothing more.
"""

from __future__ import annotations

import hashlib
import json
import math
import re
import threading
from collections import Counter
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from .matter import Matter
from .patterns import normalize_quote

_WORD = re.compile(r"[a-z0-9]+")


def hashed_embedding(text: str, dim: int) -> list[float]:
    v = [0.0] * dim
    for w in _WORD.findall(text.lower()):
        h = int.from_bytes(hashlib.blake2b(w.encode(), digest_size=8).digest(), "little")
        v[h % dim] += 1.0 if (h >> 32) & 1 else -1.0
    n = math.sqrt(sum(x * x for x in v)) or 1.0
    return [x / n for x in v]


def _doc_for(m: Matter, docs: list[str], quote: str) -> str:
    q = normalize_quote(quote)
    return next((d for d in docs if q in normalize_quote(m.documents.get(d, ""))), docs[0])


class Script:
    def __init__(self, matters: list[Matter]):
        self.matters = matters
        self.calls: Counter = Counter()
        self.cursor: dict[str, int] = {}
        self.lock = threading.Lock()

    def _matter_in(self, text: str) -> Matter | None:
        low = text.lower()
        for m in self.matters:
            probe = str(m.task.get("instructions", ""))[:120].lower()
            if probe and probe in low:
                return m
        for m in self.matters:  # fall back to a deliverable filename mention
            if any(d.lower() in low for d in m.task.get("deliverables", [])):
                return m
        return None

    def _items(self, m: Matter) -> list[tuple[str, str, str]]:
        """(conclusion, source doc, verbatim quote) for every planted item."""
        items = []
        for f in m.key.get("facts", []):
            q = f["evidence"][0]
            items.append((f["model_answer"], _doc_for(m, f["documents"], q), q))
        for d in m.key.get("dispositions", []):
            q = d["evidence"][0]
            items.append((f"{d['id']} ({d['title']}): {d['expected']}. {d.get('model_answer', '')}",
                          _doc_for(m, d["documents"], q), q))
        return items

    def findings(self, m: Matter) -> str:
        # The engine keeps at most 3 findings per agent per round, so rotate
        # through the planted items across calls to get all of them recorded.
        # Conclusion is emitted BEFORE Evidence: with the prompt's prescribed
        # Evidence -> Conclusion order the engine's strict citation regex
        # swallows the Conclusion line into the QUOTE (see README "Known issues").
        items = self._items(m)
        if not items:
            return "NO_FINDINGS"
        with self.lock:
            start = self.cursor.get(m.id, 0)
            self.cursor[m.id] = (start + 3) % len(items)
        picked = [items[(start + i) % len(items)] for i in range(min(3, len(items)))]
        return "\n\n".join(
            "FINDING:\n"
            f"Conclusion: {concl}\n"
            f"Evidence: SOURCE={doc} | QUOTE={' '.join(quote.split())}\n"
            "Confidence: 0.85\nEND_FINDING"
            for concl, doc, quote in picked)

    def deliverable(self, m: Matter) -> str:
        names = m.task.get("deliverables") or ["deliverable.md"]
        body = ["## Findings", ""]
        for f in m.key.get("facts", []):
            body += [f"- {f['model_answer']}", ""]
        if m.key.get("dispositions"):
            body += ["| Change | Clause | Disposition | Rationale |", "|---|---|---|---|"]
            body += [f"| {d['id']} | {d['title']} | {d['expected']} | {d.get('model_answer', '')} |"
                     for d in m.key["dispositions"]]
        text = "\n".join(body)
        if len(names) == 1:
            return text
        return "\n\n".join(f"=== DELIVERABLE: {n} ===\n{text}" for n in names)

    def reply(self, messages: list[dict]) -> str:
        text = "\n".join(
            c if isinstance(c, str) else " ".join(p.get("text", "") for p in c if isinstance(p, dict))
            for c in (msg.get("content") or "" for msg in messages))
        m = self._matter_in(text)
        if m and "END_FINDING" in text and "NO_FINDINGS" in text:
            kind, out = "findings", self.findings(m)
        elif m and any(d.lower() in text.lower() for d in m.task.get("deliverables", [])):
            kind, out = "deliverable", self.deliverable(m)
        else:
            kind, out = "other", "NO_FINDINGS"
        with self.lock:
            self.calls[kind] += 1
        return out


def make_handler(script: Script, dim: int):
    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):  # quiet
            pass

        def _json(self, code: int, obj) -> None:
            body = json.dumps(obj).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path.rstrip("/") in ("/api/tags", "/v1/models", "/models"):
                self._json(200, {"data": [{"id": "proofrun-mock"}], "models": [{"name": "proofrun-mock"}]})
            else:
                self._json(200, {"status": "ok"})

        def do_POST(self):
            n = int(self.headers.get("Content-Length") or 0)
            try:
                req = json.loads(self.rfile.read(n) or b"{}")
            except json.JSONDecodeError:
                self._json(400, {"error": "bad json"})
                return
            if self.path.endswith("/api/embeddings") or self.path.endswith("/api/embed"):
                with script.lock:
                    script.calls["embeddings"] += 1
                prompt = req.get("prompt") or req.get("input") or ""
                self._json(200, {"embedding": hashed_embedding(str(prompt), dim)})
                return
            if self.path.endswith("/embeddings"):
                inputs = req.get("input") or []
                inputs = [inputs] if isinstance(inputs, str) else inputs
                with script.lock:
                    script.calls["embeddings"] += len(inputs)
                self._json(200, {"data": [{"index": i, "embedding": hashed_embedding(t, dim)}
                                          for i, t in enumerate(inputs)]})
                return
            if self.path.endswith("/chat/completions"):
                content = script.reply(req.get("messages") or [])
                self._json(200, {
                    "id": "mock", "object": "chat.completion", "model": req.get("model", "proofrun-mock"),
                    "choices": [{"index": 0, "finish_reason": "stop",
                                 "message": {"role": "assistant", "content": content}}],
                    "usage": {"prompt_tokens": 1, "completion_tokens": max(1, len(content) // 4)},
                })
                return
            self._json(404, {"error": f"mock: no route {self.path}"})
    return H


def serve(matters: list[Matter], port: int, dim: int = 768) -> tuple[ThreadingHTTPServer, Script]:
    script = Script(matters)
    srv = ThreadingHTTPServer(("127.0.0.1", port), make_handler(script, dim))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, script
