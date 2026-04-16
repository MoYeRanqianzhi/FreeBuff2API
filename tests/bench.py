"""
FreeBuff2API performance benchmark.
Tests streaming TTFT (time to first token), total time, and throughput.
"""
import json
import time
import sys
import urllib.request
import urllib.error

BASE_URL = "http://localhost:8787"


def bench_non_stream(model, prompt, max_tokens=200, label=""):
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "stream": False,
        "max_tokens": max_tokens,
    }
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        f"{BASE_URL}/v1/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            body = resp.read().decode("utf-8", errors="replace")
            total = time.time() - t0
            try:
                obj = json.loads(body)
                usage = obj.get("usage", {})
                content = ""
                choices = obj.get("choices", [])
                if choices:
                    content = choices[0].get("message", {}).get("content", "")
                return {
                    "ok": True,
                    "label": label,
                    "model": model,
                    "total_s": total,
                    "prompt_tokens": usage.get("prompt_tokens"),
                    "completion_tokens": usage.get("completion_tokens"),
                    "total_tokens": usage.get("total_tokens"),
                    "content_preview": content[:120],
                }
            except Exception as e:
                return {"ok": False, "label": label, "model": model, "error": f"json: {e}", "body": body[:500]}
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        return {"ok": False, "label": label, "model": model, "error": f"HTTP {e.code}", "body": body[:500]}
    except Exception as e:
        return {"ok": False, "label": label, "model": model, "error": str(e)}


def bench_stream(model, prompt, max_tokens=300, label=""):
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "stream": True,
        "max_tokens": max_tokens,
    }
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        f"{BASE_URL}/v1/chat/completions",
        data=data,
        headers={"Content-Type": "application/json", "Accept": "text/event-stream"},
        method="POST",
    )
    t0 = time.time()
    ttft = None
    chunks = 0
    content_chars = 0
    completion_tokens = None
    prompt_tokens = None
    first_chunk_time = None
    done = False
    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            for raw in resp:
                try:
                    line = raw.decode("utf-8", errors="replace").strip()
                except Exception:
                    continue
                if not line:
                    continue
                if first_chunk_time is None:
                    first_chunk_time = time.time() - t0
                if line == "data: [DONE]":
                    done = True
                    break
                if line.startswith("data:"):
                    chunks += 1
                    body = line[5:].strip()
                    if not body:
                        continue
                    try:
                        obj = json.loads(body)
                    except Exception:
                        continue
                    # capture usage if present
                    u = obj.get("usage")
                    if u:
                        prompt_tokens = u.get("prompt_tokens", prompt_tokens)
                        completion_tokens = u.get("completion_tokens", completion_tokens)
                    for ch in obj.get("choices", []):
                        delta = ch.get("delta") or {}
                        c = delta.get("content")
                        if c:
                            if ttft is None:
                                ttft = time.time() - t0
                            content_chars += len(c)
            total = time.time() - t0
            return {
                "ok": True,
                "label": label,
                "model": model,
                "ttft_s": ttft,
                "first_chunk_s": first_chunk_time,
                "total_s": total,
                "chunks": chunks,
                "content_chars": content_chars,
                "prompt_tokens": prompt_tokens,
                "completion_tokens": completion_tokens,
                "done_sentinel": done,
                "throughput_tok_s": (completion_tokens / total) if completion_tokens and total > 0 else None,
                "throughput_chars_s": (content_chars / total) if total > 0 else None,
            }
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        return {"ok": False, "label": label, "model": model, "error": f"HTTP {e.code}", "body": body[:500]}
    except Exception as e:
        return {"ok": False, "label": label, "model": model, "error": str(e)}


SHORT_PROMPT = "用一句话介绍你自己"
# ~200 tokens CJK-ish prompt
MEDIUM_PROMPT = (
    "请用中文写一段约150字的文本，介绍一下计算机科学中的‘操作系统’是什么，"
    "包括它的主要职责（进程管理、内存管理、文件系统、设备管理、用户接口等），"
    "以及常见的操作系统举例（Linux、Windows、macOS、Android、iOS 等）。"
    "语言风格请保持科普、通俗、准确，避免过度学术化，结尾用一句话总结操作系统的重要性。"
)


def main():
    results = []

    print("=== Non-stream tests ===", flush=True)
    for i in range(2):
        r = bench_non_stream("google/gemini-2.5-flash", SHORT_PROMPT, max_tokens=200,
                             label=f"nonstream#{i+1} gemini-flash short")
        print(json.dumps(r, ensure_ascii=False), flush=True)
        results.append(r)

    print("=== Stream tests: short prompt ===", flush=True)
    for model in ["google/gemini-2.5-flash", "anthropic/claude-3.5-haiku-20241022", "anthropic/claude-sonnet-4"]:
        r = bench_stream(model, SHORT_PROMPT, max_tokens=300, label=f"stream short {model}")
        print(json.dumps(r, ensure_ascii=False), flush=True)
        results.append(r)

    print("=== Stream tests: medium prompt ===", flush=True)
    for model in ["google/gemini-2.5-flash", "anthropic/claude-3.5-haiku-20241022"]:
        r = bench_stream(model, MEDIUM_PROMPT, max_tokens=400, label=f"stream medium {model}")
        print(json.dumps(r, ensure_ascii=False), flush=True)
        results.append(r)

    # Save raw JSON
    with open("tests/results.json", "w", encoding="utf-8") as f:
        json.dump(results, f, ensure_ascii=False, indent=2)
    print("Saved tests/results.json", flush=True)


if __name__ == "__main__":
    main()
