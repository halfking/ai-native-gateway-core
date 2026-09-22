#!/usr/bin/env python3
"""Verify deployed identity and HTML-referenced assets. Does NOT certify rendered UI."""
import argparse
import hashlib
from html.parser import HTMLParser
import json
from pathlib import Path
import urllib.parse
import urllib.request


class Assets(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = []

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "script" and attrs.get("src"):
            self.paths.append(attrs["src"])
        if tag == "link" and attrs.get("rel") in ("stylesheet", "modulepreload"):
            self.paths.append(attrs["href"])


def verify(base, expected_sha, expected_seq, dist, fetch):
    checks = []
    def get(path):
        body, headers = fetch(base + path)
        return body, {k.lower(): v for k, v in headers.items()}
    def record(name, ok):
        checks.append({"check": name, "pass": bool(ok)})
    for endpoint in ("/version", "/healthz", "/version.json"):
        body, _ = get(endpoint)
        data = json.loads(body)
        record(endpoint + " identity", data.get("git_sha") == expected_sha and data.get("build_seq") == expected_seq)
        if endpoint == "/healthz":
            record("health ready", data.get("ready") is True)
    body, _ = get("/readyz")
    data = json.loads(body)
    record("DB and Redis ready", data.get("status") == "ready" and all(data.get(k, {}).get("connected") is True for k in ("database", "redis")))
    html, headers = get("/?tab=stream")
    record("HTML matches local dist", html == (dist / "index.html").read_bytes())
    record("HTML revalidates", "no-cache" in headers.get("cache-control", "") or "no-store" in headers.get("cache-control", ""))
    parser = Assets()
    parser.feed(html.decode())
    record("HTML has assets", bool(parser.paths))
    assets = []
    main = b""
    for path in dict.fromkeys(parser.paths):
        parsed = urllib.parse.urlsplit(path)
        if parsed.scheme or parsed.netloc or not path.startswith("/") or ".." in Path(parsed.path).parts:
            raise ValueError("non-local asset reference")
        local = dist / parsed.path.lstrip("/")
        body, headers = get(path)
        record(path + " bytes", body == local.read_bytes())
        expected_type = "javascript" if path.endswith(".js") else "text/css"
        record(path + " MIME", expected_type in headers.get("content-type", ""))
        assets.append({"path": path, "sha256": hashlib.sha256(body).hexdigest(), "bytes": len(body)})
        if path.startswith("/assets/index-") and path.endswith(".js"):
            main += body
    for marker in ("按处理队列", "QueuePerspectivePanel", "RequestJourneyQueues", "NodeStatusMatrix"):
        record("bundle marker " + marker, marker.encode() in main)
    body, headers = get("/menu-config.json")
    record("menu JSON MIME", "application/json" in headers.get("content-type", ""))
    record("menu payload", json.loads(body).get("source") == "gateway-appNav")
    record("menu matches local dist", body == (dist / "menu-config.json").read_bytes())
    return {"pass": all(c["pass"] for c in checks), "expected_sha": expected_sha, "expected_seq": expected_seq, "checks": checks, "assets": assets, "rendered_ui": "NOT_VERIFIED", "cache_root_cause": "NOT_PROVEN"}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--base", required=True)
    p.add_argument("--sha", required=True)
    p.add_argument("--seq", type=int, required=True)
    p.add_argument("--dist", type=Path, required=True)
    args = p.parse_args()
    parsed = urllib.parse.urlsplit(args.base)
    if parsed.scheme != "https" or parsed.path not in ("", "/") or parsed.username or parsed.password:
        p.error("base must be an HTTPS origin")
    def fetch(url):
        request = urllib.request.Request(url, headers={"Cache-Control": "no-cache"})
        with urllib.request.urlopen(request, timeout=15) as response:
            if response.geturl() != url:
                raise ValueError("unexpected redirect")
            body = response.read(8 * 1024 * 1024 + 1)
            if len(body) > 8 * 1024 * 1024:
                raise ValueError("response exceeds 8 MiB")
            return body, dict(response.headers)
    result = verify(args.base.rstrip("/"), args.sha, args.seq, args.dist, fetch)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    raise SystemExit(0 if result["pass"] else 1)


if __name__ == "__main__":
    main()
