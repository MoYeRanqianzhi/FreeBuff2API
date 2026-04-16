"""
Latency probing for endpoints that work regardless of upstream free_mode block.
Also measures the end-to-end latency of the 403 round-trip (proxy overhead + upstream).
"""
import json, time, urllib.request, urllib.error, statistics

BASE = "http://localhost:8787"

def timed_get(path, n=10):
    xs = []
    last_body = ""
    for _ in range(n):
        t0 = time.time()
        try:
            with urllib.request.urlopen(BASE + path, timeout=30) as r:
                last_body = r.read().decode("utf-8", errors="replace")
        except urllib.error.HTTPError as e:
            last_body = e.read().decode("utf-8", errors="replace")
        xs.append((time.time() - t0) * 1000.0)
    return xs, last_body[:200]

def timed_post(path, payload, n=5):
    xs = []
    code = None
    last_body = ""
    data = json.dumps(payload).encode("utf-8")
    for _ in range(n):
        t0 = time.time()
        req = urllib.request.Request(BASE + path, data=data,
            headers={"Content-Type":"application/json"}, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                code = r.status
                last_body = r.read().decode("utf-8", errors="replace")
        except urllib.error.HTTPError as e:
            code = e.code
            last_body = e.read().decode("utf-8", errors="replace")
        xs.append((time.time() - t0) * 1000.0)
    return xs, code, last_body[:300]

def summarize(xs):
    return {
        "n": len(xs),
        "min_ms": round(min(xs), 2),
        "max_ms": round(max(xs), 2),
        "mean_ms": round(statistics.mean(xs), 2),
        "median_ms": round(statistics.median(xs), 2),
        "stdev_ms": round(statistics.stdev(xs), 2) if len(xs) > 1 else 0.0,
    }

def main():
    out = {}
    xs, body = timed_get("/health", 20)
    out["health"] = {"stats": summarize(xs), "body": body}

    xs, body = timed_get("/v1/models", 10)
    out["models"] = {"stats": summarize(xs), "body_preview": body}

    # Roundtrip latency on the blocked chat endpoint (proxy + upstream 403)
    payload = {"model":"google/gemini-2.5-flash",
               "messages":[{"role":"user","content":"hi"}],
               "stream": False, "max_tokens": 50}
    xs, code, body = timed_post("/v1/chat/completions", payload, 5)
    out["chat_nonstream_blocked"] = {"status": code, "stats": summarize(xs), "body": body}

    payload["stream"] = True
    xs, code, body = timed_post("/v1/chat/completions", payload, 5)
    out["chat_stream_blocked"] = {"status": code, "stats": summarize(xs), "body": body}

    print(json.dumps(out, ensure_ascii=False, indent=2))
    with open("tests/latency.json","w",encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)

if __name__ == "__main__":
    main()
