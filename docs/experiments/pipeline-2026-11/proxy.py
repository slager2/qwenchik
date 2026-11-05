# Logging reverse proxy: 127.0.0.1:<listen> -> upstream. Writes JSONL per request.
import http.server, json, sys, time, urllib.request, threading
listen, upstream, logpath = int(sys.argv[1]), sys.argv[2], sys.argv[3]
lock = threading.Lock()
class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def _do(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n) if n else None
        req = urllib.request.Request(upstream + self.path, data=body, method=self.command)
        for k, v in self.headers.items():
            if k.lower() not in ("host", "content-length", "connection", "accept-encoding"):
                req.add_header(k, v)
        t0 = time.time()
        streaming = False
        try:
            streaming = bool(body) and json.loads(body).get("stream") is True
        except Exception:
            pass
        if streaming:
            resp = urllib.request.urlopen(req, timeout=900)
            self.send_response(resp.status)
            for k, v in resp.headers.items():
                if k.lower() not in ("transfer-encoding", "connection", "content-length"):
                    self.send_header(k, v)
            self.send_header("Transfer-Encoding", "chunked"); self.end_headers()
            chunks = []
            while True:
                line = resp.readline()
                if not line: break
                chunks.append(line)
                self.wfile.write(b"%x\r\n" % len(line) + line + b"\r\n"); self.wfile.flush()
            self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()
            data = b"".join(chunks); status = resp.status
            self._record(t0, body, data, status)
            return
        try:
            resp = urllib.request.urlopen(req, timeout=900); status = resp.status; data = resp.read(); hdrs = resp.headers
        except urllib.error.HTTPError as e:
            status = e.code; data = e.read(); hdrs = e.headers
        self._record(t0, body, data, status)
        self.send_response(status)
        for k, v in hdrs.items():
            if k.lower() not in ("transfer-encoding", "connection", "content-length"):
                self.send_header(k, v)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers(); self.wfile.write(data)
    def _record(self, t0, body, data, status):
        dt = time.time() - t0
        rec = {"t": t0, "path": self.path, "status": status, "sec": round(dt, 3), "req_bytes": len(body or b"")}
        if self.path.endswith("/chat/completions") and body:
            try:
                b = json.loads(body)
                if b.get("stream"):
                    r = {}; out = []
                    for line in data.decode("utf-8","replace").splitlines():
                        if line.startswith("data: ") and line[6:].strip() != "[DONE]":
                            ch = json.loads(line[6:])
                            if ch.get("timings"): r["timings"] = ch["timings"]
                            for c in ch.get("choices") or []:
                                out.append((c.get("delta") or {}).get("content") or "")
                    r["choices"] = [{"message": {"content": "".join(out)}}]
                else:
                    r = json.loads(data)
                rec["messages"] = [{"role": m.get("role"), "len": len(m.get("content") or ""), "head": (m.get("content") or "")[:160]} for m in b.get("messages", [])]
                rec["params"] = {k: b.get(k) for k in ("temperature","top_p","top_k","min_p","presence_penalty","max_tokens") if k in b}
                rec["schema"] = bool(b.get("response_format"))
                rec["timings"] = {k: r.get("timings", {}).get(k) for k in ("cache_n","prompt_n","prompt_ms","predicted_n","predicted_ms")}
                rec["full"] = b
                rec["output"] = (r.get("choices") or [{}])[0].get("message", {}).get("content")
            except Exception as e:
                rec["err"] = str(e)
        with lock:
            with open(logpath, "a") as f: f.write(json.dumps(rec, ensure_ascii=False) + "\n")
    do_GET = do_POST = _do
http.server.ThreadingHTTPServer(("127.0.0.1", listen), H).serve_forever()
